package worker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestToolCallCounterFixtures(t *testing.T) {
	for _, tc := range []struct {
		runtime string
		want    *int64
	}{
		{"claude", int64Ptr(2)},
		{"cursor", int64Ptr(1)},
		{"codex", int64Ptr(2)},
		{"opencode", int64Ptr(2)},
		{"antigravity", nil},
	} {
		t.Run(tc.runtime, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", "tools", tc.runtime+".jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			counter := newToolCallCounter(tc.runtime, true)
			// The event can be divided across arbitrary stdout writes.
			cut := 7
			if _, err := counter.Write(raw[:cut]); err != nil {
				t.Fatal(err)
			}
			if _, err := counter.Write(raw[cut:]); err != nil {
				t.Fatal(err)
			}
			assertOptionalInt64(t, "tool calls", counter.result(), tc.want)
		})
	}
}

func TestToolCallCounterUnknownWithoutEvents(t *testing.T) {
	for _, runtime := range []string{"claude", "codex", "opencode"} {
		counter := newToolCallCounter(runtime, false)
		_, _ = counter.Write([]byte(`{"type":"result"}`))
		if got := counter.result(); got != nil {
			t.Fatalf("%s without structured tool events: %d", runtime, *got)
		}
	}
	// An Antigravity assistant stream carrying Claude tool_use blocks can be
	// counted; a result-only stream cannot establish a zero.
	counter := newToolCallCounter("antigravity", true)
	_, _ = counter.Write([]byte("{\"event\":\"assistant\",\"message\":{\"content\":[{\"type\":\"tool_use\",\"id\":\"one\"}]}}"))
	assertOptionalInt64(t, "antigravity tool calls", counter.result(), int64Ptr(1))
	zero := newToolCallCounter("codex", true)
	_, _ = zero.Write([]byte("{\"type\":\"item.completed\",\"item\":{\"id\":\"msg\",\"type\":\"agent_message\"}}"))
	assertOptionalInt64(t, "known zero", zero.result(), int64Ptr(0))
}
