package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OWNER/jevkit/internal/sdlc/adaptive"
	"github.com/OWNER/jevkit/internal/sdlc/enrollment"
	"github.com/OWNER/jevkit/internal/sdlc/ledger"
	"github.com/OWNER/jevkit/internal/sdlc/stageflow"
)

// A root run and up to three child levels may compose SDLC workflows. Child
// assignments, revisions, and reported cost count against the parent's budget.
const sdlcMaxChildDepth = 3
const sdlcMaxChildRuns = 8

func (a *App) validateSpawnTargets(wf availableWorkflow) error {
	if wf.Builtin {
		return nil
	}
	for _, stage := range wf.W.Stages {
		if stage.Spawn == nil {
			continue
		}
		target, err := a.resolveWorkflowByName(stage.Spawn.Workflow)
		if err != nil {
			return fmt.Errorf("spawn stage %q: %w", stage.ID, err)
		}
		if !target.Builtin {
			parentPath, parentErr := filepath.Abs(wf.Path)
			childPath, childErr := filepath.Abs(target.Path)
			if parentErr == nil && childErr == nil && parentPath == childPath {
				return fmt.Errorf("spawn stage %q cannot run its own workflow", stage.ID)
			}
		}
	}
	return nil
}

func (a *App) sdlcDriveSpawn(ctx context.Context, runID string, store *ledger.Store) error {
	var childID string
	var remaining time.Duration
	err := store.WithRunLock(func() error {
		parent, err := store.ReadRun()
		if err != nil {
			return failf("read run: %v", err)
		}
		if parent.StageFlow == nil || parent.Adaptive == nil || parent.Adaptive.Stage != "spawn" {
			return failf("run %s is no longer at a workflow stage", runID)
		}
		stage, ok := parent.StageFlow.Stage()
		if !ok || stage.Spawn == nil {
			return failf("run %s has an invalid workflow stage", runID)
		}
		for _, transition := range parent.StageFlow.Transitions {
			if transition.ChildRunID == "" {
				continue
			}
			previous, err := ledger.Open(a.sdlcRunsDir(), transition.ChildRunID).ReadRun()
			if err != nil {
				return failf("read previous child run: %v", err)
			}
			if previous.Workflow == stage.Spawn.Workflow {
				return a.pauseSpawnLocked(parent, store, "workflow-target-repeated")
			}
		}
		policy, _, err := a.sdlcEnrollment()
		if err != nil {
			return failf("%v", err)
		}
		remaining, err = a.treeRemaining(parent, policy)
		if err != nil {
			return failf("%v", err)
		}
		if remaining <= 0 {
			return a.pauseSpawnLocked(parent, store, "run-time-budget-exhausted")
		}
		if parent.Depth >= sdlcMaxChildDepth {
			return a.pauseSpawnLocked(parent, store, "workflow-depth-exhausted")
		}
		rootID := parent.RunID
		for ancestor := parent; ancestor.ParentRunID != ""; {
			rootID = ancestor.ParentRunID
			ancestor, err = ledger.Open(a.sdlcRunsDir(), rootID).ReadRun()
			if err != nil {
				return failf("read ancestor run: %v", err)
			}
		}
		if tree, err := a.sdlcTree(rootID); err != nil {
			return failf("read run tree: %v", err)
		} else if len(tree) > sdlcMaxChildRuns {
			return a.pauseSpawnLocked(parent, store, "workflow-child-budget-exhausted")
		}
		if parent.Adaptive.BudgetExhausted() || parent.Adaptive.MaxAssignments-parent.Adaptive.AssignmentCount < 1 || parent.Adaptive.MaxRevisions-parent.Adaptive.RevisionCount < 1 {
			return a.pauseSpawnLocked(parent, store, "workflow-budget-exhausted")
		}
		if parent.StageFlow.Workflow.MaxSteps-parent.StageFlow.Steps < 2 {
			return a.pauseSpawnLocked(parent, store, "stage-step-budget-exhausted")
		}
		if parent.StageFlow.ChildRunID != "" {
			childID = parent.StageFlow.ChildRunID
			return nil
		}
		childID = fmt.Sprintf("%s-c%d", runID, parent.StageFlow.Steps)
		childStore := ledger.Open(a.sdlcRunsDir(), childID)
		if child, err := childStore.ReadRun(); err == nil {
			if child.ParentRunID != runID || child.Depth != parent.Depth+1 {
				return failf("child run ID %s is already in use", childID)
			}
		} else if errors.Is(err, os.ErrNotExist) {
			if err := a.chargeTree(&parent, policy, "child"); err != nil {
				return a.pauseSpawnLocked(parent, store, "workflow-child-budget-exhausted")
			}
			if err := a.createSpawnChild(parent, stage.Spawn.Workflow, stage.Spawn.Objective, childID, childStore); err != nil {
				a.outf("  workflow %s: cannot start: %v\n", stage.Spawn.Workflow, err)
				return a.pauseSpawnLocked(parent, store, "workflow-start-failed")
			}
		} else {
			return failf("read child run: %v", err)
		}
		parent.StageFlow.ChildRunID = childID
		parent.UpdatedAt = a.now().UTC().Format(time.RFC3339)
		if err := store.WriteRun(parent); err != nil {
			return failf("store child link: %v", err)
		}
		a.outf("  workflow %s: started run %s\n", stage.Spawn.Workflow, childID)
		return nil
	})
	if err != nil || childID == "" {
		return err
	}
	a.progressFlush()
	childStore := ledger.Open(a.sdlcRunsDir(), childID)
	child, err := childStore.ReadRun()
	if err != nil {
		return failf("read child run: %v", err)
	}
	var driveErr error
	if child.Adaptive != nil && (child.Adaptive.Role() != "" || child.Adaptive.Stage == "question" || child.Adaptive.Stage == "spawn") {
		stepCtx, cancel := context.WithTimeout(ctx, remaining)
		driveErr = a.sdlcDrive(stepCtx, childID)
		cancel()
		child, err = childStore.ReadRun()
		if err != nil {
			return failf("read child run: %v", err)
		}
	}
	if child.Adaptive == nil {
		return failf("child run %s has an unsupported run format", childID)
	}
	if child.Adaptive.Stage != adaptive.Done && child.Adaptive.Stage != adaptive.Paused {
		return driveErr
	}
	if child.Adaptive.Stage == adaptive.Paused && child.Adaptive.Outcome == "plan-approval-required" {
		return store.WithRunLock(func() error {
			parent, err := store.ReadRun()
			if err != nil {
				return err
			}
			parent.Adaptive.PendingDecision = "child-plan-approval"
			parent.Adaptive.PendingPhase = "spawn"
			parent.Adaptive.PendingReason = "Review and approve the plan for child run " + childID + "."
			parent.Adaptive.Pause("child-plan-approval-required")
			parent.UpdatedAt = a.now().UTC().Format(time.RFC3339)
			return store.WriteRun(parent)
		})
	}
	return store.WithRunLock(func() error {
		parent, err := store.ReadRun()
		if err != nil {
			return failf("read parent run: %v", err)
		}
		if parent.StageFlow == nil || parent.Adaptive == nil || parent.StageFlow.ChildRunID != childID || parent.Adaptive.Stage != "spawn" {
			return failf("parent run %s changed while child %s was active", runID, childID)
		}
		outcome := "succeeded"
		if child.Adaptive.Stage == adaptive.Paused {
			outcome = "paused"
		} else if child.Adaptive.Outcome == "aborted" {
			outcome = "aborted"
		}
		parent.Adaptive.AssignmentCount += child.Adaptive.AssignmentCount
		parent.Adaptive.RevisionCount += child.Adaptive.RevisionCount
		parent.Adaptive.EstimatedCostUSD += child.Adaptive.EstimatedCostUSD
		if child.StageFlow != nil {
			parent.StageFlow.Steps += child.StageFlow.Steps
		}
		for _, name := range []string{"plan.md", "patch.diff"} {
			if data, err := childStore.ReadArtifact(name); err == nil {
				if err := store.WriteArtifact(name, data); err != nil {
					return failf("copy child artifact %s: %v", name, err)
				}
			}
		}
		if child.Adaptive.PlanRevision != "" {
			parent.Adaptive.PlanRevision = child.Adaptive.PlanRevision
		}
		if child.Adaptive.DiffRevision != "" {
			parent.Adaptive.DiffRevision = child.Adaptive.DiffRevision
		}
		if err := parent.StageFlow.Advance(outcome, parent.Adaptive); err != nil {
			return failf("advance workflow stage: %v", err)
		}
		policy, _, err := a.sdlcEnrollment()
		if err != nil {
			return err
		}
		if err := a.chargeTree(&parent, policy, "step"); err != nil {
			parent.Adaptive.Pause("stage-step-budget-exhausted")
		}
		parent.UpdatedAt = a.now().UTC().Format(time.RFC3339)
		if err := store.WriteRun(parent); err != nil {
			return failf("store parent run: %v", err)
		}
		a.outf("  workflow %s: %s → %s (run %s)\n", child.Workflow, outcome, parent.StageFlow.Current, childID)
		return nil
	})
}

func (a *App) pauseSpawnLocked(parent ledger.Run, store *ledger.Store, reason string) error {
	parent.Adaptive.Pause(reason)
	parent.UpdatedAt = a.now().UTC().Format(time.RFC3339)
	if err := store.WriteRun(parent); err != nil {
		return failf("store parent pause: %v", err)
	}
	a.outf("  workflow stage %s: paused (%s)\n", parent.StageFlow.Current, reason)
	return failf("run %s paused: %s", parent.RunID, reason)
}

func (a *App) createSpawnChild(parent ledger.Run, name, objective, childID string, store *ledger.Store) error {
	target, err := a.resolveWorkflowByName(name)
	if err != nil {
		return err
	}
	for ancestor := parent; ; {
		if ancestor.Workflow == target.Name {
			return fmt.Errorf("workflow %q is already in the parent ancestry", target.Name)
		}
		if ancestor.ParentRunID == "" {
			break
		}
		ancestor, err = ledger.Open(a.sdlcRunsDir(), ancestor.ParentRunID).ReadRun()
		if err != nil {
			return fmt.Errorf("read ancestor run: %w", err)
		}
	}
	if !target.Builtin {
		if err := a.validateSpawnTargets(target); err != nil {
			return err
		}
	}
	pf, err := a.sdlcPreflight(parent.Adaptive.Profile, a.cliReach())
	if err != nil {
		return fmt.Errorf("child workflow cannot start under policy %s: %w", parent.Adaptive.Profile, err)
	}
	if len(pf.Missing) > 0 {
		return fmt.Errorf("child workflow cannot start under policy %s: %s", parent.Adaptive.Profile, strings.Join(pf.Missing, "; "))
	}
	quorum := pf.Quorum
	if parent.Adaptive.Quorum > quorum {
		quorum = parent.Adaptive.Quorum
	}
	if enrollment.DistinctBindings(pf.Roles["assessor"]) < quorum {
		return fmt.Errorf("child workflow needs %d independent eligible assessors", quorum)
	}
	policy, _, err := a.sdlcEnrollment()
	if err != nil {
		return err
	}
	concurrent := policy.MaxConcurrent
	if parent.Adaptive.MaxConcurrent < concurrent {
		concurrent = parent.Adaptive.MaxConcurrent
	}
	st, err := adaptive.New(target.Name, parent.Adaptive.Profile, quorum, concurrent, parent.Adaptive.MaxRevisions-parent.Adaptive.RevisionCount)
	if err != nil {
		return err
	}
	st.MaxAssignments = parent.Adaptive.MaxAssignments - parent.Adaptive.AssignmentCount
	if policy.MaxAssignments < st.MaxAssignments {
		st.MaxAssignments = policy.MaxAssignments
	}
	if policy.MaxRevisions < st.MaxRevisions {
		st.MaxRevisions = policy.MaxRevisions
	}
	if parent.Adaptive.MaxEstimatedCostUSD > 0 {
		st.MaxEstimatedCostUSD = parent.Adaptive.MaxEstimatedCostUSD - parent.Adaptive.EstimatedCostUSD
	}
	if policy.MaxEstimatedCostUSD > 0 && (st.MaxEstimatedCostUSD == 0 || policy.MaxEstimatedCostUSD < st.MaxEstimatedCostUSD) {
		st.MaxEstimatedCostUSD = policy.MaxEstimatedCostUSD
	}
	var flow *stageflow.State
	if !target.Builtin {
		f, err := stageflow.New(*target.W, &st)
		if err != nil {
			return err
		}
		remainingSteps := parent.StageFlow.Workflow.MaxSteps - parent.StageFlow.Steps - 1
		if f.Workflow.MaxSteps > remainingSteps {
			f.Workflow.MaxSteps = remainingSteps
		}
		flow = &f
	}
	task := parent.Task
	if objective != "" {
		task += "\n\nSubworkflow objective: " + objective
	}
	now := a.now().UTC().Format(time.RFC3339)
	child := ledger.Run{RunID: childID, WorkDir: parent.WorkDir, AllowRead: append([]string(nil), parent.AllowRead...), ParentRunID: parent.RunID, Depth: parent.Depth + 1, Workflow: target.Name, Task: task, CreatedAt: now, UpdatedAt: now, Adaptive: &st, StageFlow: flow, RequirePlanApproval: parent.RequirePlanApproval}
	if !target.Builtin {
		raw, err := os.ReadFile(target.Path)
		if err != nil {
			return err
		}
		child.GraphSHA256 = fmt.Sprintf("%x", sha256.Sum256(raw))
	}
	return store.WriteRun(child)
}
