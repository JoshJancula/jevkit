package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OWNER/jevkit/internal/sdlc/enrollment"
)

const startWorkflowYAML = `version: 1
name: ship-feature
description: Ask whether work is ready.
entry: scope
maxSteps: 5
stages:
  - id: scope
    question:
      prompt: Is the task ready?
      options: {ready: Ready to work, unclear: Needs more detail}
      routes: {ready: plan, unclear: pause}
      fallback: pause
  - id: plan
    work:
      role: planner
      objective: Make a plan.
      routes: {planned: done, answer: done, no-change: done}
  - id: pause
    finish: paused
  - id: done
    finish: succeeded
`

func writeWorkflow(t *testing.T, a *App, name, data string) string {
	t.Helper()
	path := filepath.Join(a.WorkDir, ".jevkit", "sdlc", name+".yaml")
	writeFile(t, path, data)
	return path
}

func setupSDLC(t *testing.T, a *App) {
	t.Helper()
	writeFile(t, a.sdlcRosterPath(), `version: 1
agents:
  - id: host-self
    roles: [planner, implementer, assessor]
    rubric: Test host.
    via: host-self
`)
	a.SdlcReach = func() enrollment.Reach { return enrollment.Reach{Driver: "host", Self: true} }
}

func TestSdlcStartNamedStageWorkflowSkipsSelection(t *testing.T) {
	a, _, fj := cliApp(t)
	setupSDLC(t, a)
	writeWorkflow(t, a, "ship-feature", startWorkflowYAML)
	code, out, errs := run(a, "", "sdlc", "start", "ship-feature", "--task", "add a thing")
	if code != exitOK || !strings.Contains(out, "workflow ship-feature") || fj.calls != 0 {
		t.Fatalf("start: %d %q %q, calls=%d", code, out, errs, fj.calls)
	}
}

func TestSdlcStartRejectsGraphShapeWithoutCreatingRun(t *testing.T) {
	a := newApp(t)
	setupSDLC(t, a)
	writeWorkflow(t, a, "custom", "version: 1\nname: custom\ndescription: example\nnodes: []\n")
	code, _, errs := run(a, "", "sdlc", "start", "custom", "--task", "x")
	if code == exitOK || !strings.Contains(errs, "must define stages") || strings.Contains(strings.ToLower(errs), "legacy") || strings.Contains(strings.ToLower(errs), "migration") {
		t.Fatalf("start: %d %q", code, errs)
	}
	assertNoRuns(t, a)
}

func assertNoRuns(t *testing.T, a *App) {
	t.Helper()
	entries, err := os.ReadDir(a.sdlcRunsDir())
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("unexpected runs: %v", entries)
	}
}
