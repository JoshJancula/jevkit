package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/OWNER/jevkit/internal/sdlc/adaptive"
	"github.com/OWNER/jevkit/internal/sdlc/ledger"
)

func TestDecisionJourneyLogsAndUsage(t *testing.T) {
	a := newApp(t)
	id := "journey"
	st, err := adaptive.New("feature", "lean", 1, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	st.Stage = adaptive.Done
	store := ledger.Open(a.sdlcRunsDir(), id)
	now := time.Now().UTC().Format(time.RFC3339)
	if err := store.WriteRun(ledger.Run{RunID: id, Workflow: "feature", CreatedAt: now, UpdatedAt: now, Adaptive: &st, Usage: []ledger.InvocationUsage{{Invocation: "inv", Agent: "claude-builder", Runtime: "claude", Role: "implementer"}}}); err != nil {
		t.Fatal(err)
	}
	if err := a.recordDecision(store, ledger.Decision{RunID: id, Kind: "specialist-check", Invocation: "inv", Choice: "skip", Outcome: "no eligible research specialist", Next: "continue implementation"}); err != nil {
		t.Fatal(err)
	}
	code, out, errs := run(a, "", "sdlc", "logs", id, "--stream", "decisions")
	if code != exitOK || errs != "" || !strings.Contains(out, "specialist-check") || !strings.Contains(out, "continue implementation") {
		t.Fatalf("logs: %d %q %q", code, out, errs)
	}
	code, out, errs = run(a, "", "sdlc", "usage", id)
	if code != exitOK || errs != "" || !strings.Contains(out, "input unknown, output unknown") {
		t.Fatalf("usage: %d %q %q", code, out, errs)
	}
}

func TestSessionStrategyPersistsAndNewRoleStartsFresh(t *testing.T) {
	a := newApp(t)
	id := "sessions"
	st, err := adaptive.New("feature", "lean", 1, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	store := ledger.Open(a.sdlcRunsDir(), id)
	r := ledger.Run{RunID: id, Workflow: "feature", CreatedAt: time.Now().UTC().Format(time.RFC3339), Adaptive: &st, Sessions: map[string]string{"binding/planner": "prior-session"}}
	if err := store.WriteRun(r); err != nil {
		t.Fatal(err)
	}
	if err := a.setRunSessionStrategy(id, "resume"); err != nil {
		t.Fatal(err)
	}
	r, err = store.ReadRun()
	if err != nil || r.SessionStrategy != "resume" {
		t.Fatalf("persist: %+v %v", r, err)
	}
	role, session, err := a.chooseSession(context.Background(), store, r, adaptive.Assignment{InvocationID: "inv", Binding: "binding", Role: "implementer"}, "auto")
	if err != nil || role != "fresh" || session != "" {
		t.Fatalf("new role: %q %q %v", role, session, err)
	}
	role, session, err = a.chooseSession(context.Background(), store, r, adaptive.Assignment{InvocationID: "inv2", Binding: "binding", Role: "planner"}, "auto")
	if err != nil || role != "resume" || session != "prior-session" {
		t.Fatalf("same role: %q %q %v", role, session, err)
	}
}
