package main

import (
	"bytes"
	"math"
	"strings"
	"testing"

	"github.com/OWNER/jevkit/internal/sdlc/adaptive"
	"github.com/OWNER/jevkit/internal/sdlc/ledger"
	"github.com/OWNER/jevkit/internal/usage"
)

func TestFinalSummaryReportsPausedRunAndUsage(t *testing.T) {
	a := newApp(t)
	id := "run-20260925T154040Z-d74628dc"
	in, out, cost := int64(120), int64(45), 0.0025
	run := ledger.Run{
		RunID: id, Workflow: "feature", Task: "Fix the review loop\x1b[31m", Adaptive: &adaptive.State{
			Stage: adaptive.Paused, Outcome: "assessor-failed", PendingReason: "assessor reply invalid\x1b[31m",
		},
		Usage: []ledger.InvocationUsage{
			{Invocation: "one", Runtime: "cursor", Model: "auto", Role: "planner", InputTokens: &in, OutputTokens: &out, CostUSD: &cost},
			{Invocation: "two", Runtime: "claude", Model: "sonnet", Role: "assessor"},
		},
	}
	store := ledger.Open(a.sdlcRunsDir(), id)
	if err := store.WriteRun(run); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendDecision(ledger.Decision{At: "2026-09-25T15:40:40Z", RunID: id, Kind: "invocation-outcome", Stage: adaptive.Planning, Trigger: "cursor-planner", Choice: "planned", Outcome: adaptive.Assessing}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendDecision(ledger.Decision{At: "2026-09-25T15:41:40Z", RunID: id, Kind: "invocation-outcome", Stage: adaptive.Assessing, Trigger: "claude-reviewer", Choice: "failed", Outcome: adaptive.Paused}); err != nil {
		t.Fatal(err)
	}
	if err := usage.Append(a.stateHome(), usage.Record{RunID: id, Transport: usage.TransportHTTPS, UsageSource: usage.SourceMeasured, Model: "jev-test", QuestionSetID: "review", InputTokens: 25, OutputTokens: 6}); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	a.sdlcFinalSummary(&b, id, nil)
	got := b.String()
	for _, want := range []string{
		"Run summary", "State:    PAUSED (assessor-failed)", "Cause:    assessor reply invalid",
		"planner cursor-planner: planned", "assessor claude-reviewer: failed",
		"Token usage by runtime and model:", "cursor", "auto", "claude", "sonnet",
		"120", "45", "0 (1 unknown)", "$0.002500", "jev", "jev-test", "25", "6", "~$",
		"jevkit sdlc resume " + id + " --retry-failed", "jevkit sdlc logs " + id,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\x1b") {
		t.Fatalf("terminal controls leaked into summary: %q", got)
	}
}

func TestFinalSummaryExplainsActiveAndCompletedRuns(t *testing.T) {
	a := newApp(t)
	id := "run-20260925T154040Z-d74628dc"
	run := ledger.Run{RunID: id, Workflow: "feature", Adaptive: &adaptive.State{Stage: adaptive.Implementing}}
	store := ledger.Open(a.sdlcRunsDir(), id)
	if err := store.WriteRun(run); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendEvent(ledger.Event{At: "2026-09-25T15:40:40Z", RunID: id, Stage: adaptive.Implementing, Agent: "cursor-builder", Outcome: "started"}); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	a.sdlcFinalSummary(&b, id, nil)
	if !strings.Contains(b.String(), "State:    IMPLEMENTING") || !strings.Contains(b.String(), "implementing · cursor-builder: started") || !strings.Contains(b.String(), "jevkit sdlc resume "+id) {
		t.Fatalf("active summary: %s", b.String())
	}
	run.Adaptive.Stage, run.Adaptive.Outcome = adaptive.Done, "approved"
	if err := store.WriteRun(run); err != nil {
		t.Fatal(err)
	}
	b.Reset()
	a.sdlcFinalSummary(&b, id, nil)
	if !strings.Contains(b.String(), "State:    DONE (approved)") || !strings.Contains(b.String(), "Review the saved logs and artifacts") {
		t.Fatalf("completed summary: %s", b.String())
	}
}

func TestFinalSummaryUsesColorAndAlignedUsageTable(t *testing.T) {
	a := newApp(t)
	a.Environ = append(a.Environ, "CLICOLOR_FORCE=1")
	id := "run-20260925T154040Z-d74628dc"
	input, output := int64(27713), int64(2431)
	run := ledger.Run{RunID: id, Workflow: "bugfix", Adaptive: &adaptive.State{Stage: adaptive.Done, Outcome: "approved"},
		Usage: []ledger.InvocationUsage{{Invocation: "one", Runtime: "cursor", Role: "planner", InputTokens: &input, OutputTokens: &output}}}
	if err := ledger.Open(a.sdlcRunsDir(), id).WriteRun(run); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	a.sdlcFinalSummary(&b, id, nil)
	got := b.String()
	if !strings.Contains(got, ansiCyan+"Run summary"+ansiReset) || !strings.Contains(got, ansiGreen+"DONE (approved)"+ansiReset) || !strings.Contains(got, ansiCyan+"MODEL"+ansiReset) {
		t.Fatalf("styled summary: %q", got)
	}
	width := 0
	for _, line := range strings.Split(got, "\n") {
		if !strings.HasPrefix(line, "│") {
			continue
		}
		if width == 0 {
			width = textWidth(line)
		} else if textWidth(line) != width {
			t.Fatalf("usage table row width %d != %d: %q", textWidth(line), width, line)
		}
	}
	if width == 0 {
		t.Fatal("no usage table rendered")
	}
	if !strings.Contains(strings.ReplaceAll(got, " ", ""), "│cursor│(unknown)│1│27,713│2,431") {
		t.Fatalf("token counts were not comma-grouped: %q", got)
	}
}

func TestFormatInt(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   int64
		want string
	}{
		{name: "zero", in: 0, want: "0"},
		{name: "small", in: 999, want: "999"},
		{name: "thousand", in: 1000, want: "1,000"},
		{name: "large", in: 148588, want: "148,588"},
		{name: "maximum", in: math.MaxInt64, want: "9,223,372,036,854,775,807"},
		{name: "negative", in: -1000, want: "-1,000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatInt(tc.in); got != tc.want {
				t.Fatalf("formatInt(%d) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestFinalUsageSeparatesModelsWithinRuntime(t *testing.T) {
	a := newApp(t)
	one, two := int64(10), int64(20)
	runs := []ledger.Run{{Usage: []ledger.InvocationUsage{
		{Invocation: "a", Runtime: "codex", Model: "small", InputTokens: &one, OutputTokens: &one},
		{Invocation: "b", Runtime: "codex", Model: "large", InputTokens: &two, OutputTokens: &two},
		{Invocation: "b", Runtime: "codex", Model: "large", InputTokens: &two, OutputTokens: &two},
	}}}
	var b bytes.Buffer
	a.sdlcUsageTable(&b, runs, nil)
	lines := strings.Split(b.String(), "\n")
	for _, want := range []string{"│codex│large│1│20│20", "│codex│small│1│10│10"} {
		found := false
		for _, line := range lines {
			if strings.Contains(strings.ReplaceAll(line, " ", ""), want) {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing model row %q:\n%s", want, b.String())
		}
	}
}
