package main

import (
	"fmt"
	"time"

	"github.com/OWNER/jevkit/internal/sdlc/enrollment"
	"github.com/OWNER/jevkit/internal/sdlc/ledger"
)

func (a *App) rootRun(run ledger.Run) (ledger.Run, error) {
	for run.ParentRunID != "" {
		var err error
		run, err = ledger.Open(a.sdlcRunsDir(), run.ParentRunID).ReadRun()
		if err != nil {
			return ledger.Run{}, err
		}
	}
	return run, nil
}

func (a *App) treeRemaining(run ledger.Run, p enrollment.Policy) (time.Duration, error) {
	root, err := a.rootRun(run)
	if err != nil {
		return 0, err
	}
	return sdlcRunRemaining(root, p, a.now())
}

// chargeTree serializes child updates at the root. An interrupted write may
// conservatively consume a reservation, but cannot create uncharged work.
func (a *App) chargeTree(run *ledger.Run, p enrollment.Policy, kind string) error {
	root, err := a.rootRun(*run)
	if err != nil {
		return err
	}
	charge := func(r *ledger.Run) error {
		if r.TreeUsage == nil {
			r.TreeUsage = &ledger.TreeUsage{}
			if r.Adaptive != nil {
				r.TreeUsage.Assignments = r.Adaptive.AssignmentCount
				r.TreeUsage.Revisions = r.Adaptive.RevisionCount
				r.TreeUsage.EstimatedCostUSD = r.Adaptive.EstimatedCostUSD
			}
		}
		u := r.TreeUsage
		if p.MaxEstimatedCostUSD > 0 && u.EstimatedCostUSD >= p.MaxEstimatedCostUSD {
			return fmt.Errorf("root cost budget exhausted")
		}
		switch kind {
		case "assignment":
			if u.Assignments >= p.MaxAssignments {
				return fmt.Errorf("root assignment budget exhausted")
			}
			u.Assignments++
		case "revision":
			if u.Revisions >= p.MaxRevisions {
				return fmt.Errorf("root revision budget exhausted")
			}
			u.Revisions++
		case "child":
			if u.ChildRuns >= sdlcMaxChildRuns {
				return fmt.Errorf("root child-run budget exhausted")
			}
			u.ChildRuns++
		case "step":
			if r.StageFlow != nil && u.StageSteps >= r.StageFlow.Workflow.MaxSteps {
				return fmt.Errorf("root stage-step budget exhausted")
			}
			u.StageSteps++
		}
		return nil
	}
	if root.RunID == run.RunID {
		if err := charge(run); err != nil {
			return err
		}
		run.UpdatedAt = a.now().UTC().Format(time.RFC3339)
		return ledger.Open(a.sdlcRunsDir(), run.RunID).WriteRun(*run)
	}
	store := ledger.Open(a.sdlcRunsDir(), root.RunID)
	return store.WithRunLock(func() error {
		r, err := store.ReadRun()
		if err != nil {
			return err
		}
		if err := charge(&r); err != nil {
			return err
		}
		r.UpdatedAt = a.now().UTC().Format(time.RFC3339)
		return store.WriteRun(r)
	})
}

func (a *App) chargeTreeCost(run *ledger.Run, p enrollment.Policy, cost float64) error {
	if cost <= 0 {
		return nil
	}
	root, err := a.rootRun(*run)
	if err != nil {
		return err
	}
	charge := func(r *ledger.Run) error {
		if r.TreeUsage == nil {
			r.TreeUsage = &ledger.TreeUsage{}
		}
		r.TreeUsage.EstimatedCostUSD += cost
		if p.MaxEstimatedCostUSD > 0 && r.TreeUsage.EstimatedCostUSD > p.MaxEstimatedCostUSD {
			return fmt.Errorf("root cost budget exhausted")
		}
		return nil
	}
	if root.RunID == run.RunID {
		costErr := charge(run)
		run.UpdatedAt = a.now().UTC().Format(time.RFC3339)
		if err := ledger.Open(a.sdlcRunsDir(), run.RunID).WriteRun(*run); err != nil {
			return err
		}
		return costErr
	}
	store := ledger.Open(a.sdlcRunsDir(), root.RunID)
	return store.WithRunLock(func() error {
		r, err := store.ReadRun()
		if err != nil {
			return err
		}
		costErr := charge(&r)
		r.UpdatedAt = a.now().UTC().Format(time.RFC3339)
		if err := store.WriteRun(r); err != nil {
			return err
		}
		return costErr
	})
}
