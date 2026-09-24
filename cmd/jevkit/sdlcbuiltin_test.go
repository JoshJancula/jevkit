package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OWNER/jevkit/internal/sdlc/enrollment"
)

const customWorkflowYAML = `version: 1
name: custom
description: A project-specific decision workflow.
entry: plan
stages:
  - id: plan
    work:
      role: planner
      objective: Plan the work.
      routes: {planned: done, answer: done, no-change: done}
  - id: done
    finish: succeeded
`

func TestSdlcListShowsBuiltinsAndProjectWorkflows(t *testing.T) {
	a := newApp(t)
	writeWorkflow(t, a, "custom", customWorkflowYAML)
	code, out, errs := run(a, "", "sdlc", "list")
	if code != exitOK {
		t.Fatalf("list: %d %q %q", code, out, errs)
	}
	for _, want := range []string{"TASK KINDS", "feature", "OPTIONAL PROJECT WORKFLOWS", "custom", ".jevkit/sdlc/custom.yaml", "RUN A TASK"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
	if strings.Contains(strings.ToLower(out), "legacy") || strings.Contains(strings.ToLower(out), "migration") {
		t.Fatalf("unwanted wording: %q", out)
	}
}

func TestSdlcListEmptyStillShowsBuiltins(t *testing.T) {
	a := newApp(t)
	code, out, errs := run(a, "", "sdlc", "list")
	if code != exitOK || !strings.Contains(out, "│ feature") || !strings.Contains(out, "Build a new capability") || !strings.Contains(out, "  none") || !strings.Contains(out, "agents add") {
		t.Fatalf("list: %d %q %q", code, out, errs)
	}
}

func TestSdlcConfigYAMLIsNotAWorkflow(t *testing.T) {
	a := newApp(t)
	writeFile(t, filepath.Join(a.WorkDir, ".jevkit", "sdlc", "agents.yaml"), "version: 1\nagents: []\n")
	writeFile(t, filepath.Join(a.WorkDir, ".jevkit", "sdlc", "policy.yaml"), "version: 1\n")
	code, out, errs := run(a, "", "sdlc", "list")
	if code != exitOK || !strings.Contains(out, "  none") || strings.Contains(out, "agents.yaml") {
		t.Fatalf("list: %d %q %q", code, out, errs)
	}
}

func TestSdlcListShowsProjectOverride(t *testing.T) {
	a := newApp(t)
	writeFile(t, filepath.Join(a.WorkDir, ".jevkit", "sdlc", "feature.yaml"), strings.Replace(customWorkflowYAML, "name: custom", "name: feature", 1))
	code, out, errs := run(a, "", "sdlc", "list")
	if code != exitOK || !strings.Contains(out, "project stage workflow") || !strings.Contains(out, "feature") {
		t.Fatalf("list: %d %q %q", code, out, errs)
	}
}

func TestSdlcListRejectsNonStageWorkflow(t *testing.T) {
	a := newApp(t)
	writeFile(t, filepath.Join(a.WorkDir, ".jevkit", "sdlc", "custom.yaml"), "version: 1\nname: custom\ndescription: x\nnodes: []\n")
	code, _, errs := run(a, "", "sdlc", "list")
	if code == exitOK || !strings.Contains(errs, "must define stages") || strings.Contains(strings.ToLower(errs), "legacy") || strings.Contains(strings.ToLower(errs), "migration") {
		t.Fatalf("list: %d %q", code, errs)
	}
}

func TestSdlcListIncludesPreflightAvailability(t *testing.T) {
	a := newApp(t)
	writeFile(t, a.sdlcRosterPath(), `version: 1
agents:
  - {id: host-self, roles: [implementer], rubric: Implement., via: host-self}
`)
	a.SdlcReach = func() enrollment.Reach { return enrollment.Reach{Driver: "host", Self: true} }
	writeWorkflow(t, a, "custom", customWorkflowYAML)
	code, out, errs := run(a, "", "sdlc", "list")
	if code != exitOK || !strings.Contains(out, "│ custom") || !strings.Contains(out, "setup") || !strings.Contains(out, "sdlc doctor --policy lean") {
		t.Fatalf("list: %d %q %q", code, out, errs)
	}
}

func TestSdlcStartResolvesBuiltinByName(t *testing.T) {
	a, _, fj := cliApp(t)
	setupSDLC(t, a)
	code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "add rate limiting")
	if code != exitOK || !strings.Contains(out, "task kind feature") || fj.calls != 0 {
		t.Fatalf("start: %d %q %q", code, out, errs)
	}
}

func TestSdlcStartProjectWorkflowOverridesBuiltinName(t *testing.T) {
	a := newApp(t)
	setupSDLC(t, a)
	writeWorkflow(t, a, "feature", strings.Replace(customWorkflowYAML, "name: custom", "name: feature", 1))
	code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "x")
	if code != exitOK || !strings.Contains(out, "workflow feature") || strings.Contains(out, "task kind feature") {
		t.Fatalf("start: %d %q %q", code, out, errs)
	}
}

func TestSdlcCreateWritesCustomStarter(t *testing.T) {
	a := newApp(t)
	code, out, errs := run(a, "", "sdlc", "create", "custom-review")
	if code != exitOK {
		t.Fatalf("init: %d\nstdout=%q\nstderr=%q", code, out, errs)
	}
	path := filepath.Join(a.WorkDir, ".jevkit", "sdlc", "custom-review.yaml")
	if !strings.Contains(out, path) {
		t.Fatalf("out = %q", out)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
	starter := readFile(t, path)
	if !strings.Contains(starter, "version: 1") || !strings.Contains(starter, "prompt: Is there enough information") || !strings.Contains(starter, "role: planner") || !strings.Contains(starter, "fallback: needs-context") || !strings.Contains(out, "sdlc run custom-review") {
		t.Fatalf("starter or guidance is incorrect: %q", out)
	}
	// The written file must itself be a valid stage workflow.
	code, _, errs = run(a, "", "sdlc", "validate", path)
	if code != exitOK {
		t.Fatalf("validate written file: %d %s", code, errs)
	}
	code, out, errs = run(a, "", "sdlc", "explain", path)
	if code != exitOK || !strings.Contains(out, "scope  QUESTION") || !strings.Contains(out, "ready") || !strings.Contains(out, "needs-context") || !strings.Contains(out, "PLAN") {
		t.Fatalf("explain starter: %d %q %q", code, out, errs)
	}
	fakeSDLCReach(a)
	writeFile(t, a.sdlcRosterPath(), `version: 1
agents:
  - {id: planner, roles: [planner], rubric: Plan., via: runtime, runtime: codex, model: p}
  - {id: implementer, roles: [implementer], rubric: Implement., via: runtime, runtime: cursor, model: i}
  - {id: assessor, roles: [assessor], rubric: Assess., via: runtime, runtime: opencode, model: a}
`)
	code, out, errs = run(a, "", "sdlc", "start", "custom-review", "--task", "prepare a handoff")
	if code != exitOK || !strings.Contains(out, "workflow custom-review") || !strings.Contains(out, "first stage scope") {
		t.Fatalf("start starter: %d %q %q", code, out, errs)
	}
}

func TestSdlcCreateRefusesToOverwrite(t *testing.T) {
	a := newApp(t)
	writeWorkflow(t, a, "custom-review", "placeholder")
	code, _, errs := run(a, "", "sdlc", "create", "custom-review")
	if code == exitOK {
		t.Fatal("expected init to refuse an existing file")
	}
	if !strings.Contains(errs, "already exists") {
		t.Fatalf("stderr = %q", errs)
	}
}

func TestSdlcCreateRejectsBuiltInAndUnsafeName(t *testing.T) {
	a := newApp(t)
	code, _, errs := run(a, "", "sdlc", "create", "feature")
	if code == exitOK {
		t.Fatal("expected an error for a built-in name")
	}
	if !strings.Contains(errs, "built-in task kind") {
		t.Fatalf("stderr = %q", errs)
	}
	code, _, errs = run(a, "", "sdlc", "create", "../outside")
	if code == exitOK || !strings.Contains(errs, "workflow name must") {
		t.Fatalf("unsafe name: %d %q", code, errs)
	}
}

func TestSdlcValidateAndExplainAcceptBuiltinNames(t *testing.T) {
	a := newApp(t)
	for _, cmd := range []string{"validate", "explain"} {
		code, out, errs := run(a, "", "sdlc", cmd, "review")
		if code != exitOK {
			t.Fatalf("%s review: %d\nstdout=%q\nstderr=%q", cmd, code, out, errs)
		}
	}
}

func TestSdlcAgentsSetupUsesAdd(t *testing.T) {
	a := newApp(t)
	code, out, errs := run(a, "", "sdlc", "agents", "--help")
	if code != exitOK || !strings.Contains(out, "\n  add ") || strings.Contains(out, "\n  enroll ") || strings.Contains(out, "\n  init ") {
		t.Fatalf("agents help: %d %q %q", code, out, errs)
	}
	code, _, errs = run(a, "", "sdlc", "agents", "init")
	if code == exitOK || !strings.Contains(errs, "unknown command") {
		t.Fatalf("removed init command: %d %q", code, errs)
	}
	if _, err := os.Stat(a.sdlcRosterPath()); !os.IsNotExist(err) {
		t.Fatalf("removed init should not create a roster: %v", err)
	}
}
