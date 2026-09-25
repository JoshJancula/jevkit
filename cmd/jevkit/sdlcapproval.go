package main

import (
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/OWNER/jevkit/internal/sdlc/adaptive"
	"github.com/OWNER/jevkit/internal/sdlc/ledger"
)

// A run only needs a gate when it is about to assign implementation work.
// The approval is bound to the bytes the implementer will receive as plan.md.
func (a *App) sdlcPlanApprovalGate(runID string) (bool, error) {
	store := ledger.Open(a.sdlcRunsDir(), runID)
	var paused bool
	err := store.WithRunLock(func() error {
		run, err := store.ReadRun()
		if err != nil {
			return err
		}
		paused, err = a.sdlcPlanApprovalGateLocked(run, store)
		return err
	})
	return paused, err
}

func (a *App) sdlcPlanApprovalGateLocked(run ledger.Run, store *ledger.Store) (bool, error) {
	if !run.RequirePlanApproval || run.Adaptive == nil || run.Adaptive.Stage != adaptive.Implementing {
		return false, nil
	}
	st := run.Adaptive
	if st.PlanRevision != "" {
		plan, err := store.ReadArtifact("plan.md")
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(plan)) != st.PlanRevision {
			st.PendingReason = "The saved plan.md no longer matches the planned revision; start a new run."
			st.Pause("plan-artifact-mismatch")
			run.UpdatedAt = a.now().UTC().Format(time.RFC3339)
			return true, store.WriteRun(run)
		}
	}
	if st.PlanRevision != "" && run.ApprovedPlanRevision == st.PlanRevision {
		return false, nil
	}
	if len(st.Assignments) != 0 {
		return false, failf("run %s has an implementation assignment without plan approval", run.RunID)
	}
	if st.PlanRevision == "" {
		st.PendingReason = "This workflow reached implementation without a plan. Add a planner stage or supply a plan."
		st.Pause("plan-required")
	} else {
		st.PendingReason = "Review the saved plan.md and approve it before implementation."
		st.Pause("plan-approval-required")
	}
	st.PendingPhase = adaptive.Implementing
	run.UpdatedAt = a.now().UTC().Format(time.RFC3339)
	if err := store.WriteRun(run); err != nil {
		return false, err
	}
	if err := a.recordDecision(store, ledger.Decision{RunID: run.RunID, Kind: "plan-approval", Stage: adaptive.Implementing,
		Trigger: st.PlanRevision, Choice: "pending", Outcome: st.Outcome, Next: "jevkit sdlc resume " + run.RunID + " --approve-plan"}); err != nil {
		return true, err
	}
	return true, nil
}

func (a *App) sdlcApprovePlan(runID string) error {
	store := ledger.Open(a.sdlcRunsDir(), runID)
	initial, err := store.ReadRun()
	if err != nil {
		return failf("read run: %v", err)
	}
	if initial.Adaptive != nil && initial.Adaptive.Outcome == "child-plan-approval-required" && initial.StageFlow != nil && initial.StageFlow.ChildRunID != "" {
		childID := initial.StageFlow.ChildRunID
		child, err := ledger.Open(a.sdlcRunsDir(), childID).ReadRun()
		if err != nil {
			return err
		}
		if child.Adaptive == nil {
			return failf("child run %s has no plan approval state", childID)
		}
		if child.Adaptive.Stage != adaptive.Done && child.ApprovedPlanRevision != child.Adaptive.PlanRevision {
			if err := a.sdlcApprovePlan(childID); err != nil {
				return err
			}
		}
		return store.WithRunLock(func() error {
			run, err := store.ReadRun()
			if err != nil {
				return err
			}
			if run.Adaptive == nil || run.Adaptive.Outcome != "child-plan-approval-required" || run.StageFlow == nil || run.StageFlow.ChildRunID != childID {
				return failf("run %s changed while approving child plan", runID)
			}
			run.Adaptive.Stage = "spawn"
			run.Adaptive.Outcome = ""
			run.Adaptive.PendingDecision = ""
			run.Adaptive.PendingPhase = ""
			run.Adaptive.PendingReason = ""
			run.UpdatedAt = a.now().UTC().Format(time.RFC3339)
			return store.WriteRun(run)
		})
	}
	return store.WithRunLock(func() error {
		run, err := store.ReadRun()
		if err != nil {
			return failf("read run: %v", err)
		}
		if !run.RequirePlanApproval || run.Adaptive == nil || run.Adaptive.PlanRevision == "" || run.Adaptive.Stage == adaptive.Done {
			return usagef("run %s has no plan awaiting approval", runID)
		}
		if run.ApprovedPlanRevision == run.Adaptive.PlanRevision {
			return usagef("run %s has already approved this plan", runID)
		}
		if run.Adaptive.Stage == adaptive.Paused && run.Adaptive.Outcome != "plan-approval-required" {
			return usagef("run %s is paused for %s, not plan approval", runID, run.Adaptive.Outcome)
		}
		plan, err := store.ReadArtifact("plan.md")
		if err != nil {
			return failf("read plan.md: %v", err)
		}
		if fmt.Sprintf("%x", sha256.Sum256(plan)) != run.Adaptive.PlanRevision {
			return failf("saved plan.md no longer matches the planned revision; start a new run")
		}
		run.ApprovedPlanRevision = run.Adaptive.PlanRevision
		if run.Adaptive.Stage == adaptive.Paused {
			run.Adaptive.Stage = run.Adaptive.PendingPhase
			run.Adaptive.Outcome = ""
			run.Adaptive.PendingPhase = ""
			run.Adaptive.PendingReason = ""
		}
		run.UpdatedAt = a.now().UTC().Format(time.RFC3339)
		if err := store.WriteRun(run); err != nil {
			return err
		}
		return a.recordDecision(store, ledger.Decision{RunID: runID, Kind: "plan-approval", Stage: run.Adaptive.Stage,
			Trigger: run.Adaptive.PlanRevision, Choice: "approved", Next: "continue run"})
	})
}
