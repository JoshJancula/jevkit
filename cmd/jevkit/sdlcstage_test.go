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

func stageTestRoster(t *testing.T, a *App) {
	t.Helper()
	fakeSDLCReach(a)
	writeFile(t, a.sdlcRosterPath(), `version: 1
agents:
  - {id: planner, roles: [planner], rubric: Plan., via: runtime, runtime: codex, model: p}
  - {id: implementer, roles: [implementer], rubric: Implement., via: runtime, runtime: cursor, model: i}
  - {id: assessor, roles: [assessor], rubric: Assess., via: runtime, runtime: opencode, model: a}
`)
}

func TestCustomStageWorkflowAsksAndDrivesEnrolledWorkers(t *testing.T) {
	a, _, fj := cliApp(t)
	a.NewJev = func(jev.Config, func() (string, error)) Asker { return fj }
	a.Environ = append(a.Environ, "JEVKIT_TRANSPORT=fixture")
	stageTestRoster(t, a)
	fj.resp = &jev.Response{Answers: map[string]jev.Answer{"route": jev.ChoiceAnswer{Choice: "ready", Confidence: .98}}}
	f := &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "planned", Content: "Plan."}, {Outcome: "changed", Content: "diff --git a/a b/a\n+new\n"}, {Outcome: "approved"}}}
	a.SdlcExecutor = f
	code, _, errs := run(a, "", "sdlc", "init", "custom-review")
	if code != exitOK {
		t.Fatalf("init: %d %s", code, errs)
	}
	code, out, errs := run(a, "", "sdlc", "start", "custom-review", "--task", "add a line", "--auto")
	if code != exitOK {
		t.Fatalf("start: %d %q %q", code, out, errs)
	}
	runID := strings.Fields(out)[1]
	code, _, errs = run(a, "", "sdlc", "next", runID)
	if code == exitOK || !strings.Contains(errs, "use sdlc resume") {
		t.Fatalf("next at question: %d %q", code, errs)
	}
	code, out, errs = run(a, "", "sdlc", "drive", runID, "--until-done")
	if code != exitOK {
		t.Fatalf("drive: %d %q %q", code, out, errs)
	}
	r, err := ledger.Open(a.sdlcRunsDir(), runID).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if r.Adaptive.Stage != adaptive.Done || r.Adaptive.Outcome != "succeeded" || r.StageFlow.Current != "done" || len(r.StageFlow.Transitions) != 4 {
		t.Fatalf("completed run: %+v", r)
	}
	if fj.calls != 1 || !strings.Contains(fj.req.State, "add a line") || len(f.requests) != 3 || f.requests[0].Assignment.StageID != "plan" || !strings.Contains(f.requests[0].Task, "Stage objective:") {
		t.Fatalf("jev calls=%d, request=%+v, worker requests=%+v", fj.calls, fj.req, f.requests)
	}
}

func TestCustomStageWorkflowFallsBackWithoutJev(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	code, _, errs := run(a, "", "sdlc", "init", "custom-review")
	if code != exitOK {
		t.Fatalf("init: %d %s", code, errs)
	}
	code, out, errs := run(a, "", "sdlc", "start", "custom-review", "--task", "unclear request", "--auto")
	if code != exitOK {
		t.Fatalf("start: %d %q %q", code, out, errs)
	}
	runID := strings.Fields(out)[1]
	code, out, errs = run(a, "", "sdlc", "drive", runID, "--until-done")
	if code != exitOK || !strings.Contains(out, "fallback") {
		t.Fatalf("fallback: %d %q %q", code, out, errs)
	}
	r, err := ledger.Open(a.sdlcRunsDir(), runID).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if r.Adaptive.Stage != adaptive.Paused || r.StageFlow.Current != "needs-context" || r.StageFlow.Transitions[0].Answer != "fallback" {
		t.Fatalf("run: %+v", r)
	}
}

func TestCustomStageWorkflowNeedsEnrollmentBeforeRun(t *testing.T) {
	a := newApp(t)
	fakeSDLCReach(a)
	code, _, errs := run(a, "", "sdlc", "init", "custom-review")
	if code != exitOK {
		t.Fatalf("init: %d %s", code, errs)
	}
	code, _, errs = run(a, "", "sdlc", "start", "custom-review", "--task", "add a line", "--auto")
	if code == exitOK || !strings.Contains(errs, "no agents enrolled") {
		t.Fatalf("start: %d %q", code, errs)
	}
	if _, err := os.Stat(a.sdlcRunsDir()); !os.IsNotExist(err) {
		t.Fatalf("a failed preflight created a run directory: %v", err)
	}
}

func TestCustomStageWorkflowLowConfidenceUsesFallback(t *testing.T) {
	a, _, fj := cliApp(t)
	a.NewJev = func(jev.Config, func() (string, error)) Asker { return fj }
	a.Environ = append(a.Environ, "JEVKIT_TRANSPORT=fixture")
	stageTestRoster(t, a)
	fj.resp = &jev.Response{Answers: map[string]jev.Answer{"route": jev.ChoiceAnswer{Choice: "ready", Confidence: .2}}}
	code, _, errs := run(a, "", "sdlc", "init", "custom-review")
	if code != exitOK {
		t.Fatalf("init: %d %s", code, errs)
	}
	code, out, errs := run(a, "", "sdlc", "start", "custom-review", "--task", "unclear", "--auto")
	if code != exitOK {
		t.Fatalf("start: %d %q %q", code, out, errs)
	}
	runID := strings.Fields(out)[1]
	code, out, errs = run(a, "", "sdlc", "drive", runID)
	if code != exitOK || !strings.Contains(out, "fallback (confidence 0.20 below 0.85)") {
		t.Fatalf("drive: %d %q %q", code, out, errs)
	}
	r, err := ledger.Open(a.sdlcRunsDir(), runID).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if r.StageFlow.Current != "needs-context" || r.Adaptive.Stage != adaptive.Paused {
		t.Fatalf("run: %+v", r)
	}
}

func TestCustomStageQuestionRedactsTaskBeforeJev(t *testing.T) {
	a, _, fj := cliApp(t)
	a.NewJev = func(jev.Config, func() (string, error)) Asker { return fj }
	a.Environ = append(a.Environ, "JEVKIT_TRANSPORT=fixture")
	stageTestRoster(t, a)
	writeFile(t, filepath.Join(a.ConfigDir, "redact.yaml"), "version: 1\nrules:\n  - id: fake-secret\n    pattern: 'sk-[A-Za-z0-9]+'\n    replacement: '[REDACTED]'\n")
	fj.resp = &jev.Response{Answers: map[string]jev.Answer{"route": jev.ChoiceAnswer{Choice: "unclear", Confidence: .98}}}
	code, _, errs := run(a, "", "sdlc", "init", "custom-review")
	if code != exitOK {
		t.Fatalf("init: %d %s", code, errs)
	}
	const secret = "sk-liveSECRETvalue123"
	code, out, errs := run(a, "", "sdlc", "start", "custom-review", "--task", "handle key "+secret, "--auto")
	if code != exitOK {
		t.Fatalf("start: %d %q %q", code, out, errs)
	}
	runID := strings.Fields(out)[1]
	code, _, errs = run(a, "", "sdlc", "drive", runID)
	if code != exitOK {
		t.Fatalf("drive: %d %q", code, errs)
	}
	if strings.Contains(fj.req.State, secret) || !strings.Contains(fj.req.State, "[REDACTED]") {
		t.Fatalf("unredacted question state: %q", fj.req.State)
	}
	r, err := ledger.Open(a.sdlcRunsDir(), runID).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Task, secret) {
		t.Fatalf("task was altered in the ledger: %q", r.Task)
	}
}

func TestCustomStageAssessmentUsesQuorumDecision(t *testing.T) {
	a, _, fj := cliApp(t)
	a.NewJev = func(jev.Config, func() (string, error)) Asker { return fj }
	a.Environ = append(a.Environ, "JEVKIT_TRANSPORT=fixture")
	fakeSDLCReach(a)
	writeFile(t, a.sdlcRosterPath(), `version: 1
agents:
  - {id: planner, roles: [planner], rubric: Plan., via: runtime, runtime: codex, model: p}
  - {id: implementer, roles: [implementer], rubric: Implement., via: runtime, runtime: cursor, model: i}
  - {id: assessor-one, roles: [assessor], rubric: Assess., via: runtime, runtime: opencode, model: a}
  - {id: assessor-two, roles: [assessor], rubric: Assess., via: runtime, runtime: codex, model: b}
`)
	fj.resp = &jev.Response{Answers: map[string]jev.Answer{"route": jev.ChoiceAnswer{Choice: "ready", Confidence: .98}}}
	a.SdlcExecutor = &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "planned", Content: "Plan."}, {Outcome: "changed", Content: "diff --git a/a b/a\n+new\n"}, {Outcome: "changes-required"}, {Outcome: "approved"}}}
	code, _, errs := run(a, "", "sdlc", "init", "custom-review")
	if code != exitOK {
		t.Fatalf("init: %d %s", code, errs)
	}
	code, out, errs := run(a, "", "sdlc", "start", "custom-review", "--task", "add a line", "--policy", "collaborative", "--auto")
	if code != exitOK {
		t.Fatalf("start: %d %q %q", code, out, errs)
	}
	runID := strings.Fields(out)[1]
	for range 5 {
		code, out, errs = run(a, "", "sdlc", "drive", runID)
		if code != exitOK {
			t.Fatalf("drive: %d %q %q", code, out, errs)
		}
	}
	r, err := ledger.Open(a.sdlcRunsDir(), runID).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if r.StageFlow.Current != "implement" || r.Adaptive.Stage != adaptive.Implementing || r.StageFlow.Transitions[len(r.StageFlow.Transitions)-1].Answer != "changes-required" {
		t.Fatalf("quorum route: %+v", r)
	}
}
