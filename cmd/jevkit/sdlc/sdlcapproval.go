package sdlc

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
)

// A run only needs a gate when it is about to assign implementation work.
// Approval is bound to plan.md, checks.json, and subtasks.json digests.
func (a *App) sdlcPlanApprovalGate(runID string) (bool, error) {
	store := ledger.Open(a.SDLCRunsDir(), runID)
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
	if run.Adaptive == nil || run.Adaptive.Stage != adaptive.Implementing {
		return false, nil
	}
	st := run.Adaptive
	if st.PlanRevision != "" {
		b, err := ensurePlanSideArtifacts(store, st, st.MaxConcurrent, remainingAssignmentBudget(run))
		if err != nil {
			return false, err
		}
		run.Adaptive = st
		if err := planArtifactsMatchState(b, st); err != nil {
			st.PendingReason = err.Error() + "; start a new run."
			st.Pause("plan-artifact-mismatch")
			run.UpdatedAt = a.Clock().UTC().Format(time.RFC3339)
			return true, store.WriteRun(run)
		}
		if !run.RequirePlanApproval {
			if err := store.WriteRun(run); err != nil {
				return false, err
			}
			if paused, err := a.sdlcCommandAuthorizationGateLocked(run, store, b); err != nil || paused {
				return paused, err
			}
			return false, nil
		}
		if planApprovalComplete(run) {
			if run.AuthorizedChecksRevision == "" {
				run.AuthorizedChecksRevision = st.ChecksRevision
			}
			run.UpdatedAt = a.Clock().UTC().Format(time.RFC3339)
			if err := store.WriteRun(run); err != nil {
				return false, err
			}
			return false, nil
		}
	}
	if !run.RequirePlanApproval {
		return false, nil
	}
	if len(st.Assignments) != 0 {
		return false, app.Failf("run %s has an implementation assignment without plan approval", run.RunID)
	}
	if st.PlanRevision == "" {
		st.PendingReason = "This workflow reached implementation without a plan. Add a planner stage or supply a plan."
		st.Pause("plan-required")
	} else {
		st.PendingReason = "Review the saved plan.md, proposed checks, and subtask graph before implementation."
		st.Pause("plan-approval-required")
	}
	st.PendingPhase = adaptive.Implementing
	run.UpdatedAt = a.Clock().UTC().Format(time.RFC3339)
	if err := store.WriteRun(run); err != nil {
		return false, err
	}
	if err := a.recordDecision(store, ledger.Decision{RunID: run.RunID, Kind: "plan-approval", Stage: adaptive.Implementing,
		Trigger: st.PlanRevision, Choice: "pending", Outcome: st.Outcome, Next: "jevkit sdlc resume " + run.RunID + " --approve-plan"}); err != nil {
		return true, err
	}
	return true, nil
}

func (a *App) sdlcCommandAuthorizationGateLocked(run ledger.Run, store *ledger.Store, b planArtifactBundle) (bool, error) {
	st := run.Adaptive
	if st == nil {
		return false, nil
	}
	if !argvChecksNeedAuthorization(b.ChecksFile.Checks, run.AuthorizedChecksRevision, b.ChecksDigest) {
		return false, nil
	}
	if len(st.Assignments) != 0 {
		return false, app.Failf("run %s has an implementation assignment without command authorization", run.RunID)
	}
	st.PendingReason = "Authorize the planner-proposed commands before they can run. --auto alone is not permission. Review checks.json, then use jevkit sdlc resume " + run.RunID + " --authorize-checks"
	st.Pause("command-authorization-required")
	st.PendingPhase = adaptive.Implementing
	run.UpdatedAt = a.Clock().UTC().Format(time.RFC3339)
	if err := store.WriteRun(run); err != nil {
		return false, err
	}
	_ = a.recordDecision(store, ledger.Decision{RunID: run.RunID, Kind: "command-authorization", Stage: adaptive.Implementing,
		Trigger: b.ChecksDigest, Choice: "pending", Outcome: st.Outcome, Next: "jevkit sdlc resume " + run.RunID + " --authorize-checks"})
	return true, nil
}

func (a *App) sdlcApprovePlan(runID string) error {
	store := ledger.Open(a.SDLCRunsDir(), runID)
	initial, err := store.ReadRun()
	if err != nil {
		return app.Failf("read run: %v", err)
	}
	if initial.Adaptive != nil && initial.Adaptive.Outcome == "child-plan-approval-required" && initial.StageFlow != nil && initial.StageFlow.ChildRunID != "" {
		childID := initial.StageFlow.ChildRunID
		child, err := ledger.Open(a.SDLCRunsDir(), childID).ReadRun()
		if err != nil {
			return err
		}
		if child.Adaptive == nil {
			return app.Failf("child run %s has no plan approval state", childID)
		}
		if child.Adaptive.Stage != adaptive.Done && !planApprovalComplete(child) {
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
				return app.Failf("run %s changed while approving child plan", runID)
			}
			run.Adaptive.Stage = "spawn"
			run.Adaptive.Outcome = ""
			run.Adaptive.PendingDecision = ""
			run.Adaptive.PendingPhase = ""
			run.Adaptive.PendingReason = ""
			run.UpdatedAt = a.Clock().UTC().Format(time.RFC3339)
			return store.WriteRun(run)
		})
	}
	return store.WithRunLock(func() error {
		run, err := store.ReadRun()
		if err != nil {
			return app.Failf("read run: %v", err)
		}
		if !run.RequirePlanApproval || run.Adaptive == nil || run.Adaptive.PlanRevision == "" || run.Adaptive.Stage == adaptive.Done {
			return app.Usagef("run %s has no plan awaiting approval", runID)
		}
		if planApprovalComplete(run) {
			return app.Usagef("run %s has already approved this plan", runID)
		}
		if run.Adaptive.Stage == adaptive.Paused && run.Adaptive.Outcome != "plan-approval-required" {
			return app.Usagef("run %s is paused for %s, not plan approval", runID, run.Adaptive.Outcome)
		}
		st := run.Adaptive
		b, err := ensurePlanSideArtifacts(store, st, st.MaxConcurrent, remainingAssignmentBudget(run))
		if err != nil {
			return app.Failf("%v", err)
		}
		if err := planArtifactsMatchState(b, st); err != nil {
			return app.Failf("%s; start a new run", err.Error())
		}
		run.ApprovedPlanRevision = st.PlanRevision
		run.ApprovedChecksRevision = st.ChecksRevision
		run.ApprovedSubtasksRevision = st.SubtasksRevision
		run.AuthorizedChecksRevision = st.ChecksRevision
		if run.Adaptive.Stage == adaptive.Paused {
			run.Adaptive.Stage = run.Adaptive.PendingPhase
			run.Adaptive.Outcome = ""
			run.Adaptive.PendingPhase = ""
			run.Adaptive.PendingReason = ""
		}
		run.UpdatedAt = a.Clock().UTC().Format(time.RFC3339)
		if err := store.WriteRun(run); err != nil {
			return err
		}
		return a.recordDecision(store, ledger.Decision{RunID: runID, Kind: "plan-approval", Stage: run.Adaptive.Stage,
			Trigger: run.Adaptive.PlanRevision, Choice: "approved", Detail: "plan=" + st.PlanRevision + " checks=" + st.ChecksRevision + " subtasks=" + st.SubtasksRevision, Next: "continue run"})
	})
}

func (a *App) sdlcAuthorizeChecks(runID string, expectedDigest string) error {
	store := ledger.Open(a.SDLCRunsDir(), runID)
	return store.WithRunLock(func() error {
		run, err := store.ReadRun()
		if err != nil {
			return app.Failf("read run: %v", err)
		}
		if run.Adaptive == nil || run.Adaptive.PlanRevision == "" {
			return app.Usagef("run %s has no planned checks to authorize", runID)
		}
		st := run.Adaptive
		b, err := ensurePlanSideArtifacts(store, st, st.MaxConcurrent, remainingAssignmentBudget(run))
		if err != nil {
			return app.Failf("%v", err)
		}
		if err := planArtifactsMatchState(b, st); err != nil {
			return app.Failf("%s; start a new run", err.Error())
		}
		if expectedDigest != "" && expectedDigest != b.ChecksDigest {
			return app.Failf("authorize-checks digest does not match saved checks.json (%s)", b.ChecksDigest)
		}
		if !adaptive.HasArgvChecks(b.ChecksFile.Checks) {
			return app.Usagef("run %s has no argv checks to authorize", runID)
		}
		if run.AuthorizedChecksRevision == b.ChecksDigest {
			return app.Usagef("run %s already authorized these checks", runID)
		}
		if run.Adaptive.Stage == adaptive.Paused && run.Adaptive.Outcome != "command-authorization-required" && run.Adaptive.Outcome != "plan-approval-required" {
			return app.Usagef("run %s is paused for %s, not command authorization", runID, run.Adaptive.Outcome)
		}
		run.AuthorizedChecksRevision = b.ChecksDigest
		if run.Adaptive.Stage == adaptive.Paused && run.Adaptive.Outcome == "command-authorization-required" {
			run.Adaptive.Stage = run.Adaptive.PendingPhase
			run.Adaptive.Outcome = ""
			run.Adaptive.PendingPhase = ""
			run.Adaptive.PendingReason = ""
		}
		run.UpdatedAt = a.Clock().UTC().Format(time.RFC3339)
		if err := store.WriteRun(run); err != nil {
			return err
		}
		return a.recordDecision(store, ledger.Decision{RunID: runID, Kind: "command-authorization", Stage: run.Adaptive.Stage,
			Trigger: b.ChecksDigest, Choice: "authorized", Next: "continue run"})
	})
}

func (a *App) formatPlanApprovalExtras(b planArtifactBundle, maxConcurrent int) string {
	var out strings.Builder
	out.WriteString("\nPROPOSED CHECKS\n")
	if len(b.ChecksFile.Checks) == 0 {
		out.WriteString("  (none)\n")
	} else {
		for _, c := range b.ChecksFile.Checks {
			out.WriteString("  - id: " + c.ID + "\n")
			if c.Description != "" {
				out.WriteString("    description: " + c.Description + "\n")
			}
			if len(c.Argv) > 0 {
				out.WriteString("    argv: " + strings.Join(quoteArgv(c.Argv), " ") + "\n")
				wd := c.WorkingDir
				if wd == "" {
					wd = "."
				}
				out.WriteString("    workingDir: " + wd + "\n")
				timeout := c.TimeoutSeconds
				if timeout == 0 {
					out.WriteString("    timeoutSeconds: (default)\n")
				} else {
					out.WriteString(fmt.Sprintf("    timeoutSeconds: %d\n", timeout))
				}
			} else {
				out.WriteString("    manual: " + c.Manual + "\n")
			}
		}
	}
	out.WriteString("\nPROPOSED SUBTASK GRAPH\n")
	out.WriteString("  mode: " + b.Graph.Mode + "\n")
	if b.Graph.FallbackReason != "" {
		out.WriteString("  fallbackReason: " + b.Graph.FallbackReason + "\n")
	}
	if b.Graph.IndependenceReason != "" {
		out.WriteString("  independenceReason: " + b.Graph.IndependenceReason + "\n")
	}
	if b.Graph.IntegrationOwner != "" {
		out.WriteString("  integrationOwner: " + b.Graph.IntegrationOwner + "\n")
	}
	if len(b.Graph.SharedPaths) > 0 {
		out.WriteString("  sharedPaths: " + strings.Join(b.Graph.SharedPaths, ", ") + "\n")
	}
	eff := b.Graph.EffectiveConcurrency
	if eff == 0 {
		eff = 1
	}
	if maxConcurrent > 0 && maxConcurrent < eff {
		eff = maxConcurrent
	}
	out.WriteString(fmt.Sprintf("  effectiveConcurrency: %d (policy maxConcurrent=%d)\n", eff, maxConcurrent))
	if len(b.Graph.Subtasks) > 0 {
		ordered := append([]adaptive.Subtask(nil), b.Graph.Subtasks...)
		sort.SliceStable(ordered, func(i, j int) bool {
			if ordered[i].MergeOrder == ordered[j].MergeOrder {
				return ordered[i].ID < ordered[j].ID
			}
			return ordered[i].MergeOrder < ordered[j].MergeOrder
		})
		out.WriteString("  integrationOrder:\n")
		for _, st := range ordered {
			out.WriteString(fmt.Sprintf("    %d. %s — owner scope: %s\n", st.MergeOrder, st.ID, strings.Join(st.OwnedPaths, ", ")))
			if len(st.DependsOn) > 0 {
				out.WriteString("       dependsOn: " + strings.Join(st.DependsOn, ", ") + "\n")
			}
			out.WriteString("       objective: " + st.Objective + "\n")
		}
	}
	out.WriteString("\nARTIFACT DIGESTS\n")
	out.WriteString("  plan.md:      " + b.PlanDigest + "\n")
	out.WriteString("  checks.json:  " + b.ChecksDigest + "\n")
	out.WriteString("  subtasks.json: " + b.SubtasksDigest + "\n")
	return out.String()
}

func quoteArgv(argv []string) []string {
	out := make([]string, len(argv))
	for i, arg := range argv {
		if strings.ContainsAny(arg, " \t\"'") {
			out[i] = fmt.Sprintf("%q", arg)
		} else {
			out[i] = arg
		}
	}
	return out
}
