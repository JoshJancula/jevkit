package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OWNER/jevkit/internal/sdlc/ledger"
	"github.com/OWNER/jevkit/internal/sdlc/worker"
)

func TestSDLCRunFindsProjectFromTaskFileAndResumeUsesIt(t *testing.T) {
	a := newApp(t)
	parent := a.WorkDir
	project := filepath.Join(parent, "jevkit")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "-q", project)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	project, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	a.WorkDir = project
	stageTestRoster(t, a)
	writeFile(t, filepath.Join(project, "task.md"), "Add a feature.")
	a.WorkDir = parent
	executor := &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "planned", Content: "Plan."}, {Outcome: "changed", Content: "diff --git a/a b/a\n+new\n"}, {Outcome: "approved"}}}
	a.SdlcExecutor = executor
	code, out, errs := run(a, "", "sdlc", "run", "feature", "--task-file", "jevkit/task.md", "--step", "--auto")
	if code != exitOK {
		t.Fatalf("run: %d %q %q", code, out, errs)
	}
	runID := strings.Fields(out)[1]
	saved, err := ledger.Open(a.sdlcRunsDir(), runID).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if !sameProjectPath(t, saved.WorkDir, project) || len(executor.requests) != 1 || !sameProjectPath(t, executor.requests[0].WorkDir, project) || a.WorkDir != parent {
		t.Fatalf("project binding: run=%q requests=%+v current=%q", saved.WorkDir, executor.requests, a.WorkDir)
	}
	code, _, errs = run(a, "", "sdlc", "resume", runID)
	if code != exitOK || len(executor.requests) != 3 || !sameProjectPath(t, executor.requests[1].WorkDir, project) || a.WorkDir != parent {
		t.Fatalf("resume: %d %q requests=%+v current=%q", code, errs, executor.requests, a.WorkDir)
	}
}

func TestSDLCRunFindsProjectFromPlanFile(t *testing.T) {
	a := newApp(t)
	parent := a.WorkDir
	project := filepath.Join(parent, "jevkit")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", project).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	var err error
	project, err = filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	a.WorkDir = project
	stageTestRoster(t, a)
	writeFile(t, filepath.Join(project, "plan.md"), "# Plan\nMake the change.\n")
	a.WorkDir = parent
	executor := &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "changed", Content: "diff --git a/a b/a\n+new\n"}, {Outcome: "approved"}}}
	a.SdlcExecutor = executor
	code, out, errs := run(a, "", "sdlc", "run", "feature", "--plan-file", "jevkit/plan.md", "--auto")
	if code != exitOK {
		t.Fatalf("run: %d %q %q", code, out, errs)
	}
	id := strings.Fields(out)[1]
	saved, err := ledger.Open(a.sdlcRunsDir(), id).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if !sameProjectPath(t, saved.WorkDir, project) || saved.Adaptive.Stage != "done" || len(executor.requests) != 2 || !sameProjectPath(t, executor.requests[0].WorkDir, project) || a.WorkDir != parent {
		t.Fatalf("project plan binding: run=%+v requests=%+v current=%q", saved, executor.requests, a.WorkDir)
	}
}

func sameProjectPath(t *testing.T, first, second string) bool {
	t.Helper()
	a, err := os.Stat(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(second)
	if err != nil {
		t.Fatal(err)
	}
	return os.SameFile(a, b)
}

func TestSDLCRunOutsideRepositoryFailsBeforeCreatingRun(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	code, out, errs := run(a, "", "sdlc", "run", "feature", "--task", "Add a feature", "--auto")
	if code == exitOK || out != "" || !strings.Contains(errs, "require a Git repository") {
		t.Fatalf("run: %d %q %q", code, out, errs)
	}
	assertNoRuns(t, a)
}

func TestSDLCRunRequiresInitialCommitBeforeCreatingRun(t *testing.T) {
	a := newApp(t)
	cmd := exec.Command("git", "init", "-q", a.WorkDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	stageTestRoster(t, a)
	code, out, errs := run(a, "", "sdlc", "run", "feature", "--task", "Add a feature", "--auto")
	if code == exitOK || out != "" || !strings.Contains(errs, "initial Git commit") {
		t.Fatalf("run: %d %q %q", code, out, errs)
	}
	assertNoRuns(t, a)
}
