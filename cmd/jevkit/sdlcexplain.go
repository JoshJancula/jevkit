package main

import (
	"sort"
	"strings"

	"github.com/OWNER/jevkit/internal/sdlc/adaptive"
	"github.com/OWNER/jevkit/internal/sdlc/spec"
)

func (a *App) sdlcExplainAdaptive(kind string) {
	a.outf("%s  adaptive task kind\n", kind)
	a.outf("%s\n\n", adaptive.TaskKindDescription(kind))
	a.heading("FLOW")
	a.outf("  PLAN       planned ----------> IMPLEMENT\n")
	a.outf("             answer/no change -> DONE\n")
	a.outf("  IMPLEMENT  changed ----------> ASSESS\n")
	a.outf("             answer/no change -> DONE\n")
	a.outf("  ASSESS     quorum approves --> DONE\n")
	a.outf("             changes needed --> IMPLEMENT\n")
	a.outf("\nAn unavailable agent is replaced when eligible; otherwise the run pauses.\n")
	a.outf("Revision, assignment, and time limits stop repeated work; see sdlc doctor.\n")
	a.outf("A seeded plan or diff can begin at a later step.\n")
	a.outf("Assignments use enrolled eligible agents; Jev chooses when several qualify.\n")
	a.outf("No workflow file is needed.\n")
	a.outf("Run: jevkit sdlc run %s --task \"...\"\n", kind)
}

func (a *App) sdlcExplainStages(w *spec.Workflow) {
	a.outf("%s  custom stage workflow\n", w.Name)
	a.outf("%s\n", w.Description)
	a.outf("Entry: %s  |  Max transitions: %d\n\n", w.Entry, w.MaxSteps)
	for _, stage := range w.Stages {
		switch {
		case stage.Question != nil:
			q := stage.Question
			a.outf("%s  QUESTION\n  %s\n", stage.ID, q.Prompt)
			keys := make([]string, 0, len(q.Options))
			for key := range q.Options {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				a.outf("  %-18s → %-18s %s\n", key, q.Routes[key], q.Options[key])
			}
			a.outf("  %-18s → %s  (Jev unavailable or uncertain)\n\n", "fallback", q.Fallback)
		case stage.Work != nil:
			a.outf("%s  %s\n  %s\n", stage.ID, strings.ToUpper(stage.Work.Role), stage.Work.Objective)
			keys := make([]string, 0, len(stage.Work.Routes))
			for key := range stage.Work.Routes {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				a.outf("  %-18s → %s\n", key, stage.Work.Routes[key])
			}
			a.outf("\n")
		case stage.Spawn != nil:
			a.outf("%s  RUN WORKFLOW: %s\n", stage.ID, stage.Spawn.Workflow)
			if stage.Spawn.Objective != "" {
				a.outf("  %s\n", stage.Spawn.Objective)
			}
			for _, outcome := range []string{"succeeded", "paused", "aborted"} {
				a.outf("  %-18s → %s\n", outcome, stage.Spawn.Routes[outcome])
			}
			a.outf("\n")
		default:
			a.outf("%s  FINISH: %s\n\n", stage.ID, stage.Finish)
		}
	}
	a.outf("Run: jevkit sdlc run %s --task \"...\"\n", w.Name)
	a.outf("Work uses enrolled agents; questions use Jev with the declared fallback.\n")
}
