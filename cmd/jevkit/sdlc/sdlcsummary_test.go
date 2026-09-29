package sdlc

import (
	"bytes"
	"math"
	"strings"
	"testing"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/usage"
)

func TestFinalSummaryReportsPausedRunAndUsage(t *testing.T) {
	a := newApp(t)
	id := "run-20260925T154040Z-d74628dc"
	in, out, toolCalls, cost := int64(120), int64(45), int64(3), 0.0025
	run := ledger.Run{
		RunID: id, Workflow: "feature", Task: "Fix the review loop\x1b[31m", Adaptive: &adaptive.State{
			Stage: adaptive.Paused, Outcome: "assessor-failed", PendingReason: "assessor reply invalid\x1b[31m",
		},
		Usage: []ledger.InvocationUsage{
			{Invocation: "one", Runtime: "cursor", Model: "auto", Role: "planner", InputTokens: &in, OutputTokens: &out, ToolCalls: &toolCalls, CostUSD: &cost},
			{Invocation: "two", Runtime: "claude", Model: "sonnet", Role: "assessor"},
		},
	}
	store := ledger.Open(a.SDLCRunsDir(), id)
	if err := store.WriteRun(run); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendDecision(ledger.Decision{At: "2026-09-25T15:40:40Z", RunID: id, Kind: "invocation-outcome", Stage: adaptive.Planning, Trigger: "cursor-planner", Choice: "planned", Outcome: adaptive.Assessing}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendDecision(ledger.Decision{At: "2026-09-25T15:41:40Z", RunID: id, Kind: "invocation-outcome", Stage: adaptive.Assessing, Trigger: "claude-reviewer", Choice: "failed", Outcome: adaptive.Paused}); err != nil {
		t.Fatal(err)
	}
	if err := usage.Append(a.StateHome(), usage.Record{RunID: id, Transport: usage.TransportHTTPS, UsageSource: usage.SourceMeasured, Model: "jev-test", QuestionSetID: "review", InputTokens: 25, OutputTokens: 6}); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	a.sdlcFinalSummary(&b, id, nil)
	got := b.String()
	for _, want := range []string{
		"Run summary", "State:    PAUSED (assessor-failed)", "Cause:    assessor reply invalid",
		"planner cursor-planner: planned", "assessor claude-reviewer: failed",
		"Token usage by runtime and model:", "cursor", "auto", "claude", "sonnet",
		"INVOCATIONS", "TOOL CALLS", "120", "45", "0 (1 unknown)", "$0.002500", "jev", "jev-test", "25", "6", "~$",
		"Total: 2 invocations, 3 tool calls (1 unknown)",
		"jevkit sdlc resume " + id + " --retry-failed", "jevkit sdlc logs " + id,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\x1b") {
		t.Fatalf("terminal controls leaked into summary: %q", got)
	}
	if !strings.Contains(strings.ReplaceAll(got, " ", ""), "│jev│jev-test│1│—│25│6") {
		t.Fatalf("Jev tool calls should be inapplicable: %s", got)
	}
}

func TestFinalSummaryExplainsActiveAndCompletedRuns(t *testing.T) {
	a := newApp(t)
	id := "run-20260925T154040Z-d74628dc"
	run := ledger.Run{RunID: id, Workflow: "feature", Adaptive: &adaptive.State{Stage: adaptive.Implementing}}
	store := ledger.Open(a.SDLCRunsDir(), id)
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

func TestFinalSummaryPrintsSavedAnswer(t *testing.T) {
	a := newApp(t)
	id := "answer-run"
	store := ledger.Open(a.SDLCRunsDir(), id)
	if err := store.WriteRun(ledger.Run{RunID: id, Workflow: "review", Adaptive: &adaptive.State{Stage: adaptive.Done, Outcome: "answer"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteArtifact("responses/inv.txt", []byte("Verdict: fix the race.\n- Check the lock\n\x1b[31mThen rerun tests\x1b[0m")); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendDecision(ledger.Decision{RunID: id, Kind: "invocation-outcome", Invocation: "inv", Stage: adaptive.Planning, Choice: "answer", Outcome: adaptive.Done}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	a.sdlcFinalSummary(&out, id, nil)
	got := out.String()
	if !strings.Contains(got, "Answer:\n    Verdict: fix the race.\n    - Check the lock\n    Then rerun tests") || strings.Contains(got, "\x1b") {
		t.Fatalf("saved answer missing or terminal controls leaked: %q", got)
	}
}

func TestFinalSummaryUsesColorAndAlignedUsageTable(t *testing.T) {
	a := newApp(t)
	a.Environ = append(a.Environ, "CLICOLOR_FORCE=1")
	id := "run-20260925T154040Z-d74628dc"
	input, output, toolCalls := int64(27713), int64(2431), int64(1200)
	run := ledger.Run{RunID: id, Workflow: "bugfix", Adaptive: &adaptive.State{Stage: adaptive.Done, Outcome: "approved"},
		Usage: []ledger.InvocationUsage{{Invocation: "one", Runtime: "cursor", Role: "planner", InputTokens: &input, OutputTokens: &output, ToolCalls: &toolCalls}}}
	if err := ledger.Open(a.SDLCRunsDir(), id).WriteRun(run); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	a.sdlcFinalSummary(&b, id, nil)
	got := b.String()
	if !strings.Contains(got, app.ANSICyan+"Run summary"+app.ANSIReset) || !strings.Contains(got, app.ANSIGreen+"DONE (approved)"+app.ANSIReset) || !strings.Contains(got, app.ANSICyan+"MODEL"+app.ANSIReset) {
		t.Fatalf("styled summary: %q", got)
	}
	width := 0
	for _, line := range strings.Split(got, "\n") {
		if !strings.HasPrefix(line, "│") {
			continue
		}
		if width == 0 {
			width = app.TextWidth(line)
		} else if app.TextWidth(line) != width {
			t.Fatalf("usage table row width %d != %d: %q", app.TextWidth(line), width, line)
		}
	}
	if width == 0 {
		t.Fatal("no usage table rendered")
	}
	if !strings.Contains(strings.ReplaceAll(got, " ", ""), "│cursor│(unknown)│1│1,200│27,713│2,431") {
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
			if got := app.FormatInt(tc.in); got != tc.want {
				t.Fatalf("formatInt(%d) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestFinalUsageSeparatesModelsWithinRuntime(t *testing.T) {
	a := newApp(t)
	one, two, tools := int64(10), int64(20), int64(4)
	runs := []ledger.Run{{Usage: []ledger.InvocationUsage{
		{Invocation: "a", Runtime: "codex", Model: "small", InputTokens: &one, OutputTokens: &one, ToolCalls: &tools},
		{Invocation: "b", Runtime: "codex", Model: "large", InputTokens: &two, OutputTokens: &two},
		{Invocation: "b", Runtime: "codex", Model: "large", InputTokens: &two, OutputTokens: &two},
	}}}
	var b bytes.Buffer
	a.sdlcUsageTable(&b, runs, nil)
	lines := strings.Split(b.String(), "\n")
	for _, want := range []string{"│codex│large│1│0(1unknown)│20│20", "│codex│small│1│4│10│10"} {
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
	if !strings.Contains(b.String(), "Total: 2 invocations, 4 tool calls (1 unknown)") {
		t.Fatalf("missing runtime totals: %s", b.String())
	}
}
