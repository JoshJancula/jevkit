package stageflow

import (
	"strings"
	"testing"

	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/spec"
)

const boundedFlow = `version: 1
name: bounded
description: Ask once or loop until the stage budget stops the run.
entry: ask
maxSteps: 2
stages:
  - id: ask
    question:
      prompt: Continue?
      options: {again: Repeat the question., finish: Finish now.}
      routes: {again: ask, finish: done}
      fallback: done
  - id: done
    finish: succeeded
`

func TestQuestionLoopStopsAtMaxSteps(t *testing.T) {
	w, err := spec.Load([]byte(boundedFlow))
	if err != nil {
		t.Fatal(err)
	}
	worker, err := adaptive.New("bounded", "lean", 1, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	flow, err := New(*w, &worker)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := flow.Advance("again", &worker); err != nil {
			t.Fatal(err)
		}
	}
	if worker.Stage != "question" {
		t.Fatalf("stage = %s", worker.Stage)
	}
	if err := flow.Advance("again", &worker); err != nil {
		t.Fatal(err)
	}
	if worker.Stage != adaptive.Paused || worker.Outcome != "stage-step-budget-exhausted" || flow.Steps != 3 {
		t.Fatalf("flow=%+v worker=%+v", flow, worker)
	}
}

func TestMigrateSavedKeepsVersionOneReadable(t *testing.T) {
	s := State{Workflow: spec.Workflow{Version: 0, Name: "old"}}
	if err := MigrateSaved(&s); err != nil || s.Workflow.Version != CurrentStageFormatVersion {
		t.Fatalf("migrate v0: %+v err=%v", s, err)
	}
	s.Workflow.Version = CurrentStageFormatVersion
	if err := MigrateSaved(&s); err != nil {
		t.Fatal(err)
	}
	s.Workflow.Version = 2
	if err := MigrateSaved(&s); err == nil {
		t.Fatal("expected unsupported version error")
	}
}


func TestStageValidationRejectsUnknownTargetAndFallbackChoice(t *testing.T) {
	for _, tc := range []struct{ from, to, want string }{
		{"finish: done", "finish: absent", "not a declared stage"},
		{"options: {again: Repeat the question., finish: Finish now.}", "options: {fallback: Repeat the question., finish: Finish now.}", "answer \"fallback\""},
	} {
		_, err := spec.Load([]byte(strings.Replace(boundedFlow, tc.from, tc.to, 1)))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("want %q, got %v", tc.want, err)
		}
	}
}
