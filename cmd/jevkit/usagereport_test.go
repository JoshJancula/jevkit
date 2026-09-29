package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/JoshJancula/jevkit/internal/jev"
	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/sdlc/worker"
	"github.com/JoshJancula/jevkit/internal/usage"
)

func TestSaveInvocationUsagePersistsToolCalls(t *testing.T) {
	a := newApp(t)
	id := "run-20260928T120000Z-toolcalls"
	store := ledger.Open(a.sdlcRunsDir(), id)
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

func TestUsageReportSeparatesJevAndRuntimeAndLinksRun(t *testing.T) {
	a := newApp(t)
	id := "run-20260925T154040Z-d74628dc"
	fake := &fakeJev{resp: &jev.Response{Model: "jev-model", UsageReported: true, Usage: jev.Usage{InputTokens: 12, OutputTokens: 3}}}
	a.NewJev = func(jev.Config, func() (string, error)) Asker { return fake }
	client := a.newJev(jev.Config{Model: "jev-model"}, nil)
	if _, err := client.Ask(withUsageRun(context.Background(), id), jev.Request{QuestionSetID: "sdlc.route"}); err != nil {
		t.Fatal(err)
	}
	records, err := usage.ReadRecords(usage.Path(a.stateHome()))
	if err != nil || len(records) != 1 || records[0].RunID != id || records[0].UsageSource != usage.SourceMeasured {
		t.Fatalf("Jev records: %+v %v", records, err)
	}
	store := ledger.Open(a.sdlcRunsDir(), id)
	input, toolCalls := int64(40), int64(2)
	if err := store.WriteRun(ledger.Run{RunID: id, CreatedAt: "2026-09-25T15:40:40Z", Usage: []ledger.InvocationUsage{
		{Invocation: "one", Agent: "builder", Runtime: "codex", Model: "gpt", Role: "implementer", InputTokens: &input, ToolCalls: &toolCalls},
		{Invocation: "one", Agent: "builder", Runtime: "codex", Model: "gpt", Role: "implementer", InputTokens: &input, ToolCalls: &toolCalls},
	}}); err != nil {
		t.Fatal(err)
	}
	code, output, errors := run(a, "", "usage", "--format", "json")
	if code != exitOK {
		t.Fatalf("usage: %d %s", code, errors)
	}
	var report usageReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != 2 || report.Sources.Jev == nil || report.Sources.Jev.Calls != 1 || report.Sources.Runtime == nil || report.Sources.Runtime.Totals.Invocations != 1 || report.Sources.Runtime.Totals.ToolCalls != 2 || report.Sources.Runtime.Totals.UnknownToolCalls != 0 || report.Sources.Runtime.Totals.UnknownOutput != 1 {
		t.Fatalf("report: %+v", report)
	}
	code, output, errors = run(a, "", "usage", "--source", "jev")
	if code != exitOK || !strings.Contains(output, "calls: 1") || strings.Contains(output, "Agent runtime usage") || !strings.Contains(output, "┌") || !strings.Contains(output, "NAME") || strings.Contains(output, "\x1b[") {
		t.Fatalf("source jev: %d %q %s", code, output, errors)
	}
	code, output, errors = run(a, "", "usage")
	if code != exitOK || !strings.Contains(output, "By question set") || !strings.Contains(output, "By runtime") || !strings.Contains(output, "INVOCATIONS") || !strings.Contains(output, "TOOL CALLS") || strings.Contains(output, "\x1b[") {
		t.Fatalf("plain usage tables: %d %q %s", code, output, errors)
	}
	a.Environ = append(a.Environ, "CLICOLOR_FORCE=1")
	code, output, errors = run(a, "", "usage", "--source", "runtime")
	if code != exitOK || !strings.Contains(output, ansiCyan+"Agent runtime usage"+ansiReset) || !strings.Contains(output, ansiCyan+"INPUT UNKNOWN"+ansiReset) {
		t.Fatalf("colored runtime tables: %d %q %s", code, output, errors)
	}
	code, output, errors = run(a, "", "sdlc", "usage", id)
	if code != exitOK || !strings.Contains(output, "Measured total") || !strings.Contains(output, "tool calls 2 (0 unknown)") || !strings.Contains(output, "calls: 1") {
		t.Fatalf("run usage: %d %q %s", code, output, errors)
	}
}

func TestAggregateRuntimeSeparatesCacheWithoutDoubleCounting(t *testing.T) {
	a := newApp(t)
	id := "run-20260928T120000Z-cacheagg01"
	store := ledger.Open(a.sdlcRunsDir(), id)
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
	runs, err := a.allUsageRuns()
	if err != nil {
		t.Fatal(err)
	}
	sum := aggregateRuntime(runs)
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
	if code != exitOK || !strings.Contains(output, "CACHE READ") || !strings.Contains(output, "cache: read") {
		t.Fatalf("runtime usage text: %d %q %s", code, output, errors)
	}
	code, output, errors = run(a, "", "sdlc", "usage", id)
	if code != exitOK || !strings.Contains(output, "cache-read") || !strings.Contains(output, "claude.result") || strings.Contains(output, "billing saving") {
		t.Fatalf("sdlc usage: %d %q %s", code, output, errors)
	}
}
