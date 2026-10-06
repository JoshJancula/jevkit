package sdlc

import (
	"sort"
	"strings"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
)

// sdlcShowTimeline turns durable decisions into a short account of a run.
// The full decision stream remains available through sdlc logs.
func (a *App) sdlcShowTimeline(infos map[string]sdlcRunInfo, rootID string) {
	a.Heading("WHAT HAPPENED")
	type entry struct {
		at, runID, stage, action, detail, invocation string
	}
	var entries []entry
	var visit func(string)
	visit = func(id string) {
		info, ok := infos[id]
		if !ok {
			return
		}
		decisions, err := ledger.Open(a.SDLCRunsDir(), id).ReadDecisions()
		if err == nil {
			for _, d := range decisions {
				if !sdlcTimelineKind(d.Kind, d.Trigger) {
					continue
				}
				action := d.Kind
				if d.Kind == "invocation-outcome" {
					action = d.Trigger + ": " + d.Choice
				} else if d.Choice != "" {
					action += ": " + d.Choice
				}
				detail := d.Detail
				if detail == "" {
					detail = d.Next
				}
				if d.Outcome != "" {
					action += " → " + d.Outcome
				}
				entries = append(entries, entry{d.At, id, d.Stage, action, detail, d.Invocation})
			}
		}
		for _, child := range info.Children {
			visit(child)
		}
	}
	visit(rootID)
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].at < entries[j].at })
	if len(entries) == 0 {
		a.Outf("  No completed actions recorded yet.\n")
		return
	}
	if len(entries) > 12 {
		a.Outf("  Showing the latest 12 of %d events.\n", len(entries))
		entries = entries[len(entries)-12:]
	}
	for _, e := range entries {
		label := e.runID
		if e.stage != "" {
			label += " · " + e.stage
		}
		line := strings.TrimSpace(label + " · " + e.action)
		if e.detail != "" {
			line += " — " + e.detail
		}
		a.Outf("  %s  %s\n", ttyClean(e.at), summaryText(a, line))
		if app.RunIDPattern.MatchString(e.invocation) {
			a.Outf("    Log: jevkit sdlc logs %s --invocation %s\n", e.runID, e.invocation)
		}
	}
}

func sdlcTimelineKind(kind, trigger string) bool {
	switch kind {
	case "invocation-outcome", "supervisor-verification", "plan-approval", "command-authorization", "retry", "fanout-integration", "review-followup":
		return true
	case "stage-transition":
		return trigger == "workflow question"
	default:
		return false
	}
}
