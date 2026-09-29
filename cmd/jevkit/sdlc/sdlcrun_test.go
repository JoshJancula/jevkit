package sdlc

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/cmd/jevkit/internal/testkit"
	"github.com/JoshJancula/jevkit/internal/jev"
	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/sdlc/worker"
	securityconfig "github.com/JoshJancula/jevkit/internal/security/config"
)

func TestSDLCRunExecutesBuiltInInOneCommand(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	executor := &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "planned", Content: "Plan."}, {Outcome: "changed", Content: "diff --git a/a b/a\n+new\n"}, {Outcome: "approved"}}}
	a.SdlcExecutor = executor
	code, out, errs := run(a, "", "sdlc", "run", "feature", "--task", "add a line", "--auto")
	if code != app.ExitOK {
		t.Fatalf("run: %d %q %q", code, out, errs)
	}
	if !strings.Contains(out, "task kind feature") || !strings.Contains(out, "done (approved)") || strings.Contains(out, "Execute: jevkit sdlc drive") {
		t.Fatalf("output: %q", out)
	}
	runID := strings.Fields(out)[1]
	for _, want := range []string{"Run summary", "State:    DONE (approved)", "What happened:", "Token usage by runtime and model:", "RUNTIME", "MODEL", "INVOCATIONS", "TOOL CALLS", "Logs:     jevkit sdlc logs " + runID} {
		if !strings.Contains(out, want) {
			t.Fatalf("final output missing %q: %q", want, out)
		}
	}
	r, err := ledger.Open(a.SDLCRunsDir(), runID).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if r.Adaptive.Stage != adaptive.Done || len(executor.requests) != 3 {
		t.Fatalf("run=%+v requests=%d", r, len(executor.requests))
	}
}

func TestSDLCRunPlanFileBeginsWithImplementation(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	path := filepath.Join(a.WorkDir, "prepared-plan.md")
	testkit.WriteFile(t, path, "# Plan\nImplement the change.\n")
	executor := &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "changed", Content: "diff --git a/a b/a\n+new\n"}, {Outcome: "approved"}}}
	a.SdlcExecutor = executor
	code, out, errs := run(a, "", "sdlc", "run", "feature", "--plan-file", path, "--auto")
	if code != app.ExitOK {
		t.Fatalf("run: %d %q %q", code, out, errs)
	}
	id := strings.Fields(out)[1]
	saved, err := ledger.Open(a.SDLCRunsDir(), id).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := ledger.Open(a.SDLCRunsDir(), id).ReadArtifact("plan.md")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Adaptive.Stage != adaptive.Done || len(executor.requests) != 2 || executor.requests[0].Assignment.Role != "implementer" || string(plan) != "# Plan\nImplement the change.\n" {
		t.Fatalf("plan run: %+v requests=%d plan=%q", saved.Adaptive, len(executor.requests), plan)
	}
}

func TestSDLCRunExecutesCustomQuestionsAndWork(t *testing.T) {
	a, _, fj := cliApp(t)
	a.NewJev = func(jev.Config, func() (string, error)) app.Asker { return fj }
	a.Environ = append(a.Environ, "JEVKIT_TRANSPORT=fixture")
	stageTestRoster(t, a)
	fj.Resp = &jev.Response{Answers: map[string]jev.Answer{"route": jev.ChoiceAnswer{Choice: "ready", Confidence: .98}}}
	a.SdlcExecutor = &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "planned", Content: "Plan."}, {Outcome: "changed", Content: "diff --git a/a b/a\n+new\n"}, {Outcome: "approved"}}}
	code, _, errs := run(a, "", "sdlc", "init", "custom-review")
	if code != app.ExitOK {
		t.Fatalf("init: %d %q", code, errs)
	}
	code, out, errs := run(a, "", "sdlc", "run", "custom-review", "--task", "add a line", "--auto")
	if code != app.ExitOK || !strings.Contains(out, "question scope: ready") || !strings.Contains(out, "done (succeeded)") {
		t.Fatalf("run: %d %q %q", code, out, errs)
	}
	if fj.Calls != 1 {
		t.Fatalf("question calls = %d", fj.Calls)
	}
}

func TestSDLCRunRejectsNonStageWorkflowBeforeCreatingRun(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	writeWorkflow(t, a, "custom", "version: 1\nname: custom\ndescription: example\nnodes: []\n")
	code, _, errs := run(a, "", "sdlc", "run", "custom", "--task", "do work", "--auto")
	if code == app.ExitOK || !strings.Contains(errs, "must define stages") {
		t.Fatalf("run: %d %q", code, errs)
	}
	if _, err := os.Stat(a.SDLCRunsDir()); !os.IsNotExist(err) {
		t.Fatalf("run directory created: %v", err)
	}
}

func TestSDLCRunWithoutNameIncludesProjectWorkflowInSelection(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	testkit.WriteFile(t, filepath.Join(a.WorkDir, ".jevkit", "sdlc", "custom.yaml"), customWorkflowYAML)
	a.SdlcExecutor = &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "answer", Content: "No changes needed."}}}
	code, out, errs := run(a, "", "sdlc", "run", "--task", "answer the request", "--auto")
	if code == app.ExitOK || !strings.Contains(errs, "candidates: bugfix, custom, feature, release, review") || out != "" {
		t.Fatalf("run: %d %q %q", code, out, errs)
	}
}

func TestSDLCRunWithoutNameNamesInvalidProjectWorkflow(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	path := writeWorkflow(t, a, "broken", "version: 1\nname: broken\ndescription: Broken workflow.\nstages: []\n")
	code, _, errs := run(a, "", "sdlc", "run", "--task", "change behavior", "--auto")
	if code == app.ExitOK || !strings.Contains(errs, path) {
		t.Fatalf("invalid project workflow: %d %q", code, errs)
	}
}

func TestSDLCRunRequiresEnrollmentBeforeCreatingRun(t *testing.T) {
	a := newApp(t)
	fakeSDLCReach(a)
	code, _, errs := run(a, "", "sdlc", "run", "feature", "--task", "add a line", "--auto")
	if code == app.ExitOK || !strings.Contains(errs, "no agents enrolled") {
		t.Fatalf("run: %d %q", code, errs)
	}
	if _, err := os.Stat(a.SDLCRunsDir()); !os.IsNotExist(err) {
		t.Fatalf("run directory created: %v", err)
	}
}

func TestSDLCSecurityAllowlistComesFromInputFlags(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	outside := t.TempDir()
	taskPath := filepath.Join(outside, "task.md")
	referencePath := filepath.Join(outside, "reference.md")
	testkit.WriteFile(t, taskPath, "Read "+taskPath+" and implement the feature.\n")
	testkit.WriteFile(t, referencePath, "# Reference\n")
	executor := &fakeSDLCExecutor{replies: []worker.Reply{
		{Outcome: "planned", Content: "Plan."},
		{Outcome: "changed", Content: "diff --git a/a b/a\n+new\n"},
		{Outcome: "approved"},
	}}
	a.SdlcExecutor = executor
	code, _, errs := run(a, "", "sdlc", "run", "feature", "--task-file", taskPath, "--file", referencePath+"=reference.md", "--auto")
	if code != app.ExitOK || len(executor.requests) == 0 {
		t.Fatalf("SDLC run: code=%d requests=%d err=%q", code, len(executor.requests), errs)
	}
	req := executor.requests[0]
	if len(req.AllowRead) != 2 || req.AllowRead[0] != taskPath || req.AllowRead[1] != referencePath {
		t.Fatalf("per-run allowlist = %v", req.AllowRead)
	}
	guard := securityconfig.Guard{Workspace: req.Workspace, AllowRead: req.AllowRead}
	if violation, ok := guard.Check(req.Task); !ok {
		t.Fatalf("task-file path was not allowed: %s", violation)
	}
}

func TestTUIWorkerSilencesOnlyItsOwnOutput(t *testing.T) {
	a := newApp(t)
	var tui bytes.Buffer
	a.Stdout = &tui
	worker := a.sdlcDashboardWorker()
	if worker.App == a.App {
		t.Fatal("worker shares the TUI's App")
	}
	if a.Stdout != &tui || a.SuppressOutput {
		t.Fatalf("worker muted the TUI: stdout replaced=%v suppress=%v", a.Stdout != &tui, a.SuppressOutput)
	}
	a.Outf("still visible\n")
	worker.Outf("hidden\n")
	if got := tui.String(); got != "still visible\n" {
		t.Fatalf("TUI output = %q", got)
	}
}
