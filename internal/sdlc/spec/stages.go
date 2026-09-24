package spec

import (
	"fmt"
	"strings"
)

const DefaultMaxStageSteps = 20

func (w *Workflow) StageByID(id string) (Stage, bool) {
	for _, stage := range w.Stages {
		if stage.ID == id {
			return stage, true
		}
	}
	return Stage{}, false
}

// ValidateStages checks the stage-oriented SDLC format. Questions route by
// named answers; work stages route by structured SDLC worker outcomes.
func (w *Workflow) ValidateStages() error {
	if w.Version != 1 {
		return fmt.Errorf("sdlc: workflow version must be 1")
	}
	if !nameRE.MatchString(w.Name) || strings.TrimSpace(w.Description) == "" {
		return fmt.Errorf("sdlc: stage workflow needs a valid name and description")
	}
	if len(w.Nodes) > 0 || len(w.Stages) == 0 {
		return fmt.Errorf("sdlc: stage workflow must use stages, not nodes")
	}
	if w.Entry == "" {
		return fmt.Errorf("sdlc: stage workflow requires entry")
	}
	if w.MaxSteps == 0 {
		w.MaxSteps = DefaultMaxStageSteps
	}
	if w.MaxSteps < 1 || w.MaxSteps > 1000 {
		return fmt.Errorf("sdlc: maxSteps must be between 1 and 1000")
	}
	if len(w.Stages) > 64 {
		return fmt.Errorf("sdlc: stage workflow has more than 64 stages")
	}
	ids := map[string]bool{}
	for _, stage := range w.Stages {
		if !idRE.MatchString(stage.ID) || ids[stage.ID] {
			return fmt.Errorf("sdlc: stage IDs must be valid and unique: %q", stage.ID)
		}
		ids[stage.ID] = true
		count := 0
		if stage.Question != nil {
			count++
		}
		if stage.Work != nil {
			count++
		}
		if stage.Spawn != nil {
			count++
		}
		if stage.Finish != "" {
			count++
		}
		if count != 1 {
			return fmt.Errorf("sdlc: stage %q needs exactly one of question, work, spawn, or finish", stage.ID)
		}
		if q := stage.Question; q != nil {
			if strings.TrimSpace(q.Prompt) == "" || len(q.Options) < 2 || len(q.Options) > 16 || q.Fallback == "" {
				return fmt.Errorf("sdlc: question stage %q needs a prompt, 2-16 options, and fallback", stage.ID)
			}
			if q.MinConfidence == 0 {
				q.MinConfidence = 0.85
			}
			if q.MinConfidence < 0 || q.MinConfidence > 1 {
				return fmt.Errorf("sdlc: question stage %q minConfidence must be between 0 and 1", stage.ID)
			}
			for answer, description := range q.Options {
				if answer == "fallback" || !idRE.MatchString(answer) || strings.TrimSpace(description) == "" || q.Routes[answer] == "" {
					return fmt.Errorf("sdlc: question stage %q needs a description and route for answer %q", stage.ID, answer)
				}
			}
			for answer := range q.Routes {
				if _, ok := q.Options[answer]; !ok {
					return fmt.Errorf("sdlc: question stage %q routes unknown answer %q", stage.ID, answer)
				}
			}
		}
		if work := stage.Work; work != nil {
			if strings.TrimSpace(work.Objective) == "" {
				return fmt.Errorf("sdlc: work stage %q needs an objective", stage.ID)
			}
			var outcomes []string
			switch work.Role {
			case "planner":
				outcomes = []string{"planned", "answer", "no-change"}
			case "implementer":
				outcomes = []string{"changed", "answer", "no-change"}
			case "assessor":
				outcomes = []string{"approved", "changes-required"}
			default:
				return fmt.Errorf("sdlc: work stage %q role must be planner, implementer, or assessor", stage.ID)
			}
			for _, outcome := range outcomes {
				if work.Routes[outcome] == "" {
					return fmt.Errorf("sdlc: work stage %q needs a route for %q", stage.ID, outcome)
				}
			}
			if len(work.Routes) != len(outcomes) {
				return fmt.Errorf("sdlc: work stage %q has an unsupported outcome route", stage.ID)
			}
		}
		if spawn := stage.Spawn; spawn != nil {
			if !nameRE.MatchString(spawn.Workflow) {
				return fmt.Errorf("sdlc: spawn stage %q needs a workflow name", stage.ID)
			}
			for _, outcome := range []string{"succeeded", "paused", "aborted"} {
				if spawn.Routes[outcome] == "" {
					return fmt.Errorf("sdlc: spawn stage %q needs a route for %q", stage.ID, outcome)
				}
			}
			if len(spawn.Routes) != 3 {
				return fmt.Errorf("sdlc: spawn stage %q has an unsupported outcome route", stage.ID)
			}
		}
		if stage.Finish != "" && stage.Finish != "succeeded" && stage.Finish != "paused" && stage.Finish != "aborted" {
			return fmt.Errorf("sdlc: finish stage %q must be succeeded, paused, or aborted", stage.ID)
		}
	}
	if !ids[w.Entry] {
		return fmt.Errorf("sdlc: entry %q is not a declared stage", w.Entry)
	}
	seen := map[string]bool{}
	var walk func(string) error
	walk = func(id string) error {
		if !ids[id] {
			return fmt.Errorf("sdlc: route target %q is not a declared stage", id)
		}
		if seen[id] {
			return nil
		}
		seen[id] = true
		stage, _ := w.StageByID(id)
		if stage.Question != nil {
			for _, target := range stage.Question.Routes {
				if err := walk(target); err != nil {
					return err
				}
			}
			return walk(stage.Question.Fallback)
		}
		if stage.Work != nil {
			for _, target := range stage.Work.Routes {
				if err := walk(target); err != nil {
					return err
				}
			}
		}
		if stage.Spawn != nil {
			for _, target := range stage.Spawn.Routes {
				if err := walk(target); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(w.Entry); err != nil {
		return err
	}
	finished := false
	for _, stage := range w.Stages {
		if !seen[stage.ID] {
			return fmt.Errorf("sdlc: stage %q is unreachable from entry", stage.ID)
		}
		finished = finished || stage.Finish != ""
	}
	if !finished {
		return fmt.Errorf("sdlc: stage workflow has no reachable finish stage")
	}
	return nil
}
