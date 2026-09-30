package stageflow

import (
	"fmt"

	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/spec"
)

// CurrentStageFormatVersion is the authored stage-workflow version on disk and
// in saved run snapshots. Older version-1 workflows remain readable: routes
// such as `changed: assess` still mean the implement stage advances only after
// supervisor fan-out join and verification finish.
const CurrentStageFormatVersion = 1

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

// MigrateSaved upgrades a persisted stageflow snapshot when needed. Version 1
// workflows need no rewrite; unsupported versions fail closed with a migration
// hint so operators can re-author rather than silently reinterpret routes.
func MigrateSaved(s *State) error {
	if s == nil {
		return fmt.Errorf("stageflow: nil state")
	}
	if s.Workflow.Version == 0 {
		// Snapshots that omitted version before validation still behave as v1.
		s.Workflow.Version = CurrentStageFormatVersion
		return nil
	}
	if s.Workflow.Version == CurrentStageFormatVersion {
		return nil
	}
	return fmt.Errorf("stageflow: saved workflow version %d is not readable; re-author as version %d or start a new run", s.Workflow.Version, CurrentStageFormatVersion)
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
	if err := MigrateSaved(s); err != nil {
		return err
	}
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
