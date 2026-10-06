package sdlc

import (
	"context"
	"testing"

	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/enrollment"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/sdlc/worker"
)

func TestToolPolicyPartitionsSavedSessions(t *testing.T) {
	a := newApp(t)
	store := ledger.Open(a.SDLCRunsDir(), "tool-sessions")
	st, err := adaptive.New("feature", "lean", 1, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteRun(ledger.Run{RunID: "tool-sessions", Adaptive: &st}); err != nil {
		t.Fatal(err)
	}
	assignment := adaptive.Assignment{InvocationID: "inv", AgentID: "coder", Binding: "runtime:codex:m::", Runtime: "codex", Role: "implementer"}
	if err := a.saveInvocationUsage(store, assignment, "m", worker.Reply{SessionID: "unrestricted-session"}); err != nil {
		t.Fatal(err)
	}
	assignment.ToolPolicyFingerprint = (&enrollment.ToolPolicy{Auto: true}).Fingerprint()
	inherited, err := store.ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	strategy, session, err := a.chooseSession(context.Background(), store, inherited, assignment, "resume")
	if err != nil || strategy != "resume" || session != "unrestricted-session" {
		t.Fatalf("auto did not reuse inherited session: %s %s %v", strategy, session, err)
	}
	off := false
	assignment.ToolPolicyFingerprint = (&enrollment.ToolPolicy{Web: &off}).Fingerprint()
	saved, err := store.ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	strategy, session, err = a.chooseSession(context.Background(), store, saved, assignment, "resume")
	if err == nil || strategy != "" || session != "" {
		t.Fatalf("reused session with different tool policy: %s %s %v", strategy, session, err)
	}
	if err := a.saveInvocationUsage(store, assignment, "m", worker.Reply{SessionID: "restricted-session"}); err != nil {
		t.Fatal(err)
	}
	saved, err = store.ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	strategy, session, err = a.chooseSession(context.Background(), store, saved, assignment, "resume")
	if err != nil || strategy != "resume" || session != "restricted-session" {
		t.Fatalf("same policy did not resume: %s %s %v", strategy, session, err)
	}
	assignment.ToolPolicyFingerprint = (&enrollment.ToolPolicy{Shell: &off}).Fingerprint()
	strategy, session, err = a.chooseSession(context.Background(), store, saved, assignment, "resume")
	if err == nil || strategy != "" || session != "" {
		t.Fatalf("changed policy reused a session: %s %s %v", strategy, session, err)
	}
}

func TestSaveInvocationUsagePersistsToolCalls(t *testing.T) {
	a := newApp(t)
	id := "run-20260928T120000Z-toolcalls"
	store := ledger.Open(a.SDLCRunsDir(), id)
	if err := store.WriteRun(ledger.Run{RunID: id}); err != nil {
		t.Fatal(err)
	}
	tools := int64(0)
	assignment := adaptive.Assignment{InvocationID: "inv", AgentID: "builder", Runtime: "codex", Role: "implementer"}
	if err := a.saveInvocationUsage(store, assignment, "gpt", worker.Reply{ToolCalls: &tools, ElapsedMS: 4200}); err != nil {
		t.Fatal(err)
	}
	run, err := store.ReadRun()
	if err != nil || len(run.Usage) != 1 || run.Usage[0].ToolCalls == nil || *run.Usage[0].ToolCalls != 0 {
		t.Fatalf("saved tool calls: %+v, %v", run.Usage, err)
	}
	if run.Usage[0].ElapsedMS == nil || *run.Usage[0].ElapsedMS != 4200 {
		t.Fatalf("saved elapsed time: %+v", run.Usage[0])
	}
}
