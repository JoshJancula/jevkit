package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/OWNER/jevkit/internal/sdlc/adaptive"
	"github.com/OWNER/jevkit/internal/sdlc/ledger"
	"github.com/OWNER/jevkit/internal/sdlc/worker"
)

func TestTTYFrameUsesCarriageReturnsAndKeepsLinesInsideTerminal(t *testing.T) {
	box := ttyBox("AGENT cursor-architect", []string{"Now  Reading a very long path that cannot fit in the pane"}, 42)
	frame := ttyFrame(append([]string{"SDLC run", "Task: test"}, box...), 42, 8)
	if strings.Contains(frame, "\n") && !strings.Contains(frame, "\r\n") {
		t.Fatalf("raw-mode line endings lost carriage returns: %q", frame)
	}
	lines := strings.Split(frame, "\r\n")
	if len(lines) > 7 {
		t.Fatalf("frame exceeds terminal height: %d", len(lines))
	}
	for _, line := range lines {
		if utf8.RuneCountInString(line) > 42 {
			t.Fatalf("frame line exceeds terminal width: %q", line)
		}
	}
	if !strings.Contains(frame, "┌") || !strings.Contains(frame, "│") {
		t.Fatalf("agent pane missing: %q", frame)
	}
}

func TestTTYWrappedCodeDiffAndControlStripping(t *testing.T) {
	text := "    ┃ +" + strings.Repeat("added", 15) + "\x1b]8;;https://example.com\a\x1b[31m"
	rows := ttyWrapPreserve(text, 32)
	if len(rows) < 2 || !strings.HasPrefix(rows[0], "    ┃ +") || !strings.HasPrefix(rows[1], "    ┃ ") {
		t.Fatalf("code wrapping: %#v", rows)
	}
	for _, row := range rows {
		if utf8.RuneCountInString(row) > 32 || strings.Contains(row, "\x1b") {
			t.Fatalf("unsafe row: %q", row)
		}
	}
	// A markdown bullet is not a removal outside a diff.
	if rows := watchDetailRows("Agent text", []string{"- item"}, 40, true); strings.Contains(rows[0].plain, hlDelBg) {
		t.Fatalf("bullet styled as removal: %q", rows[0].plain)
	}
}

func TestWatchDetailRowsHighlightDiffAndSource(t *testing.T) {
	diff := []string{"diff --git a/main.go b/main.go", "--- a/main.go", "+++ b/main.go", "@@ -1,3 +1,3 @@", " package main", `-func old() string { return "x" }`, `+func main() { fmt.Println("hi") } // greet`}
	rows := watchDetailRows("Files changed", diff, 60, true)
	if len(rows) != len(diff) {
		t.Fatalf("rows: %#v", rows)
	}
	want := []watchRowKind{rowDiffFile, rowDiffFile, rowDiffFile, rowDiffHunk, rowDiffContext, rowDiffDel, rowDiffAdd}
	for i, row := range rows {
		if row.kind != want[i] || !strings.HasPrefix(row.plain, ttyPrestyled) {
			t.Fatalf("row %d: %+v", i, row)
		}
		if w := textWidth(strings.TrimPrefix(row.plain, ttyPrestyled)); w > 60 {
			t.Fatalf("row %d overflows: %d", i, w)
		}
	}
	add := rows[6].plain
	for _, code := range []string{hlAddBg, hlAddSign + "+", hlKeyword + "func", hlString + `"hi"`, hlComment + "// greet", hlFunc + "Println"} {
		if !strings.Contains(add, code) {
			t.Fatalf("added row missing %q: %q", code, add)
		}
	}
	if !strings.Contains(rows[5].plain, hlDelBg) || !strings.Contains(rows[5].plain, hlKeyword+"return") {
		t.Fatalf("removed row: %q", rows[5].plain)
	}

	grep := watchDetailRows("Tool result · rg -n usagef cmd", []string{"cmd/jevkit/usage.go:29:\t\treturn usagef(\"bad\")"}, 80, true)
	if !strings.Contains(grep[0].plain, hlPath+"cmd/jevkit/usage.go") || !strings.Contains(grep[0].plain, hlLineNo+"29") || !strings.Contains(grep[0].plain, hlKeyword+"return") {
		t.Fatalf("grep row: %q", grep[0].plain)
	}
	if plain := watchDetailRows("Files changed", diff, 60, false); strings.Contains(plain[6].plain, "\x1b") || plain[6].plain != diff[6] {
		t.Fatalf("color disabled row styled: %q", plain[6].plain)
	}
}

func TestTTYFitKeepsStylesZeroWidth(t *testing.T) {
	s := hlKeyword + "func" + hlFgReset + " main() {}" + ansiReset
	if got := ttyFit(s, 20); got != s {
		t.Fatalf("fitting styled text changed it: %q", got)
	}
	got := ttyFit(s, 6)
	if textWidth(got) != 6 || !strings.HasSuffix(got, "…"+ansiReset) || !strings.HasPrefix(got, hlKeyword+"func") {
		t.Fatalf("clipped styled text: %q", got)
	}
}

func TestWatchEditPreviewShowsClaudeEditAsDiff(t *testing.T) {
	work := t.TempDir()
	raw, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"content": []map[string]any{{
		"type": "tool_use", "name": "Edit",
		"input": map[string]string{"file_path": filepath.Join(work, "a.go"), "old_string": "a\nold\nz", "new_string": "a\nnew\nz"},
	}}}})
	title, lines := watchEditPreview(string(raw), work)
	want := []string{"--- a.go", "+++ a.go", "@@", " a", "-old", "+new", " z"}
	if title != "Edit · a.go" || strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Fatalf("edit preview: %q %#v", title, lines)
	}
}

func TestWatchFileChangesShowsGitDiff(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	work := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-c", "user.email=t@example.com", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = work
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(work, "main.go"), []byte("package main\n\nfunc old() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "init")
	if err := os.WriteFile(filepath.Join(work, "main.go"), []byte("package main\n\nfunc renamed() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "new.md"), []byte("# New\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"type": "item.completed", "item": map[string]any{"type": "file_change", "status": "completed", "changes": []map[string]string{
		{"path": filepath.Join(work, "main.go"), "kind": "update"},
		{"path": filepath.Join(work, "new.md"), "kind": "add"},
	}}})
	_, _, detail := watchFileChanges(string(raw), work)
	joined := strings.Join(detail, "\n")
	for _, want := range []string{"update  main.go", "-func old() {}", "+func renamed() {}", "+# New"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("diff missing %q:\n%s", want, joined)
		}
	}
}

func TestTTYStyleLineHighlightsExactDecisionOutcome(t *testing.T) {
	for _, test := range []struct {
		choice string
		color  string
	}{
		{choice: "failed", color: ansiRed},
		{choice: "pass", color: ansiGreen},
	} {
		line := "    invocation-outcome  " + test.choice + " · assessing — worker: non-implementer changed the workspace"
		styled := ttyStyleLine(line)
		if !strings.Contains(styled, test.color+test.choice+ansiReset) {
			t.Fatalf("%s outcome color missing: %q", test.choice, styled)
		}
	}

	line := "    invocation-outcome  retry failed bindings · assessing"
	if styled := ttyStyleLine(line); styled != "\x1b[2m"+line+ansiReset {
		t.Fatalf("non-exact outcome choice was highlighted: %q", styled)
	}
}

func TestWatchActivityNamesCursorToolInsteadOfMetadata(t *testing.T) {
	saved := worker.LogLine{Stream: "stdout", Text: `{"type":"tool_call","subtype":"started","call_id":"one","tool_call":{"readToolCall":{"args":{"path":"docs/SDLC.md"}},"startedAtMs":"123"}}`}
	label, key, started, completed := watchActivity(saved)
	if label != "Reading docs/SDLC.md" || key != "one" || !started || completed {
		t.Fatalf("activity: %q %q %t %t", label, key, started, completed)
	}
	if label, _, _, _ := watchActivity(worker.LogLine{Stream: "stdout", Text: `{"type":"thinking","subtype":"delta","text":"internal text"}`}); label != "" {
		t.Fatalf("thinking exposed as work status: %q", label)
	}
}

func TestTTYViewShowsCurrentAgentActionWithinRunSummary(t *testing.T) {
	a := newApp(t)
	runID := "run-20260924T230044Z-ac3257e1"
	invocation := "run-20260924T230044Z-ca5e7964"
	dir := filepath.Join(a.sdlcRunsDir(), runID, "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(worker.LogMeta{Invocation: invocation, Agent: "cursor-architect", Runtime: "cursor", StartedAt: "2026-09-24T23:00:44Z"})
	if err := os.WriteFile(filepath.Join(dir, invocation+".json"), meta, 0o600); err != nil {
		t.Fatal(err)
	}
	event, _ := json.Marshal(worker.LogLine{Stream: "stdout", Text: `{"type":"tool_call","subtype":"started","call_id":"one","tool_call":{"readToolCall":{"args":{"path":"docs/SDLC.md"}},"startedAtMs":"123"}}`})
	if err := os.WriteFile(filepath.Join(dir, invocation+".lines.jsonl"), append(event, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	st := adaptive.State{Stage: adaptive.Planning, MaxAssignments: 20, MaxRevisions: 3, Assignments: map[string]adaptive.Assignment{invocation: {InvocationID: invocation, Role: "planner", AgentID: "cursor-architect"}}}
	run := ledger.Run{RunID: runID, Task: "Explain scoring", Adaptive: &st}
	view := a.sdlcTTYView([]ledger.Run{run}, nil, watchTTYState{selected: -1, driving: true}, 80, 24)
	if !strings.Contains(view, "Task  Explain scoring") || !strings.Contains(view, "Now  Reading docs/SDLC.md") || strings.Contains(view, "startedAtMs") {
		t.Fatalf("current work missing or noisy: %q", view)
	}
}

func TestWatchToolResultPreservesCodeBlockAndBoundsExcerpt(t *testing.T) {
	raw := `{"type":"tool_call","subtype":"completed","tool_call":{"readToolCall":{"args":{"path":"main.go"},"result":{"success":{"content":"# Example\n\n` + "```go" + `\nfunc main() {}\n` + "```" + `\n"}}}}}`
	title, lines := watchToolPreview(raw)
	if title != "Tool result · Reading main.go" || len(lines) != 4 || lines[1] != "┃ ```go" || lines[2] != "┃ func main() {}" {
		t.Fatalf("tool preview: %q %#v", title, lines)
	}
	formatted := formatWatchExcerpt(strings.Repeat("line\n", 200), 4)
	if len(formatted) != 5 || formatted[4] != "… more in sdlc logs" {
		t.Fatalf("unbounded excerpt: %#v", formatted)
	}
}

func TestWatchFinalPreviewNormalizesScopedOutcome(t *testing.T) {
	raw := `{"type":"result","result":"{\"outcome\":\"planner: planned\",\"content\":\"## Goal\\nWrite docs\"}"}`
	title, lines := watchFinalPreview(raw)
	if title != "Agent result · planned" || len(lines) != 2 || lines[0] != "## Goal" {
		t.Fatalf("final preview: %q %#v", title, lines)
	}
}

func TestTTYViewSwitchesBetweenAgentAndToolResults(t *testing.T) {
	a := newApp(t)
	runID := "run-20260924T230044Z-ac3257e1"
	invocation := "run-20260924T230044Z-ca5e7964"
	dir := filepath.Join(a.sdlcRunsDir(), runID, "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(worker.LogMeta{Invocation: invocation, Agent: "cursor-architect", Runtime: "cursor", StartedAt: "2026-09-24T23:00:44Z"})
	if err := os.WriteFile(filepath.Join(dir, invocation+".json"), meta, 0o600); err != nil {
		t.Fatal(err)
	}
	tool := `{"type":"tool_call","subtype":"completed","tool_call":{"readToolCall":{"args":{"path":"main.go"},"result":{"success":{"content":"` + "```go" + `\nfunc main() {}\n` + "```" + `"}}}}}`
	line, _ := json.Marshal(worker.LogLine{Stream: "stdout", Text: tool})
	if err := os.WriteFile(filepath.Join(dir, invocation+".lines.jsonl"), append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	result := `{"type":"result","result":"{\"outcome\":\"planned\",\"content\":\"## Plan\\nWrite docs\"}"}`
	if err := os.WriteFile(filepath.Join(dir, invocation+".stdout"), []byte(result+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := ledger.Run{RunID: runID, Task: "Write docs", Adaptive: &adaptive.State{Stage: adaptive.Paused}}
	state := watchTTYState{selected: -1, logs: true}
	view := a.sdlcTTYView([]ledger.Run{run}, nil, state, 100, 28)
	if !strings.Contains(view, "Agent result · planned") || !strings.Contains(view, "## Plan") {
		t.Fatalf("missing agent result: %q", view)
	}
	state.toolView = true
	view = a.sdlcTTYView([]ledger.Run{run}, nil, state, 100, 28)
	if !strings.Contains(view, "Tool result · Reading main.go") || !strings.Contains(view, "┃ func main() {}") {
		t.Fatalf("missing formatted tool result: %q", view)
	}
}

func TestTTYViewKeepsPauseAndControlsVisibleAtNarrowSize(t *testing.T) {
	a := newApp(t)
	runID := "run-20260924T230044Z-ac3257e1"
	invocation := "run-20260924T230044Z-ca5e7964"
	dir := filepath.Join(a.sdlcRunsDir(), runID, "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(worker.LogMeta{Invocation: invocation, Agent: "cursor-architect", Runtime: "cursor"})
	if err := os.WriteFile(filepath.Join(dir, invocation+".json"), meta, 0o600); err != nil {
		t.Fatal(err)
	}
	run := ledger.Run{RunID: runID, Task: strings.Repeat("Improve the contributor workflow ", 8), Adaptive: &adaptive.State{Stage: adaptive.Paused, Outcome: "planner-failed"}}
	decisions := []ledger.Decision{{Kind: "invocation-outcome", Choice: "failed", Outcome: "paused"}, {Kind: "stage-transition", Choice: "paused", Outcome: "planner-failed"}}
	view := a.sdlcTTYView([]ledger.Run{run}, decisions, watchTTYState{selected: -1, logs: true, help: true, paused: true, canRetry: true, cause: "invalid planning outcome"}, 80, 16)
	if !strings.Contains(view, "AGENT 1/1") || !strings.Contains(view, "PAUSED  invalid planning outcome") || !strings.Contains(view, "r auto") || !strings.Contains(view, "q leave") {
		t.Fatalf("lost core run information: %q", view)
	}
	lines := strings.Split(view, "\r\n")
	if len(lines) > 15 {
		t.Fatalf("frame overflows terminal: %d lines", len(lines))
	}
	for _, line := range lines {
		if utf8.RuneCountInString(line) > 80 {
			t.Fatalf("line overflows terminal: %q", line)
		}
	}
}

func TestTTYViewStylesAfterFitting(t *testing.T) {
	a := newApp(t)
	a.Stdout = &bytes.Buffer{}
	a.Environ = append(a.Environ, "CLICOLOR_FORCE=1")
	run := ledger.Run{RunID: "run-20260924T230044Z-ac3257e1", Task: "Update docs", Adaptive: &adaptive.State{Stage: adaptive.Paused, Outcome: "planner-failed"}}
	view := a.sdlcTTYView([]ledger.Run{run}, nil, watchTTYState{paused: true, cause: "invalid planning outcome"}, 60, 16)
	if !strings.Contains(view, ansiYellow+"paused · planner-failed"+ansiReset) || !strings.Contains(view, ansiYellow+"PAUSED  "+ansiReset) {
		t.Fatalf("status colors missing: %q", view)
	}
	for _, line := range strings.Split(view, "\r\n") {
		if textWidth(line) > 60 {
			t.Fatalf("styled line overflows terminal: %q", line)
		}
	}
}

func TestWatchNarrowControlsKeepDetachVisible(t *testing.T) {
	a := newApp(t)
	run := ledger.Run{RunID: "run-20260924T230044Z-ac3257e1", Task: "Update docs", Adaptive: &adaptive.State{Stage: adaptive.Implementing}}
	view := a.sdlcTTYView([]ledger.Run{run}, nil, watchTTYState{}, 80, 16)
	if !strings.Contains(view, "q detach") || !strings.Contains(view, "wheel: history") {
		t.Fatalf("navigation clipped at 80 columns: %q", view)
	}
}

func TestTTYHelpExplainsNavigationAndCanBeHidden(t *testing.T) {
	a := newApp(t)
	run := ledger.Run{RunID: "run-20260924T230044Z-ac3257e1", Task: "Update docs", Adaptive: &adaptive.State{Stage: adaptive.Implementing}}
	state := watchTTYState{help: true, logs: true, driving: true}
	view := a.sdlcTTYView([]ledger.Run{run}, nil, state, 100, 26)
	for _, want := range []string{"HELP  Press ? to hide", "PgUp/PgDn: jump five", "J/K: scroll message text", "n: next agent", "l: show/hide logs"} {
		if !strings.Contains(view, want) {
			t.Fatalf("help missing %q: %s", want, view)
		}
	}
	state.help = false
	if view := a.sdlcTTYView([]ledger.Run{run}, nil, state, 100, 26); strings.Contains(view, "HELP  ") || !strings.Contains(view, "? help") {
		t.Fatalf("help did not collapse to footer: %s", view)
	}
}

func TestTTYHelpKeepsPauseAndRetryVisibleOnSmallTerminal(t *testing.T) {
	a := newApp(t)
	run := ledger.Run{RunID: "run-20260924T230044Z-ac3257e1", Task: "Update docs", Adaptive: &adaptive.State{Stage: adaptive.Paused, Outcome: "assessor-failed"}}
	state := watchTTYState{help: true, paused: true, canRetry: true, cause: "assessor reply invalid"}
	for _, size := range []struct{ width, height int }{{80, 16}, {40, 16}, {32, 16}} {
		view := a.sdlcTTYView([]ledger.Run{run}, []ledger.Decision{{Kind: "invocation-outcome", Choice: "failed"}}, state, size.width, size.height)
		for _, want := range []string{"PAUSED  assessor reply invalid", "HELP  ?", "r auto", "q leave"} {
			if !strings.Contains(view, want) {
				t.Fatalf("%dx%d help lost %q: %s", size.width, size.height, want, view)
			}
		}
		if size.width == 32 && !strings.Contains(view, "c:compact") {
			t.Fatalf("compact retry explanation clipped at 32 columns: %s", view)
		}
		lines := strings.Split(view, "\r\n")
		if len(lines) > size.height-1 {
			t.Fatalf("%dx%d help overflows height: %s", size.width, size.height, view)
		}
		for _, line := range lines {
			if utf8.RuneCountInString(line) > size.width {
				t.Fatalf("%dx%d help overflows width: %q", size.width, size.height, line)
			}
		}
	}
}

func TestTTYViewStartsAtLatestResultAndScrollsBack(t *testing.T) {
	a := newApp(t)
	runID := "run-20260924T230044Z-ac3257e1"
	invocation := "run-20260924T230044Z-ca5e7964"
	dir := filepath.Join(a.sdlcRunsDir(), runID, "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(worker.LogMeta{Invocation: invocation, Agent: "cursor-architect", Runtime: "cursor"})
	if err := os.WriteFile(filepath.Join(dir, invocation+".json"), meta, 0o600); err != nil {
		t.Fatal(err)
	}
	var content []string
	for i := 0; i < 20; i++ {
		content = append(content, fmt.Sprintf("step %02d", i))
	}
	reply, _ := json.Marshal(map[string]string{"outcome": "planned", "content": strings.Join(content, "\n")})
	result, _ := json.Marshal(map[string]string{"type": "result", "result": string(reply)})
	if err := os.WriteFile(filepath.Join(dir, invocation+".stdout"), append(result, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	run := ledger.Run{RunID: runID, Task: "Write docs", Adaptive: &adaptive.State{Stage: adaptive.Paused}}
	state := watchTTYState{selected: -1, logs: true}
	view := a.sdlcTTYView([]ledger.Run{run}, nil, state, 80, 16)
	if !strings.Contains(view, "step 19") || strings.Contains(view, "step 00") {
		t.Fatalf("did not open at the latest result: %q", view)
	}
	state.detailScroll = 20
	view = a.sdlcTTYView([]ledger.Run{run}, nil, state, 80, 16)
	if !strings.Contains(view, "step 00") || strings.Contains(view, "step 19") {
		t.Fatalf("scroll did not move to older result: %q", view)
	}
}

func TestTTYViewNavigatesEarlierCodexToolCalls(t *testing.T) {
	a := newApp(t)
	runID := "run-20260924T230044Z-ac3257e1"
	invocation := "run-20260924T230044Z-ca5e7964"
	dir := filepath.Join(a.sdlcRunsDir(), runID, "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(worker.LogMeta{Invocation: invocation, Agent: "codex-builder-lite", Runtime: "codex"})
	if err := os.WriteFile(filepath.Join(dir, invocation+".json"), meta, 0o600); err != nil {
		t.Fatal(err)
	}
	events := []string{
		`{"type":"item.started","item":{"id":"one","type":"command_execution","command":"cat CONTRIBUTING.md"}}`,
		`{"type":"item.completed","item":{"id":"one","type":"command_execution","command":"cat CONTRIBUTING.md","aggregated_output":"old instructions\nsecond line","exit_code":0}}`,
		`{"type":"item.started","item":{"id":"two","type":"command_execution","command":"git diff --check"}}`,
		`{"type":"item.completed","item":{"id":"two","type":"command_execution","command":"git diff --check","aggregated_output":"new result","exit_code":0}}`,
	}
	var saved []byte
	for _, event := range events {
		line, _ := json.Marshal(worker.LogLine{Stream: "stdout", Text: event})
		saved = append(saved, line...)
		saved = append(saved, '\n')
	}
	if err := os.WriteFile(filepath.Join(dir, invocation+".lines.jsonl"), saved, 0o600); err != nil {
		t.Fatal(err)
	}
	run := ledger.Run{RunID: runID, Task: "Update docs", Adaptive: &adaptive.State{Stage: adaptive.Implementing}}
	state := watchTTYState{selected: -1, logs: true}
	view := a.sdlcTTYView([]ledger.Run{run}, nil, state, 100, 24)
	if !strings.Contains(view, "History  2/2") || !strings.Contains(view, "git diff --check") || !strings.Contains(view, "new result") {
		t.Fatalf("latest Codex activity missing: %q", view)
	}
	state.scroll = 1
	view = a.sdlcTTYView([]ledger.Run{run}, nil, state, 100, 24)
	if !strings.Contains(view, "History  1/2") || !strings.Contains(view, "old instructions") || strings.Contains(view, "new result") {
		t.Fatalf("older Codex result unavailable: %q", view)
	}
}

func TestWatchFileChangesShowsProjectRelativePaths(t *testing.T) {
	work := filepath.Join(t.TempDir(), "project")
	raw, _ := json.Marshal(map[string]any{
		"type": "item.completed",
		"item": map[string]any{"type": "file_change", "changes": []map[string]string{
			{"path": filepath.Join(work, "README.md"), "kind": "update"},
			{"path": filepath.Join(work, "docs", "AGENT-INTEGRATIONS.md"), "kind": "add"},
		}},
	})
	label, title, detail := watchFileChanges(string(raw), work)
	if label != "Editing README.md, docs/AGENT-INTEGRATIONS.md done" || title != "Files changed" || len(detail) != 2 || detail[0] != "update  README.md" || detail[1] != "add  docs/AGENT-INTEGRATIONS.md" {
		t.Fatalf("file changes lost paths: %q %q %#v", label, title, detail)
	}
	if strings.Contains(label, work) || strings.Contains(strings.Join(detail, " "), work) {
		t.Fatal("absolute project path displayed")
	}
}

func TestTTYViewShowsEditedFileInHistory(t *testing.T) {
	a := newApp(t)
	runID := "run-20260924T230044Z-ac3257e1"
	invocation := "run-20260924T230044Z-ca5e7964"
	dir := filepath.Join(a.sdlcRunsDir(), runID, "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(worker.LogMeta{Invocation: invocation, Agent: "codex-builder-lite", Runtime: "codex"})
	if err := os.WriteFile(filepath.Join(dir, invocation+".json"), meta, 0o600); err != nil {
		t.Fatal(err)
	}
	changed, _ := json.Marshal(map[string]any{"type": "item.completed", "item": map[string]any{"id": "edit-1", "type": "file_change", "changes": []map[string]string{{"path": filepath.Join(a.WorkDir, "docs", "AGENT-INTEGRATIONS.md"), "kind": "update"}}}})
	line, _ := json.Marshal(worker.LogLine{Stream: "stdout", Text: string(changed)})
	if err := os.WriteFile(filepath.Join(dir, invocation+".lines.jsonl"), append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	run := ledger.Run{RunID: runID, WorkDir: a.WorkDir, Task: "Update docs", Adaptive: &adaptive.State{Stage: adaptive.Implementing}}
	view := a.sdlcTTYView([]ledger.Run{run}, nil, watchTTYState{selected: -1, logs: true}, 100, 24)
	if !strings.Contains(view, "Editing docs/AGENT-INTEGRATIONS.md done") || !strings.Contains(view, "update  docs/AGENT-INTEGRATIONS.md") || strings.Contains(view, a.WorkDir) {
		t.Fatalf("edited file missing from history: %q", view)
	}
}

func TestWatchInputParserHandlesWheelArrowsAndIgnoresClicks(t *testing.T) {
	var parser watchInputParser
	feed := func(sequence string) []watchInputEvent {
		var events []watchInputEvent
		for i := 0; i < len(sequence); i++ {
			if event, ok := parser.feed(sequence[i]); ok {
				events = append(events, event)
			}
		}
		return events
	}
	for _, tc := range []struct {
		sequence string
		key      byte
		row      int
		mouse    bool
	}{
		{"\x1b[A", 'j', 0, false},
		{"\x1b[B", 'k', 0, false},
		{"\x1b[5~", 'u', 0, false},
		{"\x1b[<64;20;8M", 'j', 8, true},
		{"\x1b[<65;20;9M", 'k', 9, true},
	} {
		events := feed(tc.sequence)
		if len(events) != 1 || events[0].key != tc.key || events[0].row != tc.row || events[0].mouse != tc.mouse {
			t.Fatalf("%q: %#v", tc.sequence, events)
		}
	}
	if events := feed("\x1b[<0;20;8M"); len(events) != 0 {
		t.Fatalf("mouse click treated as navigation: %#v", events)
	}
	if !strings.Contains(watchTTYEnter(true), "\x1b[?1049h") || !strings.Contains(watchTTYEnter(true), "\x1b[?1006h") || !strings.Contains(watchTTYLeave(true), "\x1b[?1049l") {
		t.Fatal("TTY session does not enter and leave alternate screen with mouse tracking")
	}
	frame := "SDLC run\r\n" + strings.Join(ttyBox("AGENT 1/1", []string{"Now  Editing docs"}, 40), "\r\n") + "\r\nUsage  0 in"
	if top, bottom := watchAgentPaneRows(frame); top != 2 || bottom != 4 {
		t.Fatalf("wrong mouse zone: %d-%d", top, bottom)
	}
}

func TestWatchRetryKeysSelectSessionStrategy(t *testing.T) {
	for _, tc := range []struct {
		keys string
		want string
	}{
		{"rR", "auto"}, {"fF", "fresh"}, {"sS", "resume"}, {"cC", "compact"}, {"?q", ""},
	} {
		for i := 0; i < len(tc.keys); i++ {
			if got := watchRetryStrategy(tc.keys[i]); got != tc.want {
				t.Fatalf("retry key %q: got %q, want %q", tc.keys[i], got, tc.want)
			}
		}
	}
}

func TestWatchCountsPartialRuntimeUsage(t *testing.T) {
	a := newApp(t)
	id := "run-20260924T230044Z-ac3257e1"
	input := int64(120)
	output := int64(30)
	run := ledger.Run{RunID: id, Adaptive: &adaptive.State{Stage: adaptive.Done}, Usage: []ledger.InvocationUsage{
		{Invocation: "one", InputTokens: &input},
		{Invocation: "two", OutputTokens: &output},
	}}
	if err := ledger.Open(a.sdlcRunsDir(), id).WriteRun(run); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	a.Stdout = &out
	if err := a.sdlcWatch(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Measured agent tokens: 120 input (1 unknown), 30 output (1 unknown)") {
		t.Fatalf("partial runtime usage was dropped: %s", out.String())
	}
}
