package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/OWNER/jevkit/internal/jev"
	"github.com/OWNER/jevkit/internal/sdlc/adaptive"
	"github.com/OWNER/jevkit/internal/sdlc/enrollment"
	"github.com/OWNER/jevkit/internal/sdlc/ledger"
)

func TestCLIReachDoesNotClaimRestrictionsFromBinaryPresence(t *testing.T) {
	a := newApp(t)
	a.LookPath = func(name string) (string, error) {
		if name == "codex" {
			return "/bin/codex", nil
		}
		return "", errors.New("missing")
	}
	r := a.cliReach()
	if r.Driver != "cli" || len(r.Runtimes) != 1 || r.Runtimes["codex"].ReadOnly || r.Runtimes["codex"].Isolated || r.Runtimes["codex"].Scopes {
		t.Fatalf("reach overclaimed: %+v", r)
	}
}

func fakeSDLCReach(a *App) {
	a.SdlcReach = func() enrollment.Reach {
		return enrollment.Reach{Driver: "cli", Runtimes: map[string]enrollment.RuntimeCapability{
			"codex": {Write: true}, "cursor": {Write: true}, "opencode": {Write: true},
		}}
	}
}

func TestSDLCDiscoveryDoesNotAuthorizeStart(t *testing.T) {
	a := newApp(t)
	fakeSDLCReach(a)
	writeFile(t, filepath.Join(a.WorkDir, ".claude", "agents", "reviewer.md"), "---\nname: reviewer\ndescription: Reviews code.\n---\n")
	code, out, errs := run(a, "", "sdlc", "agents", "discover")
	if code != exitOK || !strings.Contains(out, "reviewer  (native / reviewer; not enrolled)") {
		t.Fatalf("discover: %d %q %q", code, out, errs)
	}
	code, _, errs = run(a, "", "sdlc", "start", "feature", "--task", "new feature")
	if code == exitOK || !strings.Contains(errs, "no agents enrolled") {
		t.Fatalf("start without enrollment: %d %q", code, errs)
	}
	assertNoRuns(t, a)
}

func TestSDLCAgentsAddCreatesOwnRuntimeAgentWithoutDiscovery(t *testing.T) {
	a := newApp(t)
	a.ConfigDir = filepath.Join(a.ConfigDir, "Application Support")
	code, out, errs := run(a, "", "sdlc", "agents", "add", "my-reviewer", "--runtime", "codex", "--model", "test-model", "--rubric", "Review code changes", "--role", "assessor")
	if code != exitOK || !strings.Contains(out, "added my-reviewer") || !strings.Contains(out, "Application%20Support/sdlc/roster.yaml") {
		t.Fatalf("add: %d %q %q", code, out, errs)
	}
	roster, err := enrollment.LoadRoster(a.sdlcRosterPath())
	if err != nil || len(roster.Agents) != 1 {
		t.Fatalf("roster: %+v, %v", roster, err)
	}
	ag := roster.Agents[0]
	if ag.ID != "my-reviewer" || ag.Runtime != "codex" || ag.Model != "test-model" || !slices.Equal(ag.Roles, []string{"assessor"}) {
		t.Fatalf("agent: %+v", ag)
	}
}

func TestSDLCAgentsAddAllRolesWithoutDiscovery(t *testing.T) {
	a := newApp(t)
	code, _, errs := run(a, "", "sdlc", "agents", "add", "codex", "--model", "test-model", "--rubric", "General repository work", "--role", "all")
	if code != exitOK {
		t.Fatalf("add: %d %q", code, errs)
	}
	roster, err := enrollment.LoadRoster(a.sdlcRosterPath())
	if err != nil || len(roster.Agents) != 1 || !slices.Equal(roster.Agents[0].Roles, []string{"planner", "implementer", "assessor"}) {
		t.Fatalf("roster: %+v, %v", roster, err)
	}
}

func TestSDLCAgentEnrollWritesOnlyUserRoster(t *testing.T) {
	a := newApp(t)
	fakeSDLCReach(a)
	code, out, errs := run(a, "", "sdlc", "agents", "enroll", "worker", "--via", "runtime", "--runtime", "codex", "--model", "test", "--rubric", "Implements code.", "--role", "implementer")
	if code != exitOK || !strings.Contains(out, "added worker") {
		t.Fatalf("enroll: %d %q %q", code, out, errs)
	}
	r, err := enrollment.LoadRoster(a.sdlcRosterPath())
	if err != nil || len(r.Agents) != 1 || r.Agents[0].ID != "worker" {
		t.Fatalf("roster=%+v %v", r, err)
	}
	if _, err := enrollment.LoadPolicy(a.sdlcPolicyPath()); err != nil {
		t.Fatal(err)
	}
}

func TestSDLCJevSeesOnlyEligibleIDsAndRubrics(t *testing.T) {
	a, _, fj := cliApp(t)
	fakeSDLCReach(a)
	a.NewJev = func(jev.Config, func() (string, error)) Asker { return fj }
	a.Environ = append(a.Environ, "JEVKIT_TRANSPORT=fixture")
	writeFile(t, a.sdlcRosterPath(), `version: 1
agents:
  - {id: codex-planner, roles: [planner], rubric: Plan with codex., via: runtime, runtime: codex, model: a}
  - {id: cursor-planner, roles: [planner], rubric: Plan with cursor., via: runtime, runtime: cursor, model: b}
  - {id: denied-planner, roles: [planner], rubric: Must stay private., via: runtime, runtime: opencode, model: c}
  - {id: implementer, roles: [implementer], rubric: Implement., via: runtime, runtime: cursor, model: i}
  - {id: assessor, roles: [assessor], rubric: Assess., via: runtime, runtime: cursor, model: r}
`)
	writeFile(t, a.sdlcPolicyPath(), `version: 1
minimumProfile: lean
maxConcurrent: 2
maxAssignments: 20
maxRevisions: 3
roles:
  planner: {via: [runtime], runtimes: [codex, cursor], write: true}
  implementer: {via: [runtime], write: true}
  assessor: {via: [runtime], write: true}
`)
	fj.resp = &jev.Response{Answers: map[string]jev.Answer{"agent": jev.ChoiceAnswer{Choice: "cursor-planner", Confidence: 0.95}}}
	code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "build it")
	if code != exitOK {
		t.Fatalf("start: %d %q %q", code, out, errs)
	}
	runID := strings.Fields(out)[1]
	code, out, errs = run(a, "", "sdlc", "next", runID)
	if code != exitOK {
		t.Fatalf("next: %d %q %q", code, out, errs)
	}
	if fj.calls != 1 {
		t.Fatalf("Jev calls=%d", fj.calls)
	}
	for _, question := range fj.req.Questions {
		choice, ok := question.(jev.ChoiceQuestion)
		if !ok {
			continue
		}
		if len(choice.Criteria) != 2 {
			t.Fatalf("Jev criteria=%v", choice.Criteria)
		}
		if _, ok := choice.Criteria["denied-planner"]; ok {
			t.Fatal("denied agent reached Jev")
		}
		if _, ok := choice.Criteria["codex-planner"]; !ok {
			t.Fatal("eligible agent missing")
		}
	}
}

func TestSDLCUnnamedAdaptiveStartUsesTaskKindDecision(t *testing.T) {
	a, _, fj := cliApp(t)
	fakeSDLCReach(a)
	a.NewJev = func(jev.Config, func() (string, error)) Asker { return fj }
	a.Environ = append(a.Environ, "JEVKIT_TRANSPORT=fixture")
	writeFile(t, a.sdlcRosterPath(), `version: 1
agents:
  - {id: planner, roles: [planner], rubric: Plan., via: runtime, runtime: codex, model: p}
  - {id: implementer, roles: [implementer], rubric: Implement., via: runtime, runtime: cursor, model: i}
  - {id: assessor, roles: [assessor], rubric: Assess., via: runtime, runtime: opencode, model: a}
`)
	fj.resp = &jev.Response{Answers: map[string]jev.Answer{"workflow": jev.ChoiceAnswer{Choice: "bugfix", Confidence: 0.95}}}
	code, out, errs := run(a, "", "sdlc", "start", "--task", "login breaks when cookie expires")
	if code != exitOK || !strings.Contains(out, "task kind bugfix") || fj.calls != 1 {
		t.Fatalf("selection: %d %q %q calls=%d", code, out, errs, fj.calls)
	}
}

func TestSDLCAssuredPreflightNeedsThreeIndependentAssessors(t *testing.T) {
	a := newApp(t)
	fakeSDLCReach(a)
	writeFile(t, a.sdlcRosterPath(), `version: 1
agents:
  - {id: planner, roles: [planner], rubric: Plan., via: runtime, runtime: codex, model: p}
  - {id: implementer, roles: [implementer], rubric: Implement., via: runtime, runtime: cursor, model: i}
  - {id: assessor-a, roles: [assessor], rubric: Assess., via: runtime, runtime: opencode, model: a}
  - {id: assessor-alias, roles: [assessor], rubric: Assess., via: runtime, runtime: opencode, model: a}
`)
	code, _, errs := run(a, "", "sdlc", "start", "feature", "--task", "build it", "--policy", "assured")
	if code == exitOK || !strings.Contains(errs, "assessor: need 3 eligible independent agent(s), have 1") {
		t.Fatalf("assured preflight: %d %q", code, errs)
	}
	assertNoRuns(t, a)
	code, out, errs := run(a, "", "sdlc", "doctor", "--policy", "assured")
	if code == exitOK || !strings.Contains(out, "assessor:") || !strings.Contains(errs, "need 3") {
		t.Fatalf("doctor: %d %q %q", code, out, errs)
	}
}

func TestSDLCCLIDriverFiltersNativeAndHostSelf(t *testing.T) {
	a := newApp(t)
	fakeSDLCReach(a)
	writeFile(t, a.sdlcRosterPath(), `version: 1
agents:
  - {id: self, roles: [planner, implementer, assessor], rubric: Self., via: host-self}
  - {id: native, roles: [planner, implementer, assessor], rubric: Native., via: native, subagent: native}
`)
	code, _, errs := run(a, "", "sdlc", "start", "feature", "--task", "build it")
	if code == exitOK || !strings.Contains(errs, "planner: need 1 eligible") {
		t.Fatalf("CLI incorrectly accepted host agents: %d %q", code, errs)
	}
	assertNoRuns(t, a)
}

func TestSDLCHostDriverUsesOnlyExposedEnrolledNative(t *testing.T) {
	a := newApp(t)
	a.SdlcReach = func() enrollment.Reach {
		return enrollment.Reach{Driver: "host", Self: true, Native: map[string]enrollment.HostCapability{
			"writer": {Write: true}, "reviewer": {Write: true},
		}}
	}
	writeFile(t, a.sdlcRosterPath(), `version: 1
agents:
  - {id: self, roles: [planner], rubric: Plan., via: host-self}
  - {id: writer, roles: [implementer], rubric: Implement., via: native, subagent: writer}
  - {id: reviewer, roles: [assessor], rubric: Review., via: native, subagent: reviewer}
`)
	code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "build it")
	if code != exitOK {
		t.Fatalf("host start: %d %q %q", code, out, errs)
	}
	runID := strings.Fields(out)[1]
	code, out, errs = run(a, "", "sdlc", "next", runID)
	if code != exitOK {
		t.Fatalf("host next: %d %q %q", code, out, errs)
	}
	var assignment adaptive.Assignment
	if err := json.Unmarshal([]byte(out), &assignment); err != nil {
		t.Fatal(err)
	}
	if assignment.AgentID != "self" {
		t.Fatalf("host-self not routed: %+v", assignment)
	}
}

func TestSDLCAuthFailureReroutesThenPausesWithoutReplacement(t *testing.T) {
	a := newApp(t)
	fakeSDLCReach(a)
	writeFile(t, a.sdlcRosterPath(), `version: 1
agents:
  - {id: a-planner, roles: [planner], rubric: Plan A., via: runtime, runtime: codex, model: a}
  - {id: b-planner, roles: [planner], rubric: Plan B., via: runtime, runtime: cursor, model: b}
  - {id: implementer, roles: [implementer], rubric: Implement., via: runtime, runtime: cursor, model: i}
  - {id: assessor, roles: [assessor], rubric: Assess., via: runtime, runtime: opencode, model: r}
`)
	code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "build it")
	if code != exitOK {
		t.Fatalf("start: %d %q %q", code, out, errs)
	}
	runID := strings.Fields(out)[1]
	code, out, errs = run(a, "", "sdlc", "next", runID)
	if code != exitOK {
		t.Fatalf("next: %d %q %q", code, out, errs)
	}
	var first adaptive.Assignment
	if err := json.Unmarshal([]byte(out), &first); err != nil {
		t.Fatal(err)
	}
	if first.AgentID != "a-planner" {
		t.Fatalf("fallback chose %s", first.AgentID)
	}
	code, _, errs = run(a, "", "sdlc", "report", runID, "--invocation", first.InvocationID, "--agent", first.AgentID, "--outcome", "auth-failed")
	if code != exitOK {
		t.Fatalf("report: %d %q", code, errs)
	}
	code, out, errs = run(a, "", "sdlc", "next", runID)
	if code != exitOK {
		t.Fatalf("reroute: %d %q %q", code, out, errs)
	}
	var second adaptive.Assignment
	if err := json.Unmarshal([]byte(out), &second); err != nil {
		t.Fatal(err)
	}
	if second.AgentID != "b-planner" {
		t.Fatalf("rerouted to %s", second.AgentID)
	}
	code, _, errs = run(a, "", "sdlc", "report", runID, "--invocation", second.InvocationID, "--agent", second.AgentID, "--outcome", "auth-failed")
	if code != exitOK {
		t.Fatalf("second report: %d %q", code, errs)
	}
	run, err := ledger.Open(a.sdlcRunsDir(), runID).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if run.Adaptive.Stage != adaptive.Paused || !strings.Contains(run.Adaptive.Outcome, "no-eligible") {
		t.Fatalf("run did not pause: %+v", run.Adaptive)
	}
}
