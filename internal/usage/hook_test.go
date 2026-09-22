package usage

import (
	"encoding/json"
	"os"
	"testing"
)

func TestAppendHook(t *testing.T) {
	dir := t.TempDir()
	rec := HookInvocation{Agent: "claude", Event: "pre-tool", Outcome: "ok", Tool: "Bash", DurationMs: 3}
	if err := AppendHook(dir, rec); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(HookPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	var got HookInvocation
	if err := json.Unmarshal(data[:len(data)-1], &got); err != nil {
		t.Fatal(err)
	}
	if got.Timestamp == "" || got.Agent != "claude" || got.Tool != "Bash" || got.DurationMs != 3 {
		t.Fatalf("%+v", got)
	}
	if err := AppendHook("", rec); err != nil {
		t.Fatalf("empty state dir: %v", err)
	}
}
