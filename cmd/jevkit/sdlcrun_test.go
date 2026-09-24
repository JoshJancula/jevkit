package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OWNER/jevkit/internal/jev"
	"github.com/OWNER/jevkit/internal/sdlc/adaptive"
	"github.com/OWNER/jevkit/internal/sdlc/ledger"
	"github.com/OWNER/jevkit/internal/sdlc/worker"
)

func TestSDLCRunExecutesBuiltInInOneCommand(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	executor := &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "planned", Content: "Plan."}, {Outcome: "changed", Content: "diff --git a/a b/a\n+new\n"}, {Outcome: "approved"}}}
	a.SdlcExecutor = executor
	code, out, errs := run(a, "", "sdlc", "run", "feature", "--task", "add a line")
	if code != exitOK {
		t.Fatalf("run: %d %q %q", code, out, errs)
	}
	if !strings.Contains(out, "task kind feature") || !strings.Contains(out, "done (approved)") || strings.Contains(out, "Execute: jevkit sdlc drive") {
		t.Fatalf("output: %q", out)
	}
	runID := strings.Fields(out)[1]
	r, err := ledger.Open(a.sdlcRunsDir(), runID).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if r.Adaptive.Stage != adaptive.Done || len(executor.requests) != 3 {
		t.Fatalf("run=%+v requests=%d", r, len(executor.requests))
	}
}

func TestSDLCRunExecutesCustomQuestionsAndWork(t *testing.T) {
	a, _, fj := cliApp(t)
	a.NewJev = func(jev.Config, func() (string, error)) Asker { return fj }
	a.Environ = append(a.Environ, "JEVKIT_TRANSPORT=fixture")
	stageTestRoster(t, a)
	fj.resp = &jev.Response{Answers: map[string]jev.Answer{"route": jev.ChoiceAnswer{Choice: "ready", Confidence: .98}}}
	a.SdlcExecutor = &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "planned", Content: "Plan."}, {Outcome: "changed", Content: "diff --git a/a b/a\n+new\n"}, {Outcome: "approved"}}}
	code, _, errs := run(a, "", "sdlc", "init", "custom-review")
	if code != exitOK {
		t.Fatalf("init: %d %q", code, errs)
	}
	code, out, errs := run(a, "", "sdlc", "run", "custom-review", "--task", "add a line")
	if code != exitOK || !strings.Contains(out, "question scope: ready") || !strings.Contains(out, "done (succeeded)") {
		t.Fatalf("run: %d %q %q", code, out, errs)
	}
	if fj.calls != 1 {
		t.Fatalf("question calls = %d", fj.calls)
	}
}

func TestSDLCRunRejectsNonStageWorkflowBeforeCreatingRun(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	writeWorkflow(t, a, "custom", "version: 1\nname: custom\ndescription: example\nnodes: []\n")
	code, _, errs := run(a, "", "sdlc", "run", "custom", "--task", "do work")
	if code == exitOK || !strings.Contains(errs, "must define stages") {
		t.Fatalf("run: %d %q", code, errs)
	}
	if _, err := os.Stat(a.sdlcRunsDir()); !os.IsNotExist(err) {
		t.Fatalf("run directory created: %v", err)
	}
}

func TestSDLCRunWithoutNameUsesBuiltInEvenWithProjectFile(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	writeFile(t, filepath.Join(a.WorkDir, ".jevkit", "sdlc", "custom.yaml"), customWorkflowYAML)
	a.SdlcExecutor = &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "answer", Content: "No changes needed."}}}
	code, out, errs := run(a, "", "sdlc", "run", "--task", "answer the request")
	if code != exitOK || !strings.Contains(out, "task kind feature") || !strings.Contains(out, "done (answer)") {
		t.Fatalf("run: %d %q %q", code, out, errs)
	}
}

func TestSDLCRunRequiresEnrollmentBeforeCreatingRun(t *testing.T) {
	a := newApp(t)
	fakeSDLCReach(a)
	code, _, errs := run(a, "", "sdlc", "run", "feature", "--task", "add a line")
	if code == exitOK || !strings.Contains(errs, "no agents enrolled") {
		t.Fatalf("run: %d %q", code, errs)
	}
	if _, err := os.Stat(a.sdlcRunsDir()); !os.IsNotExist(err) {
		t.Fatalf("run directory created: %v", err)
	}
}
