package main

import (
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/OWNER/jevkit/internal/sdlc/ledger"
)

type sdlcProgress struct {
	root         string
	out          io.Writer
	seen         map[string]int
	decisionSeen map[string]int
	last         map[string]string
}

// progressFlush renders new ledger events as short, append-only lines. The
// same layout works in a terminal and a pipe; styled() adds optional color.
func (a *App) progressFlush() {
	p := a.sdlcProgress
	if p == nil {
		return
	}
	if p.seen == nil {
		p.seen = map[string]int{}
	}
	if p.decisionSeen == nil {
		p.decisionSeen = map[string]int{}
	}
	if p.last == nil {
		p.last = map[string]string{}
	}
	runs, err := a.sdlcTree(p.root)
	if err != nil || len(runs) == 0 {
		return
	}
	root := runs[0]
	for _, run := range runs {
		if decisions, err := ledger.Open(a.sdlcRunsDir(), run.RunID).ReadDecisions(); err == nil {
			start := p.decisionSeen[run.RunID]
			if start > len(decisions) {
				start = 0
			}
			p.decisionSeen[run.RunID] = len(decisions)
			for _, d := range decisions[start:] {
				_, _ = fmt.Fprintf(p.out, "%s  decision %s: %s", strings.Repeat("  ", run.Depth), d.Kind, d.Choice)
				if d.Outcome != "" {
					_, _ = fmt.Fprintf(p.out, " (%s)", shortProgressText(d.Outcome, 72))
				}
				if d.Next != "" {
					_, _ = fmt.Fprintf(p.out, " → %s", shortProgressText(d.Next, 72))
				}
				_, _ = fmt.Fprintln(p.out)
			}
		}
		if run.AutoDecisionReason != "" && p.last[run.RunID+"/delegation"] != run.AutoDecisionReason {
			p.last[run.RunID+"/delegation"] = run.AutoDecisionReason
			_, _ = fmt.Fprintf(p.out, "%s  delegation: %s\n", strings.Repeat("  ", run.Depth), shortProgressText(run.AutoDecisionReason, 96))
		}
		if run.Adaptive != nil {
			for _, decision := range run.Adaptive.SpecialistDecisions {
				key := run.RunID + "/specialist/" + decision.Role + "/" + decision.Revision
				value := decision.Choice + "/" + decision.Reason
				if p.last[key] == value {
					continue
				}
				p.last[key] = value
				_, _ = fmt.Fprintf(p.out, "%s  specialist %s: %s", strings.Repeat("  ", run.Depth), decision.Role, decision.Reason)
				if decision.Choice != "" {
					_, _ = fmt.Fprintf(p.out, " (answer %s, confidence %.2f)", decision.Choice, decision.Confidence)
				}
				_, _ = fmt.Fprintln(p.out)
			}
		}
		events, err := ledger.Open(a.sdlcRunsDir(), run.RunID).ReadEvents()
		if err != nil {
			continue
		}
		start := p.seen[run.RunID]
		if start > len(events) {
			start = 0
		}
		p.seen[run.RunID] = len(events)
		for _, event := range events[start:] {
			a.progressEvent(p, root, run, event)
		}
	}
}

func (a *App) progressEvent(p *sdlcProgress, root, run ledger.Run, event ledger.Event) {
	key := run.RunID + "/"
	indent := strings.Repeat("  ", run.Depth)
	stage := event.Stage
	if stage == "" {
		stage = "started"
	}
	status := stage + "/" + event.Outcome
	if p.last[key+"status"] != status {
		p.last[key+"status"] = status
		label := stage
		if event.Outcome != "" {
			label += " (" + event.Outcome + ")"
		}
		_, _ = fmt.Fprintf(p.out, "%s%s: %s  %s\n", indent, run.Workflow, a.styled(p.out, ansiCyan, label), progressElapsed(run.CreatedAt, event.At))
		if run.RunID == p.root && root.TreeUsage != nil && root.Adaptive != nil {
			_, _ = fmt.Fprintf(p.out, "%s  left: %d assignments | %d revisions | %d children", indent,
				max(0, root.Adaptive.MaxAssignments-root.TreeUsage.Assignments),
				max(0, root.Adaptive.MaxRevisions-root.TreeUsage.Revisions),
				max(0, sdlcMaxChildRuns-root.TreeUsage.ChildRuns))
			if policy, _, err := a.sdlcEnrollment(); err == nil {
				if remaining, err := a.treeRemaining(root, policy); err == nil {
					_, _ = fmt.Fprintf(p.out, " | %s", remaining.Round(time.Second))
				}
			}
			_, _ = fmt.Fprintln(p.out)
		}
	}
	if event.Agent != "" {
		assignment := event.Agent + "/" + event.Runtime + "/" + event.Invocation
		if p.last[key+"agent"] != assignment {
			p.last[key+"agent"] = assignment
			_, _ = fmt.Fprintf(p.out, "%s  agent: %s (%s)\n", indent, event.Agent, event.Runtime)
			if event.Reason != "" {
				_, _ = fmt.Fprintf(p.out, "%s    route: %s\n", indent, shortProgressText(event.Reason, 88))
			}
		}
	} else if event.Reason != "" && p.last[key+"reason"] != event.Reason {
		p.last[key+"reason"] = event.Reason
		// A pause reason often repeats the outcome with spaces instead of
		// hyphens. The status line already says it.
		if strings.ReplaceAll(event.Outcome, "-", " ") != event.Reason &&
			(stage != "paused" || run.Adaptive == nil || run.Adaptive.Stage != "paused" || run.Adaptive.PendingReason == "") {
			_, _ = fmt.Fprintf(p.out, "%s  note: %s\n", indent, shortProgressText(event.Reason, 96))
		}
	}
	if stage == "paused" && run.Adaptive != nil && run.Adaptive.Stage == "paused" && run.Adaptive.PendingReason != "" &&
		p.last[key+"pending"] != run.Adaptive.PendingFocus+"/"+run.Adaptive.PendingReason {
		p.last[key+"pending"] = run.Adaptive.PendingFocus + "/" + run.Adaptive.PendingReason
		focus := run.Adaptive.PendingFocus
		if focus == "" {
			focus = "decision"
		}
		_, _ = fmt.Fprintf(p.out, "%s  pending %s: %s\n", indent, shortProgressText(focus, 40), shortProgressText(run.Adaptive.PendingReason, 96))
		if strings.Contains(run.Adaptive.PendingReason, "no-key") {
			_, _ = fmt.Fprintf(p.out, "%s  next: jevkit key set; then jevkit sdlc resume %s\n", indent, run.RunID)
		}
	}
	if event.TransitionKind != "" {
		transition := event.TransitionStage + "/" + event.TransitionAnswer + "/" + event.TransitionNext
		if event.TransitionAnswer != "" && p.last[key+"transition"] != transition {
			p.last[key+"transition"] = transition
			if event.TransitionKind == "spawn" && event.Stage != "spawn" {
				_, _ = fmt.Fprintf(p.out, "%s  workflow %s: %s -> %s\n", indent, a.progressChildName(event.ChildRunID), event.TransitionAnswer, event.TransitionNext)
			} else if event.TransitionKind != "spawn" {
				_, _ = fmt.Fprintf(p.out, "%s  %s %s: %s -> %s\n", indent, event.TransitionKind, event.TransitionStage, event.TransitionAnswer, event.TransitionNext)
			}
		}
	}
	if event.Stage == "spawn" && event.ChildRunID != "" && p.last[key+"child"] != event.ChildRunID {
		p.last[key+"child"] = event.ChildRunID
		_, _ = fmt.Fprintf(p.out, "%s  workflow %s: started run %s\n", indent, a.progressChildName(event.ChildRunID), event.ChildRunID)
	}
}

func (a *App) progressChildName(runID string) string {
	if child, err := ledger.Open(a.sdlcRunsDir(), runID).ReadRun(); err == nil {
		return child.Workflow
	}
	return "child"
}

func progressElapsed(createdAt, eventAt string) string {
	created, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return "0s"
	}
	when, err := time.Parse(time.RFC3339, eventAt)
	if err != nil {
		when = time.Now()
	}
	if when.Before(created) {
		return "0s"
	}
	return when.Sub(created).Round(time.Second).String()
}

func shortProgressText(s string, maxRunes int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	return strings.TrimSpace(string(runes[:maxRunes-1])) + "…"
}
