package sdlc

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/enrollment"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/sdlc/stageflow"
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
			return app.Failf("read run: %v", err)
		}
		if parent.StageFlow == nil || parent.Adaptive == nil || parent.Adaptive.Stage != "spawn" {
			return app.Failf("run %s is no longer at a workflow stage", runID)
		}
		stage, ok := parent.StageFlow.Stage()
		if !ok || stage.Spawn == nil {
			return app.Failf("run %s has an invalid workflow stage", runID)
		}
		if parent.StageFlow.ChildRunID != "" {
			childID = parent.StageFlow.ChildRunID
			return nil
		}
		for _, transition := range parent.StageFlow.Transitions {
			if transition.ChildRunID == "" {
				continue
			}
			previous, err := ledger.Open(a.SDLCRunsDir(), transition.ChildRunID).ReadRun()
			if err != nil {
				return app.Failf("read previous child run: %v", err)
			}
			if previous.Workflow == stage.Spawn.Workflow {
				return a.pauseSpawnLocked(parent, store, "workflow-target-repeated")
			}
		}
		policy, _, err := a.sdlcEnrollment()
		if err != nil {
			return app.Failf("%v", err)
		}
		remaining, err = a.treeRemaining(parent, policy)
		if err != nil {
			return app.Failf("%v", err)
		}
		if remaining <= 0 {
			return a.pauseSpawnLocked(parent, store, "run-time-budget-exhausted")
		}
		if parent.Depth >= sdlcMaxChildDepth {
			return a.pauseSpawnLocked(parent, store, "workflow-depth-exhausted")
		}
		if err := a.budgetGate(&parent, policy, "child"); err != nil {
			return err
		}
		if parent.StageFlow.ChildRunID != "" {
			childID = parent.StageFlow.ChildRunID
			return nil
		}
		childID = fmt.Sprintf("%s-c%d", runID, parent.StageFlow.Steps)
		childStore := ledger.Open(a.SDLCRunsDir(), childID)
		if child, err := childStore.ReadRun(); err == nil {
			if child.ParentRunID != runID || child.Depth != parent.Depth+1 {
				return app.Failf("child run ID %s is already in use", childID)
			}
		} else if errors.Is(err, os.ErrNotExist) {
			if err := a.chargeTree(&parent, policy, "child"); err != nil {
				return a.pauseSpawnLocked(parent, store, "workflow-child-budget-exhausted")
			}
			if err := a.createSpawnChild(parent, stage.Spawn.Workflow, stage.Spawn.Objective, childID, childStore); err != nil {
				a.Outf("  workflow %s: cannot start: %v\n", stage.Spawn.Workflow, err)
				return a.pauseSpawnLocked(parent, store, "workflow-start-failed")
			}
		} else {
			return app.Failf("read child run: %v", err)
		}
		parent.StageFlow.ChildRunID = childID
		parent.UpdatedAt = a.Clock().UTC().Format(time.RFC3339)
		if err := store.WriteRun(parent); err != nil {
			return app.Failf("store child link: %v", err)
		}
		a.Outf("  workflow %s: started run %s\n", stage.Spawn.Workflow, childID)
		return nil
	})
	if err != nil || childID == "" {
		return err
	}
	a.progressFlush()
	childStore := ledger.Open(a.SDLCRunsDir(), childID)
	child, err := childStore.ReadRun()
	if err != nil {
		return app.Failf("read child run: %v", err)
	}
	var driveErr error
	if child.Adaptive != nil && (child.Adaptive.Role() != "" || child.Adaptive.Stage == "question" || child.Adaptive.Stage == "spawn" || child.Adaptive.Stage == adaptive.Verifying) {
		stepCtx, cancel := context.WithTimeout(ctx, remaining)
		driveErr = a.sdlcDrive(stepCtx, childID)
		cancel()
		child, err = childStore.ReadRun()
		if err != nil {
			return app.Failf("read child run: %v", err)
		}
	}
	if child.Adaptive == nil {
		return app.Failf("child run %s has an unsupported run format", childID)
	}
	if child.Adaptive.Stage != adaptive.Done && child.Adaptive.Stage != adaptive.Paused {
		return driveErr
	}
	if child.Adaptive.Stage == adaptive.Paused && adaptive.BudgetPause(child.Adaptive.Outcome) {
		return store.WithRunLock(func() error {
			parent, err := store.ReadRun()
			if err != nil {
				return err
			}
			parent.Adaptive.Pause(child.Adaptive.Outcome)
			return store.WriteRun(parent)
		})
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
			parent.UpdatedAt = a.Clock().UTC().Format(time.RFC3339)
			return store.WriteRun(parent)
		})
	}
	return store.WithRunLock(func() error {
		parent, err := store.ReadRun()
		if err != nil {
			return app.Failf("read parent run: %v", err)
		}
		if parent.StageFlow == nil || parent.Adaptive == nil || parent.StageFlow.ChildRunID != childID || parent.Adaptive.Stage != "spawn" {
			return app.Failf("parent run %s changed while child %s was active", runID, childID)
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
		for _, name := range []string{"plan.md", "patch.diff", adaptive.ArtifactChecks, adaptive.ArtifactSubtasks, adaptive.ArtifactVerification} {
			if data, err := childStore.ReadArtifact(name); err == nil {
				if err := store.WriteArtifact(name, data); err != nil {
					return app.Failf("copy child artifact %s: %v", name, err)
				}
			}
		}
		if child.Adaptive.PlanRevision != "" {
			parent.Adaptive.PlanRevision = child.Adaptive.PlanRevision
		}
		if child.Adaptive.ChecksRevision != "" {
			parent.Adaptive.ChecksRevision = child.Adaptive.ChecksRevision
		}
		if child.Adaptive.SubtasksRevision != "" {
			parent.Adaptive.SubtasksRevision = child.Adaptive.SubtasksRevision
		}
		if child.Adaptive.DiffRevision != "" {
			parent.Adaptive.DiffRevision = child.Adaptive.DiffRevision
		}
		if len(child.Adaptive.CheckReceipts) > 0 {
			parent.Adaptive.CheckReceipts = append([]adaptive.CheckReceipt(nil), child.Adaptive.CheckReceipts...)
		}
		if child.Verification != nil {
			rec := *child.Verification
			parent.Verification = &rec
		}
		if err := a.advanceBudgetFlow(&parent, parent.Adaptive, outcome); err != nil {
			return err
		}
		parent.UpdatedAt = a.Clock().UTC().Format(time.RFC3339)
		if err := store.WriteRun(parent); err != nil {
			return app.Failf("store parent run: %v", err)
		}
		a.Outf("  workflow %s: %s → %s (run %s)\n", child.Workflow, outcome, parent.StageFlow.Current, childID)
		return nil
	})
}

func (a *App) pauseSpawnLocked(parent ledger.Run, store *ledger.Store, reason string) error {
	parent.Adaptive.Pause(reason)
	parent.UpdatedAt = a.Clock().UTC().Format(time.RFC3339)
	if err := store.WriteRun(parent); err != nil {
		return app.Failf("store parent pause: %v", err)
	}
	a.Outf("  workflow stage %s: paused (%s)\n", parent.StageFlow.Current, reason)
	return app.Failf("run %s paused: %s", parent.RunID, reason)
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
		ancestor, err = ledger.Open(a.SDLCRunsDir(), ancestor.ParentRunID).ReadRun()
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
	st, err := adaptive.New(target.Name, parent.Adaptive.Profile, quorum, concurrent, max(1, parent.Adaptive.MaxRevisions))
	if err != nil {
		return err
	}
	st.TreeBudget = true
	st.MaxAssignments = parent.Adaptive.MaxAssignments
	st.MaxEstimatedCostUSD = parent.Adaptive.MaxEstimatedCostUSD
	var flow *stageflow.State
	if !target.Builtin {
		f, err := stageflow.New(*target.W, &st)
		if err != nil {
			return err
		}
		flow = &f
	}
	task := parent.Task
	if objective != "" {
		task += "\n\nSubworkflow objective: " + objective
	}
	now := a.Clock().UTC().Format(time.RFC3339)
	child := ledger.Run{RunID: childID, WorkDir: parent.WorkDir, AllowRead: append([]string(nil), parent.AllowRead...), ParentRunID: parent.RunID, Depth: parent.Depth + 1, Workflow: target.Name, Task: task, CreatedAt: now, UpdatedAt: now, Adaptive: &st, StageFlow: flow, RequirePlanApproval: parent.RequirePlanApproval, DelegateBuiltins: parent.DelegateBuiltins, SessionStrategy: parent.SessionStrategy, RuntimeIntegration: parent.RuntimeIntegration}
	if !target.Builtin {
		raw, err := os.ReadFile(target.Path)
		if err != nil {
			return err
		}
		child.GraphSHA256 = fmt.Sprintf("%x", sha256.Sum256(raw))
	}
	return store.WriteRun(child)
}
