package worker

import "testing"

func TestParseActivityKeepsTypeWithoutRawPayload(t *testing.T) {
	a := parseActivity("codex", `{"type":"item.started","item":{"type":"command_execution","command":"cat secret.txt"}}`)
	if a == nil || a.Kind != "codex/item.started" || a.Label != "command_execution" {
		t.Fatalf("activity: %+v", a)
	}
	if parseActivity("claude", "plain response") != nil {
		t.Fatal("plain text misidentified as activity")
	}
}
