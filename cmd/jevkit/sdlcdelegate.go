package main

import (
	"context"
	"fmt"
	"time"

	"github.com/OWNER/jevkit/internal/registry"
	"github.com/OWNER/jevkit/internal/sdlc/adaptive"
	"github.com/OWNER/jevkit/internal/sdlc/enrollment"
	"github.com/OWNER/jevkit/internal/sdlc/ledger"
	"github.com/OWNER/jevkit/internal/sdlc/route"
	"github.com/OWNER/jevkit/internal/sdlc/spec"
)

func (a *App) sdlcDelegationAllowed(p enrollment.Policy) (bool, error) {
	if a.sdlcDelegateChoice != nil {
		if !*a.sdlcDelegateChoice {
			return false, nil
		}
		if p.AdaptiveBuiltinDelegation == "off" {
			return false, usagef("project policy does not permit built-in delegation")
		}
		return true, nil
	}
	return p.AdaptiveBuiltinDelegation == "on", nil
}

func (a *App) sdlcMaybeDelegate(ctx context.Context, run ledger.Run) (bool, error) {
	if !run.DelegateBuiltins || run.StageFlow != nil || run.Adaptive == nil || run.Adaptive.Stage != adaptive.Implementing || run.Adaptive.PlanRevision == "" {
		return false, nil
	}
	store := ledger.Open(a.sdlcRunsDir(), run.RunID)
	p, _, err := a.sdlcEnrollment()
	if err != nil {
		return false, err
	}
	remaining, err := a.treeRemaining(run, p)
	if err != nil {
		return false, err
	}
	if remaining <= 0 {
		run.Adaptive.Pause("run-time-budget-exhausted")
		_ = store.WriteRun(run)
		return true, failf("run %s paused: %s", run.RunID, run.Adaptive.Outcome)
	}
	if run.AutoChildRunID != "" {
		return a.driveAutoChild(ctx, run)
	}
	if child, err := ledger.Open(a.sdlcRunsDir(), run.RunID+"-a1").ReadRun(); err == nil && child.ParentRunID == run.RunID {
		run.AutoChildRunID = child.RunID
		run.AutoDecisionDone = true
		if err := store.WriteRun(run); err != nil {
			return true, err
		}
		return a.driveAutoChild(ctx, run)
	}
	if run.AutoDecisionDone {
		return false, nil
	}
	if run.Depth >= sdlcMaxChildDepth {
		return false, nil
	}
	ancestors := map[string]bool{}
	for r := run; ; {
		ancestors[r.Workflow] = true
		if r.ParentRunID == "" {
			break
		}
		var err error
		r, err = ledger.Open(a.sdlcRunsDir(), r.ParentRunID).ReadRun()
		if err != nil {
			return false, err
		}
	}
	criteria := map[string]string{"continue": "Keep the current implementation role on this task."}
	for _, name := range spec.BuiltinNames() {
		if !ancestors[name] {
			criteria[name] = adaptive.TaskKindDescription(name)
		}
	}
	if len(criteria) == 1 {
		return false, nil
	}
	chosen := run.AutoTarget
	delegation := ledger.Decision{RunID: run.RunID, Kind: "delegation", Stage: run.Adaptive.Stage, Trigger: "implementation plan available", Rubrics: criteria}
	for name := range criteria {
		delegation.Candidates = append(delegation.Candidates, ledger.Candidate{ID: name})
	}
	if chosen == "" {
		router, err := a.sdlcRouter()
		if err != nil {
			return a.continueDelegation(run, store, "router unavailable")
		}
		res, err := router.Decide(ctx, "sdlc.builtin-delegation", fmt.Sprintf("Task: %s\nPlan revision: %s\nShould a built-in child workflow handle implementation?", run.Task, run.Adaptive.PlanRevision), route.CriteriaFromRubrics(criteria))
		if err == nil {
			delegation.Confidence = &res.Decision.Confidence
			delegation.Outcome = res.Decision.Decision
			delegation.Detail = res.Decision.Reason
		}
		if err != nil || res.Decision.Decision != registry.Act || res.Decision.Chosen == nil {
			reason := "decision unavailable"
			if err == nil {
				reason = string(res.Decision.Decision)
			}
			return a.continueDelegation(run, store, reason)
		}
		chosen = *res.Decision.Chosen
	}
	if chosen == "continue" {
		run.AutoDecisionDone = true
		if err := store.WriteRun(run); err != nil {
			return false, err
		}
		delegation.Choice, delegation.Next = "continue", "implement in parent"
		return false, a.recordDecision(store, delegation)
	}
	if _, ok := criteria[chosen]; !ok {
		return a.continueDelegation(run, store, "invalid target")
	}
	run.AutoTarget = chosen
	if err := store.WriteRun(run); err != nil {
		return true, err
	}
	if err := a.chargeTree(&run, p, "child"); err != nil {
		run.Adaptive.Pause("workflow-child-budget-exhausted")
		_ = store.WriteRun(run)
		return true, failf("run %s paused: %s", run.RunID, run.Adaptive.Outcome)
	}
	childID := run.RunID + "-a1"
	childStore := ledger.Open(a.sdlcRunsDir(), childID)
	if err := a.createSpawnChild(run, chosen, "Implement the parent plan", childID, childStore); err != nil {
		run.Adaptive.Pause("workflow-start-failed")
		_ = store.WriteRun(run)
		return true, failf("run %s paused: child start: %v", run.RunID, err)
	}
	child, err := childStore.ReadRun()
	if err != nil {
		return false, err
	}
	child.DelegateBuiltins = run.DelegateBuiltins
	child.Adaptive.Stage = adaptive.Implementing
	child.Adaptive.PlanRevision = run.Adaptive.PlanRevision
	child.ApprovedPlanRevision = run.ApprovedPlanRevision
	if plan, err := store.ReadArtifact("plan.md"); err == nil {
		if err := childStore.WriteArtifact("plan.md", plan); err != nil {
			return false, err
		}
	}
	if err := childStore.WriteRun(child); err != nil {
		return false, err
	}
	run.AutoDecisionDone = true
	run.AutoChildRunID = childID
	run.UpdatedAt = a.now().UTC().Format(time.RFC3339)
	if err := store.WriteRun(run); err != nil {
		return false, err
	}
	delegation.Choice, delegation.Next = chosen, "start child run "+childID
	if err := a.recordDecision(store, delegation); err != nil {
		return false, err
	}
	a.outf("  delegated implementation to %s (run %s)\n", chosen, childID)
	return a.driveAutoChild(ctx, run)
}

func (a *App) continueDelegation(run ledger.Run, store *ledger.Store, reason string) (bool, error) {
	run.AutoDecisionDone = true
	run.AutoDecisionReason = "continued in parent: " + reason
	if err := store.WriteRun(run); err != nil {
		return false, err
	}
	return false, a.recordDecision(store, ledger.Decision{RunID: run.RunID, Kind: "delegation", Stage: run.Adaptive.Stage, Trigger: "implementation plan available", Choice: "continue", Outcome: reason, Next: "implement in parent"})
}

func (a *App) driveAutoChild(ctx context.Context, parent ledger.Run) (bool, error) {
	childStore := ledger.Open(a.sdlcRunsDir(), parent.AutoChildRunID)
	child, err := childStore.ReadRun()
	if err != nil {
		return true, err
	}
	if child.Adaptive.Stage != adaptive.Done && child.Adaptive.Stage != adaptive.Paused {
		if err := a.sdlcDrive(ctx, child.RunID); err != nil {
			if latest, readErr := childStore.ReadRun(); readErr == nil && latest.Adaptive != nil && latest.Adaptive.Stage == adaptive.Paused {
				parent.Adaptive.Pause("automatic-child-paused")
				_ = ledger.Open(a.sdlcRunsDir(), parent.RunID).WriteRun(parent)
			}
			return true, err
		}
		child, err = childStore.ReadRun()
		if err != nil {
			return true, err
		}
	}
	if child.Adaptive.Stage == adaptive.Paused {
		parent.Adaptive.Pause("automatic-child-paused")
		_ = ledger.Open(a.sdlcRunsDir(), parent.RunID).WriteRun(parent)
		return true, failf("run %s paused: automatic child %s", parent.RunID, child.RunID)
	}
	if child.Adaptive.Stage != adaptive.Done {
		return true, nil
	}
	parent.Adaptive.AssignmentCount += child.Adaptive.AssignmentCount
	parent.Adaptive.RevisionCount += child.Adaptive.RevisionCount
	parent.Adaptive.EstimatedCostUSD += child.Adaptive.EstimatedCostUSD
	for _, name := range []string{"plan.md", "patch.diff"} {
		if data, err := childStore.ReadArtifact(name); err == nil {
			if err := ledger.Open(a.sdlcRunsDir(), parent.RunID).WriteArtifact(name, data); err != nil {
				return true, err
			}
		}
	}
	if child.Adaptive.PlanRevision != "" {
		parent.Adaptive.PlanRevision = child.Adaptive.PlanRevision
	}
	if child.Adaptive.DiffRevision != "" {
		parent.Adaptive.DiffRevision = child.Adaptive.DiffRevision
	}
	if parent.Adaptive.DiffRevision != "" {
		parent.Adaptive.Stage = adaptive.Assessing
		parent.Adaptive.Assessments = nil
		a.scheduleSpecialists(ctx, parent, parent.Adaptive, "diff")
	} else {
		parent.Adaptive.Stage = adaptive.Done
		parent.Adaptive.Outcome = child.Adaptive.Outcome
	}
	parent.UpdatedAt = a.now().UTC().Format(time.RFC3339)
	return true, ledger.Open(a.sdlcRunsDir(), parent.RunID).WriteRun(parent)
}
