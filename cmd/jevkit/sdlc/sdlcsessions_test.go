package sdlc

import (
	"testing"

	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/sdlc/worker"
)

func TestSaveInvocationUsagePersistsToolCalls(t *testing.T) {
	a := newApp(t)
	id := "run-20260928T120000Z-toolcalls"
	store := ledger.Open(a.SDLCRunsDir(), id)
	if err := store.WriteRun(ledger.Run{RunID: id}); err != nil {
		t.Fatal(err)
	}
	tools := int64(0)
	assignment := adaptive.Assignment{InvocationID: "inv", AgentID: "builder", Runtime: "codex", Role: "implementer"}
	if err := a.saveInvocationUsage(store, assignment, "gpt", worker.Reply{ToolCalls: &tools}); err != nil {
		t.Fatal(err)
	}
	run, err := store.ReadRun()
	if err != nil || len(run.Usage) != 1 || run.Usage[0].ToolCalls == nil || *run.Usage[0].ToolCalls != 0 {
		t.Fatalf("saved tool calls: %+v, %v", run.Usage, err)
	}
}
