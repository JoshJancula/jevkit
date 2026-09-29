package main

import (
	"strings"
	"testing"

	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/security/review"
)

func TestSDLCInjectionReviewPausesAndBlocksResume(t *testing.T) {
	a := newApp(t)
	runID := "injection-run"
	st := adaptive.State{Stage: adaptive.Implementing}
	store := ledger.Open(a.sdlcRunsDir(), runID)
	if err := store.WriteRun(ledger.Run{RunID: runID, WorkDir: a.WorkDir, Adaptive: &st}); err != nil {
		t.Fatal(err)
	}
	r, err := review.Create(a.stateHome(), review.Record{SessionKey: "session", SDLCRunID: runID, ContentSHA256: review.Hash("output")})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.sdlcPauseInjectionReview(runID, r.ID); err == nil {
		t.Fatal("pause should report review")
	}
	stored, err := store.ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if stored.Adaptive.Stage != adaptive.Paused || stored.Adaptive.Outcome != "injection-review-required" || !strings.Contains(stored.Adaptive.PendingReason, r.ID) {
		t.Fatalf("state: %+v", stored.Adaptive)
	}
	if err := a.sdlcResume(t.Context(), runID, true, false, false, false, ""); err == nil || !strings.Contains(err.Error(), r.ID) {
		t.Fatalf("resume was not blocked: %v", err)
	}
	if _, err := review.Resolve(a.stateHome(), r.ID, "deny", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := review.PendingRun(a.stateHome(), runID); ok {
		t.Fatal("run latch remained")
	}
}
