package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OWNER/jevkit/internal/jev"
	"github.com/OWNER/jevkit/internal/sdlc/adaptive"
	"github.com/OWNER/jevkit/internal/sdlc/ledger"
	"github.com/OWNER/jevkit/internal/sdlc/worker"
)

const bugfixChoiceWorkflow = `version: 1
name: choose-fix
description: Route bug reports to the built-in bugfix SDLC.
entry: decide
maxSteps: 8
stages:
  - id: decide
    question:
      prompt: Does this task need a bug fix?
      options: {fix: Fix the bug, skip: No fix is needed}
      routes: {fix: run-bugfix, skip: done}
      fallback: paused
  - id: run-bugfix
    spawn:
      workflow: bugfix
      objective: Resolve the reported bug.
      routes: {succeeded: done, paused: paused, aborted: aborted}
  - id: paused
    finish: paused
  - id: aborted
    finish: aborted
  - id: done
    finish: succeeded
`

func directSpawnWorkflow(name, target string) string {
	return fmt.Sprintf(`version: 1
name: %s
description: Run a child workflow.
entry: child
stages:
  - id: child
    spawn:
      workflow: %s
      routes: {succeeded: done, paused: paused, aborted: aborted}
  - id: paused
    finish: paused
  - id: aborted
    finish: aborted
  - id: done
    finish: succeeded
`, name, target)
}

func TestCustomWorkflowChoiceRunsBuiltInBugfix(t *testing.T) {
	a, _, fj := cliApp(t)
	a.NewJev = func(jev.Config, func() (string, error)) Asker { return fj }
	a.Environ = append(a.Environ, "JEVKIT_TRANSPORT=fixture")
	stageTestRoster(t, a)
	fj.resp = &jev.Response{Answers: map[string]jev.Answer{"route": jev.ChoiceAnswer{Choice: "fix", Confidence: .98}}}
	f := &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "planned", Content: "Plan."}, {Outcome: "changed", Content: "diff --git a/a b/a\n+fix\n"}, {Outcome: "approved"}}}
	a.SdlcExecutor = f
	writeWorkflow(t, a, "choose-fix", bugfixChoiceWorkflow)
	code, explanation, errs := run(a, "", "sdlc", "explain", "choose-fix")
	if code != exitOK || !strings.Contains(explanation, "RUN WORKFLOW: bugfix") || !strings.Contains(explanation, "succeeded") {
		t.Fatalf("explain: %d %q %q", code, explanation, errs)
	}
	code, out, errs := run(a, "", "sdlc", "run", "choose-fix", "--task", "login fails")
	if code != exitOK || !strings.Contains(out, "workflow bugfix: started run") || !strings.Contains(out, "done (succeeded)") {
		t.Fatalf("run: %d %q %q", code, out, errs)
	}
	parentID := strings.Fields(out)[1]
	parent, err := ledger.Open(a.sdlcRunsDir(), parentID).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if parent.Adaptive.Stage != adaptive.Done || parent.StageFlow.Current != "done" || parent.Adaptive.AssignmentCount != 3 {
		t.Fatalf("parent: %+v", parent)
	}
	childID := parent.StageFlow.Transitions[1].ChildRunID
	child, err := ledger.Open(a.sdlcRunsDir(), childID).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if child.ParentRunID != parentID || child.Depth != 1 || child.Workflow != "bugfix" || child.Adaptive.Stage != adaptive.Done || len(f.requests) != 3 || !strings.Contains(f.requests[0].Task, "Subworkflow objective: Resolve the reported bug") {
		t.Fatalf("child: %+v requests=%+v", child, f.requests)
	}
	if data, err := ledger.Open(a.sdlcRunsDir(), parentID).ReadArtifact("patch.diff"); err != nil || !strings.Contains(string(data), "+fix") {
		t.Fatalf("parent diff: %q, %v", data, err)
	}
}

func TestCustomWorkflowSpawnResumesSameChild(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	a.SdlcExecutor = &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "planned", Content: "Plan."}, {Outcome: "answer", Content: "No change."}}}
	writeWorkflow(t, a, "choose-fix", directSpawnWorkflow("choose-fix", "bugfix"))
	code, out, errs := run(a, "", "sdlc", "run", "choose-fix", "--task", "login fails", "--step")
	if code != exitOK {
		t.Fatalf("first step: %d %q %q", code, out, errs)
	}
	parentID := strings.Fields(out)[1]
	parent, _ := ledger.Open(a.sdlcRunsDir(), parentID).ReadRun()
	childID := parent.StageFlow.ChildRunID
	if childID == "" {
		t.Fatalf("child ID not saved: %+v", parent)
	}
	code, _, errs = run(a, "", "sdlc", "resume", parentID)
	if code != exitOK {
		t.Fatalf("resume: %d %q", code, errs)
	}
	parent, _ = ledger.Open(a.sdlcRunsDir(), parentID).ReadRun()
	if parent.Adaptive.Stage != adaptive.Done || parent.StageFlow.Transitions[0].ChildRunID != childID {
		t.Fatalf("resumed parent: %+v", parent)
	}
}

func TestCustomWorkflowCanSpawnProjectWorkflow(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	a.SdlcExecutor = &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "answer", Content: "Resolved."}}}
	writeWorkflow(t, a, "outer", directSpawnWorkflow("outer", "inner"))
	writeWorkflow(t, a, "inner", `version: 1
name: inner
description: Answer the task.
entry: answer
stages:
  - id: answer
    work:
      role: planner
      objective: Answer the request.
      routes: {planned: done, answer: done, no-change: done}
  - id: done
    finish: succeeded
`)
	code, out, errs := run(a, "", "sdlc", "run", "outer", "--task", "explain the issue")
	if code != exitOK || !strings.Contains(out, "workflow inner: started run") {
		t.Fatalf("run: %d %q %q", code, out, errs)
	}
	parentID := strings.Fields(out)[1]
	parent, err := ledger.Open(a.sdlcRunsDir(), parentID).ReadRun()
	if err != nil || parent.Adaptive.Stage != adaptive.Done || parent.Adaptive.AssignmentCount != 1 || parent.StageFlow.Steps != 2 {
		t.Fatalf("parent: %+v, %v", parent, err)
	}
}

func TestCustomWorkflowRoutesAbortedChild(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	writeWorkflow(t, a, "outer", directSpawnWorkflow("outer", "inner"))
	writeWorkflow(t, a, "inner", `version: 1
name: inner
description: Stop this task.
entry: stop
stages:
  - id: stop
    finish: aborted
`)
	code, out, errs := run(a, "", "sdlc", "run", "outer", "--task", "stop")
	if code != exitOK || !strings.Contains(out, "done (aborted)") {
		t.Fatalf("run: %d %q %q", code, out, errs)
	}
}

func TestCustomWorkflowRejectsMissingSpawnTarget(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	writeWorkflow(t, a, "choose-fix", strings.Replace(bugfixChoiceWorkflow, "workflow: bugfix", "workflow: absent", 1))
	code, _, errs := run(a, "", "sdlc", "validate", "choose-fix")
	if code == exitOK || !strings.Contains(errs, "spawn stage \"run-bugfix\"") {
		t.Fatalf("validate: %d %q", code, errs)
	}
}

func TestCustomWorkflowRejectsSelfSpawn(t *testing.T) {
	a := newApp(t)
	writeWorkflow(t, a, "outer", directSpawnWorkflow("outer", "outer"))
	code, _, errs := run(a, "", "sdlc", "validate", "outer")
	if code == exitOK || !strings.Contains(errs, "cannot run its own workflow") {
		t.Fatalf("validate: %d %q", code, errs)
	}
}

func TestChildAssignmentsCountAgainstParentLimit(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	writeFile(t, filepath.Join(a.WorkDir, ".jevkit", "sdlc", "policy.yaml"), "version: 1\nmaxAssignments: 1\n")
	f := &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "answer", Content: "Resolved."}}}
	a.SdlcExecutor = f
	writeWorkflow(t, a, "outer", `version: 1
name: outer
description: Try two child workflows.
entry: first
stages:
  - id: first
    spawn:
      workflow: inner
      routes: {succeeded: second, paused: paused, aborted: paused}
  - id: second
    spawn:
      workflow: inner
      routes: {succeeded: done, paused: paused, aborted: paused}
  - id: paused
    finish: paused
  - id: done
    finish: succeeded
`)
	writeWorkflow(t, a, "inner", `version: 1
name: inner
description: Answer the task.
entry: answer
stages:
  - id: answer
    work:
      role: planner
      objective: Answer the request.
      routes: {planned: done, answer: done, no-change: done}
  - id: done
    finish: succeeded
`)
	code, out, errs := run(a, "", "sdlc", "run", "outer", "--task", "answer")
	if code == exitOK || !strings.Contains(out, "workflow-budget-exhausted") || !strings.Contains(errs, "workflow-budget-exhausted") || len(f.requests) != 1 {
		t.Fatalf("run: %d %q %q requests=%d", code, out, errs, len(f.requests))
	}
}

func TestCustomWorkflowRequiresEverySpawnOutcome(t *testing.T) {
	a := newApp(t)
	writeWorkflow(t, a, "choose-fix", strings.Replace(bugfixChoiceWorkflow, "paused: paused, ", "", 1))
	code, _, errs := run(a, "", "sdlc", "validate", "choose-fix")
	if code == exitOK || !strings.Contains(errs, "needs a route for \"paused\"") {
		t.Fatalf("validate: %d %q", code, errs)
	}
}

func TestCustomWorkflowSpawnDepthIsBounded(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("flow-%d", i)
		next := fmt.Sprintf("flow-%d", i+1)
		if i == 4 {
			next = "bugfix"
		}
		writeWorkflow(t, a, name, fmt.Sprintf(`version: 1
name: %s
description: Nested workflow.
entry: child
stages:
  - id: child
    spawn:
      workflow: %s
      routes: {succeeded: done, paused: paused, aborted: paused}
  - id: paused
    finish: paused
  - id: done
    finish: succeeded
`, name, next))
	}
	code, out, errs := run(a, "", "sdlc", "run", "flow-0", "--task", "nested")
	if code != exitOK || !strings.Contains(out, "workflow-depth-exhausted") || errs != "" {
		t.Fatalf("depth limit: %d %q %q", code, out, errs)
	}
}
