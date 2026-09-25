package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OWNER/jevkit/internal/sdlc/adaptive"
	"github.com/OWNER/jevkit/internal/sdlc/enrollment"
	"github.com/OWNER/jevkit/internal/sdlc/ledger"
	"github.com/OWNER/jevkit/internal/sdlc/worker"
)

func TestWatchAndLogFollowersDetachWithoutChangingRun(t *testing.T) {
	a := newApp(t)
	st, err := adaptive.New("feature", "lean", 1, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	id := "attached"
	now := time.Now().UTC().Format(time.RFC3339)
	if err := ledger.Open(a.sdlcRunsDir(), id).WriteRun(ledger.Run{RunID: id, Workflow: "feature", CreatedAt: now, UpdatedAt: now, Adaptive: &st, TreeUsage: &ledger.TreeUsage{}}); err != nil {
		t.Fatal(err)
	}
	watchApp := *a
	logApp := *a
	var watchOut, logOut bytes.Buffer
	watchApp.Stdout = &watchOut
	logApp.Stdout = &logOut
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 2)
	go func() { done <- watchApp.sdlcWatch(ctx, id) }()
	go func() { done <- logApp.sdlcLogs(ctx, id, logFilters{raw: true, follow: true}) }()
	dir := filepath.Join(a.sdlcRunsDir(), id, "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "inv.json"), []byte(`{"invocation":"inv","agent":"planner","runtime":"codex"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "inv.stdout"), []byte("active output\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(350 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(watchOut.String(), "planning") || !strings.Contains(logOut.String(), "active output") {
		t.Fatalf("followers: watch=%q logs=%q", watchOut.String(), logOut.String())
	}
	r, err := ledger.Open(a.sdlcRunsDir(), id).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if r.Adaptive.Stage != adaptive.Planning {
		t.Fatalf("followers changed run: %+v", r.Adaptive)
	}
}

func TestSDLCStreamSummaryKeepsCursorResultReadable(t *testing.T) {
	raw := `{"type":"result","result":"I'll inspect the file.\n{\"outcome\":\"planned\",\"content\":\"Replace the comment.\"}"}`
	got := sdlcStreamSummary("stdout", raw)
	if got != "result planned: Replace the comment." {
		t.Fatalf("summary: %q", got)
	}
	if got := sdlcStreamSummary("stdout", `{"type":"tool_call","subtype":"started"}`); got != "" {
		t.Fatalf("tool event duplicated in log tail: %q", got)
	}
	if got := sdlcStreamSummary("stdout", `{"type":"text","part":{"type":"text","text":"Checking tests"}}`); got != "assistant: Checking tests" {
		t.Fatalf("OpenCode text missing from log tail: %q", got)
	}
}

func TestSDLCPauseCauseUsesRecordedInvocationFailure(t *testing.T) {
	a := newApp(t)
	id := "pause-cause"
	store := ledger.Open(a.sdlcRunsDir(), id)
	if err := store.AppendDecision(ledger.Decision{RunID: id, Kind: "invocation-outcome", Outcome: "paused", Detail: "worker: invalid structured reply"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendDecision(ledger.Decision{RunID: id, Kind: "stage-transition", Choice: "paused", Outcome: "planner-failed"}); err != nil {
		t.Fatal(err)
	}
	if got := a.sdlcPauseCause(id, context.DeadlineExceeded); got != "worker: invalid structured reply" {
		t.Fatalf("pause cause: %q", got)
	}
}

func TestSDLCSilentStepAndResume(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	a.SdlcExecutor = &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "planned", Content: "Plan."}, {Outcome: "changed", Content: "diff --git a/a b/a\n+new\n"}, {Outcome: "approved"}}}
	code, out, errs := run(a, "", "sdlc", "run", "feature", "--task", "add a line", "--silent", "--step", "--auto")
	if code != exitOK || errs != "" {
		t.Fatalf("run: %d %q %q", code, out, errs)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || !strings.Contains(lines[1], "completed planning; now implementing") {
		t.Fatalf("silent output: %q", out)
	}
	id := lines[0]
	code, out, errs = run(a, "", "sdlc", "resume", id, "--silent")
	if code != exitOK || errs != "" || strings.Count(strings.TrimSpace(out), "\n") != 0 || !strings.Contains(out, "done (approved)") {
		t.Fatalf("resume: %d %q %q", code, out, errs)
	}
}

func TestSDLCSilentRunHasOnlyIDAndFinalStatus(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	a.SdlcExecutor = &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "planned", Content: "Plan"}, {Outcome: "changed", Content: "diff --git a/a b/a\n+new\n"}, {Outcome: "approved"}}}
	code, out, errs := run(a, "", "sdlc", "run", "feature", "--task", "add behavior", "--silent", "--auto")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if code != exitOK || errs != "" || len(lines) != 2 || !strings.Contains(lines[1], "done (approved)") {
		t.Fatalf("silent run: %d %q %q", code, out, errs)
	}
}

func TestProgressRendererUsesPlainLinesAndColorSetting(t *testing.T) {
	a := newApp(t)
	st, err := adaptive.New("feature", "lean", 1, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	st.MaxAssignments = 20
	r := ledger.Run{RunID: "progress", Workflow: "feature", CreatedAt: time.Now().UTC().Format(time.RFC3339), UpdatedAt: time.Now().UTC().Format(time.RFC3339), Adaptive: &st, TreeUsage: &ledger.TreeUsage{}}
	if err := ledger.Open(a.sdlcRunsDir(), r.RunID).WriteRun(r); err != nil {
		t.Fatal(err)
	}
	var plain bytes.Buffer
	a.sdlcProgress = &sdlcProgress{root: r.RunID, out: &plain, seen: map[string]int{}, last: map[string]string{}}
	a.progressFlush()
	if !strings.Contains(plain.String(), "planning") || strings.Contains(plain.String(), "\x1b[") {
		t.Fatalf("plain progress: %q", plain.String())
	}
	var colored bytes.Buffer
	a.Environ = append(a.Environ, "CLICOLOR_FORCE=1")
	a.sdlcProgress = &sdlcProgress{root: r.RunID, out: &colored, seen: map[string]int{}, last: map[string]string{}}
	a.progressFlush()
	if !strings.Contains(colored.String(), "\x1b[") {
		t.Fatalf("forced color missing: %q", colored.String())
	}
}

func TestAgentsAddPersistsRoleRubrics(t *testing.T) {
	a := newApp(t)
	code, _, errs := run(a, "", "sdlc", "agents", "add", "api-generalist", "--runtime", "codex", "--model", "test-model", "--rubric", "API work", "--role", "planner", "--role", "assessor", "--role-rubric", "planner=Plan endpoints", "--role-rubric", "assessor=Review error cases")
	if code != exitOK {
		t.Fatalf("add: %d %q", code, errs)
	}
	roster, err := enrollment.LoadRoster(a.sdlcRosterPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(roster.Agents) != 1 || roster.Agents[0].RoleRubrics["assessor"] != "Review error cases" {
		t.Fatalf("rubrics: %+v", roster)
	}
}

func TestSDLCLogsRawFiltersAndWatchDetached(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	a.SdlcExecutor = &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "answer", Content: "done"}}}
	code, out, errs := run(a, "", "sdlc", "run", "feature", "--task", "answer", "--auto")
	if code != exitOK {
		t.Fatalf("run: %d %q %q", code, out, errs)
	}
	id := strings.Fields(out)[1]
	dir := filepath.Join(a.sdlcRunsDir(), id, "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "inv.json"), []byte(`{"invocation":"inv","agent":"planner","runtime":"codex"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	secret := "sk-abcdefghijklmnopqrstuvwxyz"
	if err := os.WriteFile(filepath.Join(dir, "inv.stdout"), []byte("hello\n"+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	journal := "{\"at\":1,\"stream\":\"stdout\",\"text\":\"hello\"}\n{\"at\":2,\"stream\":\"stderr\",\"text\":\"warning\"}\n{\"at\":3,\"stream\":\"stdout\",\"text\":\"" + secret + "\"}\n"
	if err := os.WriteFile(filepath.Join(dir, "inv.lines.jsonl"), []byte(journal), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errs = run(a, "", "sdlc", "logs", id, "--raw", "--agent", "planner", "--stream", "stdout")
	if code != exitOK || !strings.Contains(out, "["+id+" planner codex inv] hello") {
		t.Fatalf("logs: %d %q %q", code, out, errs)
	}
	if !strings.Contains(out, secret) {
		t.Fatalf("raw log lost original: %q", out)
	}
	code, interleaved, errs := run(a, "", "sdlc", "logs", id, "--raw")
	if code != exitOK || strings.Index(interleaved, "hello") > strings.Index(interleaved, "warning") || strings.Index(interleaved, "warning") > strings.Index(interleaved, secret) {
		t.Fatalf("interleaved logs: %d %q %q", code, interleaved, errs)
	}
	code, out, errs = run(a, "", "sdlc", "logs", id, "--agent", "planner", "--stream", "stdout")
	if code != exitOK || strings.Contains(out, secret) || !strings.Contains(out, "REDACTED") {
		t.Fatalf("redacted logs: %d %q %q", code, out, errs)
	}
	code, out, errs = run(a, "", "sdlc", "watch", id)
	if code != exitOK || !strings.Contains(out, "done") || strings.Contains(out, "\x1b[") {
		t.Fatalf("watch: %d %q %q", code, out, errs)
	}
}

func TestSDLCDelegationFlagRequiresPolicy(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	code, _, errs := run(a, "", "sdlc", "run", "feature", "--task", "add a line", "--delegate-builtins=true", "--step", "--auto")
	if code == exitOK || !strings.Contains(errs, "does not permit built-in delegation") {
		t.Fatalf("flag: %d %q", code, errs)
	}
}
