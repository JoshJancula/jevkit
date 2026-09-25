package main

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OWNER/jevkit/internal/sdlc/enrollment"
)

func TestSdlcValidateAndExplainStageWorkflow(t *testing.T) {
	a := newApp(t)
	p := filepath.Join(a.WorkDir, "ship-feature.yaml")
	writeFile(t, p, startWorkflowYAML)
	code, out, errs := run(a, "", "sdlc", "validate", p)
	if code != exitOK || !strings.Contains(out, "4 stages") {
		t.Fatalf("validate: %d %q %q", code, out, errs)
	}
	code, out, errs = run(a, "", "sdlc", "explain", p)
	if code != exitOK || !strings.Contains(out, "scope  QUESTION") || !strings.Contains(out, "ready") || !strings.Contains(out, "PLAN") {
		t.Fatalf("explain: %d %q %q", code, out, errs)
	}
}

func TestSdlcValidateRejectsBadRoute(t *testing.T) {
	a := newApp(t)
	p := filepath.Join(a.WorkDir, "bad.yaml")
	writeFile(t, p, strings.Replace(startWorkflowYAML, "ready: plan", "ready: missing", 1))
	code, _, errs := run(a, "", "sdlc", "validate", p)
	if code == exitOK || !strings.Contains(errs, "not a declared stage") {
		t.Fatalf("validate: %d %q", code, errs)
	}
}

func TestSdlcExplainBuiltInShowsAdaptiveFlow(t *testing.T) {
	a := newApp(t)
	code, out, errs := run(a, "", "sdlc", "explain", "feature")
	if code != exitOK || !strings.Contains(out, "adaptive task kind") || !strings.Contains(out, "quorum approves") || !strings.Contains(out, "No workflow file is needed") {
		t.Fatalf("explain: %d %q %q", code, out, errs)
	}
}

func TestSdlcValidateMissingFile(t *testing.T) {
	a := newApp(t)
	code, _, errs := run(a, "", "sdlc", "validate", filepath.Join(a.WorkDir, "nope.yaml"))
	if code == exitOK {
		t.Fatal("expected failure for a missing file")
	}
	if !strings.Contains(errs, "not a built-in") {
		t.Fatalf("stderr = %q", errs)
	}
}

func TestSdlcAgentsEmpty(t *testing.T) {
	a := newApp(t)
	a.ConfigDir = filepath.Join(a.ConfigDir, "Application Support")
	a.SdlcReach = func() enrollment.Reach {
		return enrollment.Reach{Driver: "cli", Runtimes: map[string]enrollment.RuntimeCapability{"codex": {Write: true}}}
	}
	code, out, errs := run(a, "", "sdlc", "agents")
	if code != exitOK {
		t.Fatalf("agents: %d\nstdout=%q\nstderr=%q", code, out, errs)
	}
	if !strings.Contains(out, "SDLC AGENTS") || !strings.Contains(out, "Only enabled agents in this roster can receive SDLC work") || !strings.Contains(out, "inactive template") || !strings.Contains(out, "Needs 1 planner, 1 implementer, and 1 assessor") || !strings.Contains(out, "Multiple agents can share a role; Jev chooses among them by rubric") || !strings.Contains(out, "CLI apps on PATH: codex") || !strings.Contains(out, "disabled: false") || !strings.Contains(out, "WHEN TO CHOOSE") || !strings.Contains(out, "Plan complex changes") || !strings.Contains(out, "│ AGENT") {
		t.Fatalf("agents output = %q", out)
	}
	if _, err := os.Stat(a.sdlcRosterPath()); err != nil {
		t.Fatalf("roster link points to a missing file: %v", err)
	}
	starter := readFile(t, a.sdlcRosterPath())
	for _, want := range []string{"id: claude-architect", "roles: [planner]", "runtime: claude", "id: codex-builder", "roles: [implementer]", "runtime: codex", "id: opencode-reviewer", "roles: [assessor]", "runtime: opencode", "disabled: true", "model: YOUR_MODEL"} {
		if !strings.Contains(starter, want) {
			t.Fatalf("roster starter missing %q: %s", want, starter)
		}
	}
	roster, err := enrollment.LoadRoster(a.sdlcRosterPath())
	if err != nil || len(roster.Agents) != 4 {
		t.Fatalf("new roster must have four templates: %+v, %v", roster, err)
	}
	for _, ag := range roster.Agents {
		if !ag.Disabled || ag.Ready() {
			t.Fatalf("starter agent must be inactive: %+v", ag)
		}
	}
	var rosterURL string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "  Roster: ") {
			rosterURL = strings.TrimPrefix(line, "  Roster: ")
		}
	}
	parsed, err := url.Parse(rosterURL)
	if err != nil || parsed.Scheme != "file" || parsed.Path != filepath.ToSlash(a.sdlcRosterPath()) || !strings.Contains(rosterURL, "Application%20Support") {
		t.Fatalf("roster URL %q does not point to %q: %v", rosterURL, a.sdlcRosterPath(), err)
	}
}

func TestSdlcAgentsFillsPreviouslyCreatedEmptyRoster(t *testing.T) {
	a := newApp(t)
	writeFile(t, a.sdlcRosterPath(), sdlcEmptyRoster)
	code, _, errs := run(a, "", "sdlc", "agents")
	if code != exitOK || !strings.Contains(readFile(t, a.sdlcRosterPath()), "roles: [assessor]") {
		t.Fatalf("agents: %d %q", code, errs)
	}
}

func TestSdlcAgentsUpdatesOnlyPreviousUneditedStarter(t *testing.T) {
	a := newApp(t)
	writeFile(t, a.sdlcRosterPath(), sdlcPreviousRosterStarter)
	code, _, errs := run(a, "", "sdlc", "agents")
	if code != exitOK || readFile(t, a.sdlcRosterPath()) != sdlcRosterStarter {
		t.Fatalf("agents did not update previous starter: %d %q", code, errs)
	}
}

func TestSdlcAgentsUpdatesOnlyPreviousThreeRoleStarter(t *testing.T) {
	a := newApp(t)
	writeFile(t, a.sdlcRosterPath(), sdlcThreeRoleRosterStarter)
	code, _, errs := run(a, "", "sdlc", "agents")
	if code != exitOK || readFile(t, a.sdlcRosterPath()) != sdlcRosterStarter {
		t.Fatalf("agents did not update previous starter: %d %q", code, errs)
	}
}

func TestSdlcAgentsKeepsEditedRoster(t *testing.T) {
	a := newApp(t)
	data := "version: 1\n# my notes\nagents: []\n"
	writeFile(t, a.sdlcRosterPath(), data)
	code, _, errs := run(a, "", "sdlc", "agents")
	if code != exitOK || readFile(t, a.sdlcRosterPath()) != data {
		t.Fatalf("agents changed an edited roster: %d %q", code, errs)
	}
}

func TestSdlcRosterStarterCanBeFilledOut(t *testing.T) {
	a := newApp(t)
	data := strings.ReplaceAll(sdlcRosterStarter, "disabled: true", "disabled: false")
	data = strings.ReplaceAll(data, "YOUR_MODEL", "test-model")
	writeFile(t, a.sdlcRosterPath(), data)
	roster, err := enrollment.LoadRoster(a.sdlcRosterPath())
	if err != nil || len(roster.Agents) != 4 || !roster.Agents[0].Ready() || !roster.Agents[1].Ready() || !roster.Agents[2].Ready() || !roster.Agents[3].Ready() {
		t.Fatalf("filled starter: %+v, %v", roster, err)
	}
}

func TestSdlcStarterNeedsExplicitEnrollmentAndRealModel(t *testing.T) {
	a := newApp(t)
	a.SdlcReach = func() enrollment.Reach {
		return enrollment.Reach{Driver: "cli", Runtimes: map[string]enrollment.RuntimeCapability{"claude": {Write: true}, "codex": {Write: true}, "opencode": {Write: true}}}
	}
	if code, _, _ := run(a, "", "sdlc", "agents"); code != exitOK {
		t.Fatal("could not create starter roster")
	}
	if code, _, errs := run(a, "", "sdlc", "doctor", "--policy", "lean"); code == exitOK || !strings.Contains(errs, "no active agents") {
		t.Fatalf("inactive templates must not satisfy preflight: %d %q", code, errs)
	}
	writeFile(t, a.sdlcRosterPath(), strings.ReplaceAll(sdlcRosterStarter, "disabled: true", "disabled: false"))
	if code, _, errs := run(a, "", "sdlc", "doctor", "--policy", "lean"); code == exitOK || !strings.Contains(errs, "no active agents") {
		t.Fatalf("placeholder models must not satisfy preflight: %d %q", code, errs)
	}
	writeFile(t, a.sdlcRosterPath(), strings.ReplaceAll(readFile(t, a.sdlcRosterPath()), "YOUR_MODEL", "test-model"))
	if code, out, errs := run(a, "", "sdlc", "doctor", "--policy", "lean"); code != exitOK || !strings.Contains(out, "quorum possible") {
		t.Fatalf("filled starter should satisfy preflight: %d %q %q", code, out, errs)
	}
}

func TestSdlcDoctorShowsDefaultRoleRequirements(t *testing.T) {
	a := newApp(t)
	a.SdlcReach = func() enrollment.Reach {
		return enrollment.Reach{Driver: "cli", Runtimes: map[string]enrollment.RuntimeCapability{"codex": {Write: true}}}
	}
	code, out, errs := run(a, "", "sdlc", "doctor", "--policy", "lean")
	if code == exitOK || !strings.Contains(out, "required: planner 1, implementer 1, assessor 1") || !strings.Contains(out, "planner: 0/1 eligible") || !strings.Contains(out, "implementer: 0/1 eligible") || !strings.Contains(out, "assessor: 0/1 eligible") || !strings.Contains(errs, "--role all") {
		t.Fatalf("doctor: %d %q %q", code, out, errs)
	}
}

func TestSdlcAgentsDiscoverListsNativeAndLedger(t *testing.T) {
	a := newApp(t)
	writeFile(t, filepath.Join(a.WorkDir, ".claude", "agents", "security-reviewer.md"),
		"---\nname: security-reviewer\ndescription: Reviews auth, crypto, input validation and secret handling.\nmodel: opus\n---\n")
	writeFile(t, filepath.Join(a.WorkDir, ".jevkit", "sdlc", "agents.yaml"), `version: 1
agents:
  - id: codex-implementer
    rubric: |
      WHEN: a spec is precise and the change is mechanical across several files.
    via: runtime
    runtime: codex
    model: gpt-5.6-luna
`)
	code, out, errs := run(a, "", "sdlc", "agents", "discover")
	if code != exitOK {
		t.Fatalf("agents: %d\nstdout=%q\nstderr=%q", code, out, errs)
	}
	for _, want := range []string{
		"DISCOVER = LOOK, NOT ADD",
		"NAMED SUGGESTIONS",
		"Project suggestions in .jevkit/sdlc/agents.yaml appear here, not in your roster",
		"codex-implementer  (codex / gpt-5.6-luna; not enrolled)",
		"security-reviewer  (native / security-reviewer; not enrolled)",
		"CLI APPS ON PATH (apps, not agents or model lists)",
		"ADD = PUT AN AGENT ON YOUR TEAM",
		"See your team: jevkit sdlc agents",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("agents output missing %q, got:\n%s", want, out)
		}
	}
}

func TestSdlcAgentsIDCollisionFails(t *testing.T) {
	a := newApp(t)
	writeFile(t, filepath.Join(a.WorkDir, ".claude", "agents", "codex-implementer.md"),
		"---\nname: codex-implementer\ndescription: Native version.\n---\n")
	writeFile(t, filepath.Join(a.WorkDir, ".jevkit", "sdlc", "agents.yaml"), `version: 1
agents:
  - id: codex-implementer
    rubric: r
    via: runtime
    runtime: codex
    model: m
`)
	code, _, errs := run(a, "", "sdlc", "agents", "discover")
	if code == exitOK {
		t.Fatal("expected an id-collision failure")
	}
	if !strings.Contains(errs, "declared by both") {
		t.Fatalf("stderr = %q", errs)
	}
}

func TestSdlcDiscoverRuntimeIsNotAnEnrolledAgent(t *testing.T) {
	a := newApp(t)
	a.SdlcReach = func() enrollment.Reach {
		return enrollment.Reach{Driver: "cli", Runtimes: map[string]enrollment.RuntimeCapability{"codex": {Write: true}}}
	}
	code, out, errs := run(a, "", "sdlc", "agents", "discover")
	if code != exitOK || !strings.Contains(out, "codex") || !strings.Contains(out, "apps, not agents or model lists") || !strings.Contains(out, "--model MODEL") {
		t.Fatalf("discover: %d %q %q", code, out, errs)
	}
	code, out, errs = run(a, "", "sdlc", "agents")
	if code != exitOK || !strings.Contains(out, "inactive template") {
		t.Fatalf("roster: %d %q %q", code, out, errs)
	}
	code, _, errs = run(a, "", "sdlc", "agents", "enroll", "codex", "--rubric", "General work", "--role", "planner")
	if code == exitOK || !strings.Contains(errs, "--model is required") {
		t.Fatalf("missing model: %d %q", code, errs)
	}
	code, out, errs = run(a, "", "sdlc", "agents", "enroll", "codex", "--model", "test-model", "--rubric", "General work", "--role", "planner", "--role", "implementer", "--role", "assessor")
	if code != exitOK || !strings.Contains(out, "added codex") {
		t.Fatalf("enroll runtime: %d %q %q", code, out, errs)
	}
	code, out, errs = run(a, "", "sdlc", "agents")
	if code != exitOK || !strings.Contains(out, "codex") || !strings.Contains(out, "planner, implementer, assessor") {
		t.Fatalf("enrolled roster: %d %q %q", code, out, errs)
	}
	code, out, errs = run(a, "", "sdlc", "doctor", "--policy", "lean")
	if code != exitOK || !strings.Contains(out, "quorum possible") {
		t.Fatalf("doctor: %d %q %q", code, out, errs)
	}
}
