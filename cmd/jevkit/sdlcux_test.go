package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OWNER/jevkit/internal/sdlc/adaptive"
	"github.com/OWNER/jevkit/internal/sdlc/ledger"
	"github.com/OWNER/jevkit/internal/sdlc/worker"
)

func TestSDLCPublicHelpShowsOnlyUserCommands(t *testing.T) {
	a := newApp(t)
	code, out, errs := run(a, "", "sdlc", "--help")
	if code != exitOK {
		t.Fatalf("help: %d %q %q", code, out, errs)
	}
	for _, command := range []string{"run", "resume", "create"} {
		if !strings.Contains(out, "\n  "+command+" ") {
			t.Fatalf("%s missing from help:\n%s", command, out)
		}
	}
	for _, command := range []string{"start", "drive", "next", "report", "init"} {
		if strings.Contains(out, "\n  "+command+" ") {
			t.Fatalf("%s still shown as a normal command:\n%s", command, out)
		}
	}
}

func TestSDLCCreateWritesWorkflowAndOldInitStillWorks(t *testing.T) {
	a := newApp(t)
	code, out, errs := run(a, "", "sdlc", "create", "custom-review")
	if code != exitOK || !strings.Contains(out, "created stage workflow") {
		t.Fatalf("create: %d %q %q", code, out, errs)
	}
	if _, err := os.Stat(filepath.Join(a.WorkDir, ".jevkit", "sdlc", "custom-review.yaml")); err != nil {
		t.Fatal(err)
	}
	code, _, errs = run(a, "", "sdlc", "init", "old-script")
	if code != exitOK {
		t.Fatalf("compatibility init: %d %q", code, errs)
	}
}

func TestSDLCRunStepAndResumeContinueActiveRun(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	executor := &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "planned", Content: "Plan."}, {Outcome: "changed", Content: "diff --git a/a b/a\n+new\n"}, {Outcome: "approved"}}}
	a.SdlcExecutor = executor
	code, out, errs := run(a, "", "sdlc", "run", "feature", "--task", "add a line", "--step")
	if code != exitOK {
		t.Fatalf("run step: %d %q %q", code, out, errs)
	}
	runID := strings.Fields(out)[1]
	r, err := ledger.Open(a.sdlcRunsDir(), runID).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if r.Adaptive.Stage != adaptive.Implementing || len(executor.requests) != 1 {
		t.Fatalf("after step: %+v, requests=%d", r.Adaptive, len(executor.requests))
	}
	code, _, errs = run(a, "", "sdlc", "resume", runID, "--step")
	if code != exitOK {
		t.Fatalf("resume step: %d %q", code, errs)
	}
	r, err = ledger.Open(a.sdlcRunsDir(), runID).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if r.Adaptive.Stage != adaptive.Assessing || len(executor.requests) != 2 {
		t.Fatalf("after resume step: %+v, requests=%d", r.Adaptive, len(executor.requests))
	}
	code, out, errs = run(a, "", "sdlc", "resume", runID)
	if code != exitOK || !strings.Contains(out, "done (approved)") {
		t.Fatalf("resume: %d %q %q", code, out, errs)
	}
	if len(executor.requests) != 3 {
		t.Fatalf("requests=%d", len(executor.requests))
	}
}

func TestSDLCResumeExplainsPausedRun(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	code, _, errs := run(a, "", "sdlc", "create", "custom-review")
	if code != exitOK {
		t.Fatalf("create: %d %q", code, errs)
	}
	code, out, errs := run(a, "", "sdlc", "start", "custom-review", "--task", "unclear")
	if code != exitOK {
		t.Fatalf("start: %d %q %q", code, out, errs)
	}
	runID := strings.Fields(out)[1]
	code, _, errs = run(a, "", "sdlc", "resume", runID, "--step")
	if code != exitOK {
		t.Fatalf("question step: %d %q", code, errs)
	}
	code, _, errs = run(a, "", "sdlc", "resume", runID)
	if code == exitOK || !strings.Contains(errs, "is paused") {
		t.Fatalf("resume paused: %d %q", code, errs)
	}
}
