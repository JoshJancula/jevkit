package sdlc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	usagecmd "github.com/JoshJancula/jevkit/cmd/jevkit/usage"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/cmd/jevkit/internal/testkit"
	"github.com/JoshJancula/jevkit/internal/jev"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/usage"
)

func TestUsageReportSeparatesJevAndRuntimeAndLinksRun(t *testing.T) {
	a := newApp(t)
	id := "run-20260925T154040Z-d74628dc"
	fake := &testkit.FakeJev{Resp: &jev.Response{Model: "jev-model", UsageReported: true, Usage: jev.Usage{InputTokens: 12, OutputTokens: 3}}}
	a.NewJev = func(jev.Config, func() (string, error)) app.Asker { return fake }
	client := a.JevClient(jev.Config{Model: "jev-model"}, nil)
	if _, err := client.Ask(app.WithUsageRun(context.Background(), id), jev.Request{QuestionSetID: "sdlc.route"}); err != nil {
		t.Fatal(err)
	}
	records, err := usage.ReadRecords(usage.Path(a.StateHome()))
	if err != nil || len(records) != 1 || records[0].RunID != id || records[0].UsageSource != usage.SourceMeasured {
		t.Fatalf("Jev records: %+v %v", records, err)
	}
	store := ledger.Open(a.SDLCRunsDir(), id)
	input, toolCalls := int64(40), int64(2)
	if err := store.WriteRun(ledger.Run{RunID: id, CreatedAt: "2026-09-25T15:40:40Z", Usage: []ledger.InvocationUsage{
		{Invocation: "one", Agent: "builder", Runtime: "codex", Model: "gpt", Role: "implementer", InputTokens: &input, ToolCalls: &toolCalls},
		{Invocation: "one", Agent: "builder", Runtime: "codex", Model: "gpt", Role: "implementer", InputTokens: &input, ToolCalls: &toolCalls},
	}}); err != nil {
		t.Fatal(err)
	}
	code, output, errors := run(a, "", "usage", "--format", "json")
	if code != app.ExitOK {
		t.Fatalf("usage: %d %s", code, errors)
	}
	var report usagecmd.UsageReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != 2 || report.Sources.Jev == nil || report.Sources.Jev.Calls != 1 || report.Sources.Runtime == nil || report.Sources.Runtime.Totals.Invocations != 1 || report.Sources.Runtime.Totals.ToolCalls != 2 || report.Sources.Runtime.Totals.UnknownToolCalls != 0 || report.Sources.Runtime.Totals.UnknownOutput != 1 {
		t.Fatalf("report: %+v", report)
	}
	if runs := report.Sources.Runtime.ByRun; len(runs) != 1 || runs[0].RunID != id || runs[0].JevCalls != 1 || report.Sources.Jev.ByPurpose["SDLC"].Calls != 1 {
		t.Fatalf("Jev calls by run and purpose: %+v %+v", runs, report.Sources.Jev.ByPurpose)
	}
	code, output, errors = run(a, "", "usage", "--source", "jev")
	if code != app.ExitOK || !strings.Contains(output, "Jev calls    1 · ~$") || !strings.Contains(output, "Jev used for 1 SDLC\n") || strings.Contains(output, "attempts") || strings.Contains(output, "agent time") || strings.Contains(output, "┌") || strings.Contains(output, "\x1b[") {
		t.Fatalf("source jev: %d %q %s", code, output, errors)
	}
	code, output, errors = run(a, "", "usage")
	if code != app.ExitOK || !strings.Contains(output, "By model") || !strings.Contains(output, "By role") || !strings.Contains(output, "By SDLC run") || !strings.Contains(output, id) || !strings.Contains(output, "JEV CALLS") || strings.Contains(output, "tool calls") || strings.Contains(output, "\x1b[") {
		t.Fatalf("plain usage tables: %d %q %s", code, output, errors)
	}
	a.Environ = append(a.Environ, "CLICOLOR_FORCE=1")
	code, output, errors = run(a, "", "usage", "--source", "runtime")
	if code != app.ExitOK || !strings.Contains(output, app.ANSICyan+"Usage"+app.ANSIReset) || strings.Contains(output, "INPUT UNKNOWN") {
		t.Fatalf("colored runtime tables: %d %q %s", code, output, errors)
	}
	code, output, errors = run(a, "", "sdlc", "usage", id)
	if code != app.ExitOK || !strings.Contains(output, "Measured total") || !strings.Contains(output, "tool calls 2") || !strings.Contains(output, "transport attempts: 1") {
		t.Fatalf("run usage: %d %q %s", code, output, errors)
	}
}

func TestGlobalUsageSeparatesHookCallsFromDispatches(t *testing.T) {
	a := newApp(t)
	if err := usage.Append(a.StateHome(), usage.Record{Origin: "hook", Agent: "claude", QuestionSetID: "compaction.line-relevance", UsageSource: usage.SourceMeasured, InputTokens: 7, Transport: usage.TransportHTTPS}); err != nil {
		t.Fatal(err)
	}
	if err := usage.AppendHook(a.StateHome(), usage.HookInvocation{Agent: "claude", Event: "pre-tool", Outcome: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := usage.AppendHook(a.StateHome(), usage.HookInvocation{Agent: "claude", Event: "post-tool", Outcome: "ok"}); err != nil {
		t.Fatal(err)
	}
	code, output, errors := run(a, "", "usage", "--source", "jev")
	if code != app.ExitOK || !strings.Contains(output, "Jev used for 1 compaction\n") || strings.Contains(output, "hooks") {
		t.Fatalf("usage: %d %q %s", code, output, errors)
	}
}

func TestAggregateRuntimeSeparatesCacheWithoutDoubleCounting(t *testing.T) {
	a := newApp(t)
	id := "run-20260928T120000Z-cacheagg01"
	store := ledger.Open(a.SDLCRunsDir(), id)
	inClaude, outClaude := int64(51), int64(20)
	readClaude, createClaude := int64(147615), int64(12190)
	costClaude := 0.042
	inCodex, outCodex := int64(1200), int64(40)
	readCodex := int64(900) // subset of Codex input; must not be added into input totals
	inCursor, outCursor := int64(1227), int64(13)
	readCursor, createCursor := int64(10624), int64(3136)
	inAgy, outAgy := int64(30), int64(7)
	if err := store.WriteRun(ledger.Run{RunID: id, CreatedAt: "2026-09-28T12:00:00Z", Usage: []ledger.InvocationUsage{
		{Invocation: "claude-1", Agent: "planner", Runtime: "claude", Model: "sonnet", Role: "planner",
			InputTokens: &inClaude, OutputTokens: &outClaude, CacheReadTokens: &readClaude, CacheCreationTokens: &createClaude,
			CostUSD: &costClaude, UsageProvenance: "claude.result"},
		{Invocation: "codex-1", Agent: "builder", Runtime: "codex", Model: "gpt", Role: "implementer",
			InputTokens: &inCodex, OutputTokens: &outCodex, CacheReadTokens: &readCodex, UsageProvenance: "codex.turn.completed"},
		{Invocation: "cursor-1", Agent: "reviewer", Runtime: "cursor", Model: "auto", Role: "assessor",
			InputTokens: &inCursor, OutputTokens: &outCursor, CacheReadTokens: &readCursor, CacheCreationTokens: &createCursor,
			UsageProvenance: "cursor.result"},
		{Invocation: "agy-1", Agent: "scout", Runtime: "antigravity", Model: "agy", Role: "assessor",
			InputTokens: &inAgy, OutputTokens: &outAgy, UsageProvenance: "antigravity.result"},
	}}); err != nil {
		t.Fatal(err)
	}
	runs, err := (&usagecmd.App{App: a.App}).AllUsageRuns()
	if err != nil {
		t.Fatal(err)
	}
	sum := usagecmd.AggregateRuntime(runs)
	wantInput := inClaude + inCodex + inCursor + inAgy
	wantOutput := outClaude + outCodex + outCursor + outAgy
	wantRead := readClaude + readCodex + readCursor
	wantCreate := createClaude + createCursor
	if sum.Totals.Invocations != 4 || sum.Totals.InputTokens != wantInput || sum.Totals.OutputTokens != wantOutput {
		t.Fatalf("input/output totals: %+v", sum.Totals)
	}
	if sum.Totals.ToolCalls != 0 || sum.Totals.UnknownToolCalls != 4 {
		t.Fatalf("old records must leave tool calls unknown: %+v", sum.Totals)
	}
	if sum.Totals.CacheReadTokens != wantRead || sum.Totals.CacheCreationTokens != wantCreate {
		t.Fatalf("cache totals: %+v", sum.Totals)
	}
	// Cache must stay in its own buckets — never folded into input/output.
	if sum.Totals.InputTokens == wantInput+wantRead || sum.Totals.InputTokens == wantInput+wantRead+wantCreate {
		t.Fatal("cache tokens were double-counted into input")
	}
	if sum.Totals.UnknownCacheCreation != 2 { // codex + antigravity omit creation
		t.Fatalf("unknown cache creation: %d", sum.Totals.UnknownCacheCreation)
	}
	if sum.Totals.UnknownCacheRead != 1 { // antigravity omits read
		t.Fatalf("unknown cache read: %d", sum.Totals.UnknownCacheRead)
	}
	if sum.Totals.UnknownCost != 3 || sum.Totals.SuppliedCostUSD == nil || *sum.Totals.SuppliedCostUSD != costClaude {
		t.Fatalf("cost accounting: %+v", sum.Totals)
	}
	code, output, errors := run(a, "", "usage", "--source", "runtime")
	if code != app.ExitOK || !strings.Contains(output, "CACHE HIT") || !strings.Contains(output, "agent time   ") || !strings.Contains(output, "IN includes cached input") {
		t.Fatalf("runtime usage text: %d %q %s", code, output, errors)
	}
	code, output, errors = run(a, "", "sdlc", "usage", id)
	if code != app.ExitOK || !strings.Contains(output, "cache-read") || !strings.Contains(output, "claude.result") || strings.Contains(output, "billing saving") {
		t.Fatalf("sdlc usage: %d %q %s", code, output, errors)
	}
}
