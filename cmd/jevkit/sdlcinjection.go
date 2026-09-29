package main

import (
	"fmt"
	"time"

	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
)

func (a *App) sdlcPauseInjectionReview(runID, id string) error {
	store := ledger.Open(a.sdlcRunsDir(), runID)
	err := store.WithRunLock(func() error {
		run, err := store.ReadRun()
		if err != nil {
			return err
		}
		if run.Adaptive == nil {
			return fmt.Errorf("run has no adaptive state")
		}
		st := run.Adaptive
		if st.Stage != adaptive.Paused {
			st.PendingPhase = st.Stage
		}
		st.PendingReason = "Review tool output before resuming: jevkit security review " + id
		st.Pause("injection-review-required")
		run.UpdatedAt = a.now().UTC().Format(time.RFC3339)
		return store.WriteRun(run)
	})
	if err != nil {
		return err
	}
	return failf("run %s paused for prompt-injection review %s; use jevkit security review %s", runID, id, id)
}
