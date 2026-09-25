package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/OWNER/jevkit/internal/jev"
	"github.com/OWNER/jevkit/internal/sdlc/ledger"
	"github.com/OWNER/jevkit/internal/usage"
)

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
	input := int64(40)
	if err := store.WriteRun(ledger.Run{RunID: id, CreatedAt: "2026-09-25T15:40:40Z", Usage: []ledger.InvocationUsage{
		{Invocation: "one", Agent: "builder", Runtime: "codex", Model: "gpt", Role: "implementer", InputTokens: &input},
		{Invocation: "one", Agent: "builder", Runtime: "codex", Model: "gpt", Role: "implementer", InputTokens: &input},
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
	if report.SchemaVersion != 2 || report.Sources.Jev == nil || report.Sources.Jev.Calls != 1 || report.Sources.Runtime == nil || report.Sources.Runtime.Totals.Invocations != 1 || report.Sources.Runtime.Totals.UnknownOutput != 1 {
		t.Fatalf("report: %+v", report)
	}
	code, output, errors = run(a, "", "usage", "--source", "jev")
	if code != exitOK || !strings.Contains(output, "calls: 1") || strings.Contains(output, "Agent runtime usage") || !strings.Contains(output, "┌") || !strings.Contains(output, "NAME") || strings.Contains(output, "\x1b[") {
		t.Fatalf("source jev: %d %q %s", code, output, errors)
	}
	code, output, errors = run(a, "", "usage")
	if code != exitOK || !strings.Contains(output, "By question set") || !strings.Contains(output, "By runtime") || !strings.Contains(output, "INVOCATIONS") || strings.Contains(output, "\x1b[") {
		t.Fatalf("plain usage tables: %d %q %s", code, output, errors)
	}
	a.Environ = append(a.Environ, "CLICOLOR_FORCE=1")
	code, output, errors = run(a, "", "usage", "--source", "runtime")
	if code != exitOK || !strings.Contains(output, ansiCyan+"Agent runtime usage"+ansiReset) || !strings.Contains(output, ansiCyan+"INPUT UNKNOWN"+ansiReset) {
		t.Fatalf("colored runtime tables: %d %q %s", code, output, errors)
	}
	code, output, errors = run(a, "", "sdlc", "usage", id)
	if code != exitOK || !strings.Contains(output, "Measured total") || !strings.Contains(output, "calls: 1") {
		t.Fatalf("run usage: %d %q %s", code, output, errors)
	}
}
