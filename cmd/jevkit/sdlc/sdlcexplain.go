package sdlc

import (
	"sort"
	"strings"

	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/spec"
)

func (a *App) sdlcExplainAdaptive(kind string) {
	a.Outf("%s  adaptive task kind\n", kind)
	a.Outf("%s\n\n", adaptive.TaskKindDescription(kind))
	a.Heading("FLOW")
	a.Outf("  PLAN       planned ----------> IMPLEMENT\n")
	a.Outf("             answer/no change -> DONE\n")
	a.Outf("  IMPLEMENT  changed ----------> ASSESS\n")
	a.Outf("             answer/no change -> DONE\n")
	a.Outf("  ASSESS     quorum approves --> DONE\n")
	a.Outf("             changes needed --> IMPLEMENT\n")
	a.Outf("\nAn unavailable agent is replaced when eligible; otherwise the run pauses.\n")
	a.Outf("Revision, assignment, and time limits stop repeated work; see sdlc doctor.\n")
	a.Outf("A seeded plan or diff can begin at a later step.\n")
	a.Outf("Assignments use enrolled eligible agents; Jev chooses when several qualify.\n")
	a.Outf("No workflow file is needed.\n")
	a.Outf("Run: jevkit sdlc run %s --task \"...\"\n", kind)
}

func (a *App) sdlcExplainStages(w *spec.Workflow) {
	a.Outf("%s  custom stage workflow\n", w.Name)
	a.Outf("%s\n", w.Description)
	a.Outf("Entry: %s  |  Max transitions: %d\n\n", w.Entry, w.MaxSteps)
	for _, stage := range w.Stages {
		switch {
		case stage.Question != nil:
			q := stage.Question
			a.Outf("%s  QUESTION\n  %s\n", stage.ID, q.Prompt)
			keys := make([]string, 0, len(q.Options))
			for key := range q.Options {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				a.Outf("  %-18s → %-18s %s\n", key, q.Routes[key], q.Options[key])
			}
			a.Outf("  %-18s → %s  (Jev unavailable or uncertain)\n\n", "fallback", q.Fallback)
		case stage.Work != nil:
			a.Outf("%s  %s\n  %s\n", stage.ID, strings.ToUpper(stage.Work.Role), stage.Work.Objective)
			keys := make([]string, 0, len(stage.Work.Routes))
			for key := range stage.Work.Routes {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				a.Outf("  %-18s → %s\n", key, stage.Work.Routes[key])
			}
			a.Outf("\n")
		case stage.Spawn != nil:
			a.Outf("%s  RUN WORKFLOW: %s\n", stage.ID, stage.Spawn.Workflow)
			if stage.Spawn.Objective != "" {
				a.Outf("  %s\n", stage.Spawn.Objective)
			}
			for _, outcome := range []string{"succeeded", "paused", "aborted"} {
				a.Outf("  %-18s → %s\n", outcome, stage.Spawn.Routes[outcome])
			}
			a.Outf("\n")
		default:
			a.Outf("%s  FINISH: %s\n\n", stage.ID, stage.Finish)
		}
	}
	a.Outf("Run: jevkit sdlc run %s --task \"...\"\n", w.Name)
	a.Outf("Work uses enrolled agents; questions use Jev with the declared fallback.\n")
}
