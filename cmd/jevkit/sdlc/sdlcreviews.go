package sdlc

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/enrollment"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
)

// sdlcDriveParallelReviews reserves eligible read-only assessor/specialist
// slots up to the shared run-tree concurrency cap, launches them together
// against the same candidate revision and verification receipts, and cancels
// excess work when a decisive rejection or quorum result lands.
func (a *App) sdlcDriveParallelReviews(ctx context.Context, runID string) error {
	if a.outputMu == nil {
		a.outputMu = &sync.Mutex{}
	}
	store := ledger.Open(a.SDLCRunsDir(), runID)
	policy, _, err := a.sdlcEnrollment()
	if err != nil {
		return app.Failf("%v", err)
	}

	for {
		run, err := store.ReadRun()
		if err != nil {
			return app.Failf("read run: %v", err)
		}
		if run.Adaptive == nil || !run.Adaptive.ParallelReviews() {
			return nil
		}
		if run.Adaptive.ReviewSlotsNeeded() == 0 {
			break
		}
		newSlots, err := a.reviewAdmitCap(run, policy)
		if err != nil {
			return err
		}
		if newSlots <= 0 {
			if len(run.Adaptive.Assignments) == 0 {
				return app.Failf("run %s cannot admit a review and has no active reviewers; inspect concurrency policy and remaining run budgets", runID)
			}
			break
		}
		assignment, err := a.sdlcAssignNext(ctx, runID)
		if err != nil {
			return err
		}
		if assignment == nil {
			break
		}
		a.Outf("  reserved %s: %s (%s)\n", assignment.Role, assignment.AgentID, assignment.InvocationID)
	}

	run, err := store.ReadRun()
	if err != nil {
		return app.Failf("read run: %v", err)
	}
	if run.Adaptive == nil {
		return app.Failf("run %s has an unsupported run format", runID)
	}
	if !run.Adaptive.ParallelReviews() {
		a.Outf("run %s: %s (%s)\n", runID, run.Adaptive.Stage, run.Adaptive.Outcome)
		return nil
	}
	pending := run.Adaptive.Pending()
	if len(pending) == 0 {
		return app.Failf("run %s has an unfinished review stage with no pending work; inspect the saved review queue and quorum", runID)
	}

	a.Outf("run %s: launching %d concurrent review(s) for stage %s\n",
		runID, len(pending), run.Adaptive.Stage)

	reviewCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var mu sync.Mutex
	var firstErr error
	var wg sync.WaitGroup
	for _, assignment := range pending {
		assignment := assignment
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := a.sdlcExecuteAssignment(reviewCtx, runID, assignment)
			fresh, readErr := store.ReadRun()
			siblingDecision := readErr == nil && fresh.Adaptive != nil && !fresh.Adaptive.ParallelReviews()
			if siblingDecision {
				cancel()
			}
			if err == nil {
				a.progressFlush()
				return
			}
			// Only drop work cancelled by a sibling's decisive result. Real
			// invocation failures must still fail the drive.
			if siblingDecision && reviewCtx.Err() != nil && ctx.Err() == nil &&
				(errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
				_ = a.sdlcNoteCancelledReview(runID, assignment, "cancelled after decisive review decision")
				a.progressFlush()
				return
			}
			mu.Lock()
			if firstErr == nil {
				firstErr = err
			}
			mu.Unlock()
			cancel()
		}()
	}
	wg.Wait()
	a.progressFlush()
	if firstErr != nil {
		return firstErr
	}

	run, err = store.ReadRun()
	if err != nil {
		return app.Failf("read run: %v", err)
	}
	if run.Adaptive != nil && run.Adaptive.ParallelReviews() && len(run.Adaptive.Assignments) > 0 {
		a.Outf("run %s: %d review(s) still pending\n", runID, len(run.Adaptive.Assignments))
		return nil
	}
	if run.Adaptive != nil {
		a.Outf("run %s: %s", runID, run.Adaptive.Stage)
		if run.Adaptive.Outcome != "" {
			a.Outf(" (%s)", run.Adaptive.Outcome)
		}
		a.Outf("\n")
	}
	return nil
}

func (a *App) reviewAdmitCap(run ledger.Run, policy enrollment.Policy) (int, error) {
	if run.Adaptive == nil {
		return 0, fmt.Errorf("run has no adaptive state")
	}
	remaining, err := a.treeRemaining(run, policy)
	if err != nil {
		return 0, app.Failf("%v", err)
	}
	if remaining <= 0 {
		return 0, app.Failf("review admission blocked: run-time budget exhausted")
	}
	root, err := a.rootRun(run)
	if err != nil {
		return 0, app.Failf("%v", err)
	}
	rootUsage := root.TreeUsage
	if rootUsage == nil {
		rootUsage = &ledger.TreeUsage{}
	}
	candidates, err := a.adaptiveCandidates(*run.Adaptive, a.cliReach())
	if err != nil {
		return 0, app.Failf("%v", err)
	}
	costOK := policy.MaxEstimatedCostUSD <= 0 || rootUsage.EstimatedCostUSD < policy.MaxEstimatedCostUSD
	remainAssign := policy.MaxAssignments - rootUsage.Assignments
	if remainAssign < 0 {
		remainAssign = 0
	}
	if run.Adaptive.MaxAssignments > 0 {
		left := run.Adaptive.MaxAssignments - run.Adaptive.AssignmentCount
		if left < remainAssign {
			remainAssign = left
		}
	}
	if !costOK {
		return 0, app.Failf("review admission blocked: run-tree cost budget exhausted")
	}
	if remainAssign <= 0 {
		if len(run.Adaptive.Assignments) > 0 {
			return 0, nil // Already-charged reservations may still execute.
		}
		return 0, app.Failf("review admission blocked: assignment budget exhausted")
	}
	needed := run.Adaptive.ReviewSlotsNeeded()
	effective := run.Adaptive.MaxConcurrent
	if policy.MaxConcurrent > 0 && policy.MaxConcurrent < effective {
		effective = policy.MaxConcurrent
	}
	// Candidates and assignment budgets already exclude reserved work. AdmitCap
	// subtracts in-flight work, so supply totals to avoid subtracting it twice.
	inFlight := len(run.Adaptive.Assignments)
	eligible := inFlight + enrollment.DistinctBindings(candidates)
	if run.Adaptive.Stage == adaptive.Specializing {
		// Each specialist role has its own candidate pool. In-flight reviewers
		// for other roles must not consume this role's eligibility budget.
		eligible = len(run.Adaptive.Assignments) + needed
	}
	budgets := adaptive.AdmitBudgets{
		PolicyMax:            policy.MaxConcurrent,
		RunTreeLimit:         run.Adaptive.MaxConcurrent,
		EligibleBindings:     eligible,
		RemainingAssignments: inFlight + remainAssign,
		TimeRemaining:        remaining,
		CostRemainingOK:      costOK,
	}
	capN := adaptive.AdmitCap(budgets, effective, len(run.Adaptive.Assignments))
	if needed >= 0 && capN > needed {
		capN = needed
	}
	if capN < 0 {
		return 0, nil
	}
	return capN, nil
}

func (a *App) sdlcNoteCancelledReview(runID string, assignment adaptive.Assignment, reason string) error {
	store := ledger.Open(a.SDLCRunsDir(), runID)
	return store.WithRunLock(func() error {
		run, err := store.ReadRun()
		if err != nil {
			return err
		}
		if run.Adaptive == nil {
			return nil
		}
		if _, ok := run.Adaptive.Assignments[assignment.InvocationID]; ok {
			delete(run.Adaptive.Assignments, assignment.InvocationID)
			run.UpdatedAt = a.Clock().UTC().Format(time.RFC3339)
			if err := store.WriteRun(run); err != nil {
				return err
			}
		}
		_ = store.AppendEvent(ledger.Event{
			At: a.Clock().UTC().Format(time.RFC3339), RunID: runID, Stage: run.Adaptive.Stage,
			Outcome: "cancelled", Agent: assignment.AgentID, Runtime: assignment.Runtime,
			Invocation: assignment.InvocationID, Reason: reason,
		})
		return a.recordDecision(store, ledger.Decision{
			RunID: runID, Kind: "invocation-outcome", Stage: assignment.Role,
			Invocation: assignment.InvocationID, Runtime: assignment.Runtime,
			Trigger: assignment.AgentID, Choice: "cancelled", Outcome: run.Adaptive.Stage,
			Detail: reason, Next: "usage already incurred remains charged",
		})
	})
}

func (a *App) sdlcIgnoreStaleReview(store *ledger.Store, runID string, run *ledger.Run, result adaptive.Result, reason string) error {
	if result.CostUSD > 0 {
		policy, _, err := a.sdlcEnrollment()
		if err == nil {
			_ = a.chargeTreeCost(run, policy, result.CostUSD)
			_ = store.WriteRun(*run)
		}
	}
	_ = a.recordDecision(store, ledger.Decision{
		RunID: runID, Kind: "invocation-outcome", Stage: run.Adaptive.Stage,
		Invocation: result.InvocationID, Trigger: result.AgentID, Choice: "cancelled",
		Outcome: run.Adaptive.Stage, Detail: reason + "; outcome was " + result.Outcome,
		Next: "usage already incurred remains charged",
	})
	a.Outf("run %s: ignored stale review %s (%s)\n", runID, result.InvocationID, reason)
	return nil
}
