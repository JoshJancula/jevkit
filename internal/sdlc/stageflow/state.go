// Package stageflow advances authored SDLC stages using questions and
// structured worker outcomes. Worker eligibility remains with enrollment.
package stageflow

import (
	"fmt"

	"github.com/OWNER/jevkit/internal/sdlc/adaptive"
	"github.com/OWNER/jevkit/internal/sdlc/spec"
)

type Transition struct {
	Stage      string `json:"stage"`
	Answer     string `json:"answer"`
	Next       string `json:"next"`
	ChildRunID string `json:"childRunId,omitempty"`
}

type State struct {
	Workflow    spec.Workflow `json:"workflow"`
	Current     string        `json:"current"`
	Steps       int           `json:"steps"`
	ChildRunID  string        `json:"childRunId,omitempty"`
	Transitions []Transition  `json:"transitions,omitempty"`
}

func New(w spec.Workflow, worker *adaptive.State) (State, error) {
	if err := w.ValidateStages(); err != nil {
		return State{}, err
	}
	s := State{Workflow: w, Current: w.Entry}
	return s, s.enter(worker)
}

func (s State) Stage() (spec.Stage, bool) { return s.Workflow.StageByID(s.Current) }

func (s *State) Advance(answer string, worker *adaptive.State) error {
	stage, ok := s.Stage()
	if !ok {
		return fmt.Errorf("stageflow: unknown current stage %q", s.Current)
	}
	var next string
	switch {
	case stage.Question != nil:
		next = stage.Question.Routes[answer]
		if answer == "fallback" {
			next = stage.Question.Fallback
		}
	case stage.Work != nil:
		next = stage.Work.Routes[answer]
	case stage.Spawn != nil:
		next = stage.Spawn.Routes[answer]
	default:
		return fmt.Errorf("stageflow: finish stage %q has no next stage", stage.ID)
	}
	if next == "" {
		return fmt.Errorf("stageflow: stage %q has no route for %q", stage.ID, answer)
	}
	s.Steps++
	s.Transitions = append(s.Transitions, Transition{Stage: stage.ID, Answer: answer, Next: next, ChildRunID: s.ChildRunID})
	s.ChildRunID = ""
	if s.Steps > s.Workflow.MaxSteps {
		worker.Pause("stage-step-budget-exhausted")
		return nil
	}
	s.Current = next
	return s.enter(worker)
}

func (s *State) enter(worker *adaptive.State) error {
	stage, ok := s.Stage()
	if !ok {
		return fmt.Errorf("stageflow: unknown stage %q", s.Current)
	}
	worker.Outcome = ""
	switch {
	case stage.Question != nil:
		worker.Stage = "question"
	case stage.Work != nil:
		switch stage.Work.Role {
		case "planner":
			worker.Stage = adaptive.Planning
		case "implementer":
			worker.Stage = adaptive.Implementing
		case "assessor":
			if worker.DiffRevision == "" {
				worker.Pause("assessment-without-diff")
				return nil
			}
			worker.Stage = adaptive.Assessing
		default:
			return fmt.Errorf("stageflow: unsupported role %q", stage.Work.Role)
		}
	case stage.Spawn != nil:
		worker.Stage = "spawn"
	case stage.Finish == "paused":
		worker.Pause("workflow-paused")
	case stage.Finish != "":
		worker.Stage = adaptive.Done
		worker.Outcome = stage.Finish
	default:
		return fmt.Errorf("stageflow: invalid stage %q", stage.ID)
	}
	return nil
}
