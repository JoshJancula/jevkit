package worker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFailedUsageParsesPerRuntimeCacheFields(t *testing.T) {
	tests := []struct {
		name        string
		runtime     string
		fixture     string
		input       int64
		output      int64
		cacheRead   *int64
		cacheCreate *int64
		provenance  string
		cost        *float64
	}{
		{
			name: "claude result cache", runtime: "claude", fixture: "claude.result.jsonl",
			input: 51, output: 20, cacheRead: int64Ptr(147615), cacheCreate: int64Ptr(12190),
			provenance: ProvenanceClaudeResult, cost: float64Ptr(0.042),
		},
		{
			name: "claude without cache stays nil", runtime: "claude", fixture: "claude.no-cache.jsonl",
			input: 12, output: 4, provenance: ProvenanceClaudeResult,
		},
		{
			name: "codex cached_input_tokens is cache-read only", runtime: "codex", fixture: "codex.turn.completed.jsonl",
			input: 1200, output: 40, cacheRead: int64Ptr(900), provenance: ProvenanceCodexTurnCompleted,
		},
		{
			name: "cursor camelCase cache read/write", runtime: "cursor", fixture: "cursor.result.jsonl",
			input: 1227, output: 13, cacheRead: int64Ptr(10624), cacheCreate: int64Ptr(3136),
			provenance: ProvenanceCursorResult,
		},
		{
			name: "opencode accumulates cache across step_finish", runtime: "opencode", fixture: "opencode.step_finish.jsonl",
			input: 711, output: 10, cacheRead: int64Ptr(21415), cacheCreate: int64Ptr(100),
			provenance: ProvenanceOpenCodeStepFinish, cost: float64Ptr(0.0015),
		},
		{
			name: "antigravity omits cache when absent", runtime: "antigravity", fixture: "antigravity.result.jsonl",
			input: 30, output: 7, provenance: ProvenanceAntigravityResult,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", "usage", tc.fixture))
			if err != nil {
				t.Fatal(err)
			}
			got := failedUsage(tc.runtime, raw)
			if got.InputTokens == nil || *got.InputTokens != tc.input {
				t.Fatalf("input: got %v want %d", got.InputTokens, tc.input)
			}
			if got.OutputTokens == nil || *got.OutputTokens != tc.output {
				t.Fatalf("output: got %v want %d", got.OutputTokens, tc.output)
			}
			assertOptionalInt64(t, "cache-read", got.CacheReadTokens, tc.cacheRead)
			assertOptionalInt64(t, "cache-creation", got.CacheCreationTokens, tc.cacheCreate)
			if got.UsageProvenance != tc.provenance {
				t.Fatalf("provenance: got %q want %q", got.UsageProvenance, tc.provenance)
			}
			if tc.cost != nil {
				if !got.CostReported || got.CostUSD != *tc.cost {
					t.Fatalf("cost: reported=%v usd=%v want %v", got.CostReported, got.CostUSD, *tc.cost)
				}
			}
		})
	}
}

func assertOptionalInt64(t *testing.T, label string, got, want *int64) {
	t.Helper()
	if want == nil {
		if got != nil {
			t.Fatalf("%s: want nil, got %d", label, *got)
		}
		return
	}
	if got == nil || *got != *want {
		t.Fatalf("%s: got %v want %d", label, got, *want)
	}
}

func int64Ptr(v int64) *int64       { return &v }
func float64Ptr(v float64) *float64 { return &v }
