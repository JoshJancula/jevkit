package usage

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
)

func TestUsageNumberFormats(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 9_999: "9,999", 23_467: "23.5K", 232_965: "233K", 41_940_915: "41.9M", 2_727_179_297: "2.7B"} {
		if got := usageShort(n); got != want {
			t.Errorf("usageShort(%d) = %q, want %q", n, got, want)
		}
	}
	for ms, want := range map[int64]string{0: "0s", 400: "<1s", 42_000: "42s", 192_000: "3m 12s", 3_840_000: "1h 04m", 93_780_000: "26h 03m"} {
		if got := usageDuration(ms); got != want {
			t.Errorf("usageDuration(%d) = %q, want %q", ms, got, want)
		}
	}
	for v, want := range map[float64]string{0: "$0.00", 4.405301: "$4.41", 0.000986: "$0.00099", 0.0042: "$0.0042"} {
		if got := usageUSD(v); got != want {
			t.Errorf("usageUSD(%v) = %q, want %q", v, got, want)
		}
	}
}

func TestWriteTableAlignedRightAlignsMarkedColumns(t *testing.T) {
	var b strings.Builder
	app.WriteTableAligned(&b, []string{"NAME", "CALLS"}, [][]string{{"a", "5"}, {"bb", "1,234"}}, []bool{false, true})
	if !strings.Contains(b.String(), "│ a    │     5 │") || !strings.Contains(b.String(), "│ bb   │ 1,234 │") {
		t.Fatalf("alignment:\n%s", b.String())
	}
}

func i64(v int64) *int64 { return &v }

func TestAggregateRuntimeCountsCachedInputAndFoldsChildRuns(t *testing.T) {
	runs := []ledger.Run{
		{RunID: "root", CreatedAt: "2026-10-01T00:00:00Z", Task: "fix the thing\nwith details", Usage: []ledger.InvocationUsage{
			// Claude reports cache separately from input; Codex counts it inside.
			{Invocation: "c1", Runtime: "claude", Model: "opus", Role: "planner", InputTokens: i64(10), OutputTokens: i64(5), CacheReadTokens: i64(80), CacheCreationTokens: i64(10), ElapsedMS: i64(60_000)},
			{Invocation: "x1", Runtime: "codex", Model: "gpt", Role: "implementer", InputTokens: i64(100), OutputTokens: i64(5), CacheReadTokens: i64(50)},
		}},
		{RunID: "child", ParentRunID: "root", CreatedAt: "2026-10-01T00:05:00Z", Usage: []ledger.InvocationUsage{
			{Invocation: "x2", Runtime: "codex", Model: "gpt", Role: "implementer", InputTokens: i64(100), OutputTokens: i64(5), ElapsedMS: i64(30_000)},
		}},
		{RunID: "other", CreatedAt: "2026-10-02T00:00:00Z"},
	}
	s := AggregateRuntime(runs)
	if s.Totals.ContextInputTokens != 100+100+100 || s.Totals.InputTokens != 210 {
		t.Fatalf("context input: %+v", s.Totals)
	}
	if got := cacheHit(*s.ByModel["opus"]); got != "80%" {
		t.Errorf("claude cache hit = %s", got)
	}
	if got := cacheHit(*s.ByModel["gpt"]); got != "50%" {
		t.Errorf("codex cache hit = %s (only invocations reporting reads count)", got)
	}
	if len(s.ByRun) != 1 || s.ByRun[0].RunID != "root" || s.ByRun[0].Invocations != 3 || s.ByRun[0].ElapsedMS != 90_000 || s.ByRun[0].UnknownElapsed != 1 {
		t.Fatalf("by run: %+v", s.ByRun)
	}
	if got := taskSummary(s.ByRun[0].Task); got != "fix the thing" {
		t.Errorf("task summary = %q", got)
	}
}

func TestLoggedElapsedUsesLogStartAndLastLine(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("inv.json", `{"invocation":"inv","startedAt":"`+start.Format(time.RFC3339)+`"}`)
	write("inv.lines.jsonl", `{"at":`+itoa(start.Add(time.Second).UnixNano())+`,"stream":"stdout","text":"a"}`+"\n"+
		`{"at":`+itoa(start.Add(95*time.Second).UnixNano())+`,"stream":"stdout","text":"b"}`+"\n")
	if ms, ok := loggedElapsed(dir, "inv"); !ok || ms != 95_000 {
		t.Fatalf("loggedElapsed = %d %v", ms, ok)
	}
	if _, ok := loggedElapsed(dir, "missing"); ok {
		t.Fatal("pruned logs must leave time unreported")
	}
	if _, ok := loggedElapsed(dir, "../inv"); ok {
		t.Fatal("invocation IDs must not escape the log directory")
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
