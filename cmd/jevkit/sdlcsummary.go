package main

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/OWNER/jevkit/internal/sdlc/adaptive"
	"github.com/OWNER/jevkit/internal/sdlc/ledger"
	"github.com/OWNER/jevkit/internal/usage"
)

// sdlcFinalSummary reads the saved run tree after the driver stops. It is
// printed after the alternate screen closes so the outcome stays in scrollback.
func (a *App) sdlcFinalSummary(out io.Writer, rootID string, driveErr error) {
	runs, err := a.sdlcTree(rootID)
	if err != nil || len(runs) == 0 {
		cause := "Saved run not found."
		if err != nil {
			cause = summaryText(a, err.Error())
		}
		fmt.Fprintf(out, "\nRun %s: status unavailable\n  Cause: %s\n  Next:  Verify the run ID and inspect its saved status.\n", ttyClean(rootID), cause)
		return
	}
	root := runs[0]
	stage, outcome := "unknown", ""
	if root.Adaptive != nil {
		stage, outcome = root.Adaptive.Stage, root.Adaptive.Outcome
	}
	fmt.Fprintf(out, "\n%s\n  Run:      %s\n", a.styled(out, ansiCyan, "Run summary"), rootID)
	if root.Workflow != "" {
		fmt.Fprintf(out, "  Workflow: %s\n", summaryText(a, root.Workflow))
	}
	if root.Task != "" {
		fmt.Fprintf(out, "  Task:     %s\n", summaryText(a, root.Task))
	}
	status := strings.ToUpper(ttyClean(stage))
	if outcome != "" {
		status += " (" + ttyClean(outcome) + ")"
	}
	statusColor := ansiCyan
	if stage == adaptive.Done {
		statusColor = ansiGreen
	} else if stage == adaptive.Paused {
		statusColor = ansiYellow
	}
	fmt.Fprintf(out, "  State:    %s\n", a.styled(out, statusColor, status))
	for _, child := range runs[1:] {
		if child.Adaptive != nil {
			fmt.Fprintf(out, "  Child:    %s — %s", ttyClean(child.RunID), ttyClean(child.Adaptive.Stage))
			if child.Adaptive.Outcome != "" {
				fmt.Fprintf(out, " (%s)", ttyClean(child.Adaptive.Outcome))
			}
			fmt.Fprintln(out)
		}
	}
	if stage == adaptive.Paused {
		cause := a.sdlcPauseCause(rootID, driveErr)
		if cause == "" {
			cause = outcome
		}
		if cause != "" {
			fmt.Fprintf(out, "  Cause:    %s\n", summaryText(a, cause))
		}
	} else if driveErr != nil {
		fmt.Fprintf(out, "  Note:     %s\n", summaryText(a, driveErr.Error()))
	}
	if root.Adaptive != nil && root.RequirePlanApproval && root.Adaptive.PlanRevision != "" && root.ApprovedPlanRevision != root.Adaptive.PlanRevision {
		fmt.Fprintf(out, "  Plan:     %s\n", ledger.Open(a.sdlcRunsDir(), rootID).Dir+"/artifacts/plan.md")
	}
	if root.Adaptive != nil && root.Adaptive.Outcome == "child-plan-approval-required" && root.StageFlow != nil {
		fmt.Fprintf(out, "  Plan:     %s\n", ledger.Open(a.sdlcRunsDir(), root.StageFlow.ChildRunID).Dir+"/artifacts/plan.md")
	}

	var actions []ledger.Decision
	for _, run := range runs {
		decisions, err := ledger.Open(a.sdlcRunsDir(), run.RunID).ReadDecisions()
		if err != nil {
			continue
		}
		for _, d := range decisions {
			if d.Kind == "invocation-outcome" || (d.Kind == "stage-transition" && d.Trigger == "workflow question") {
				actions = append(actions, d)
			}
		}
	}
	sort.SliceStable(actions, func(i, j int) bool { return actions[i].At < actions[j].At })
	fmt.Fprintln(out, "\n"+a.styled(out, ansiCyan, "  What happened:"))
	if len(actions) == 0 {
		var events []ledger.Event
		for _, run := range runs {
			items, err := ledger.Open(a.sdlcRunsDir(), run.RunID).ReadEvents()
			if err == nil {
				events = append(events, items...)
			}
		}
		sort.SliceStable(events, func(i, j int) bool { return events[i].At < events[j].At })
		if len(events) == 0 {
			fmt.Fprintln(out, "    No completed agent action recorded yet.")
		}
		for _, e := range events[max(0, len(events)-4):] {
			line := e.Stage
			if e.Agent != "" {
				line += " · " + e.Agent
			}
			if e.Outcome != "" {
				line += ": " + e.Outcome
			}
			if e.Reason != "" {
				line += " — " + e.Reason
			}
			fmt.Fprintf(out, "    %s\n", summaryText(a, line))
		}
	}
	for _, d := range actions[max(0, len(actions)-4):] {
		role := d.Stage
		switch role {
		case adaptive.Planning:
			role = "planner"
		case adaptive.Implementing:
			role = "implementer"
		case adaptive.Assessing:
			role = "assessor"
		}
		if d.Kind == "stage-transition" {
			role = "question " + role
		}
		actor := ""
		if d.Kind == "invocation-outcome" && d.Trigger != "" {
			actor = " " + d.Trigger
		}
		line := fmt.Sprintf("%s%s: %s", role, actor, d.Choice)
		if d.Next != "" {
			line += " → " + d.Next
		} else if d.Outcome != "" {
			line += " → " + d.Outcome
		}
		fmt.Fprintf(out, "    %s\n", summaryText(a, line))
	}

	fmt.Fprintln(out, "\n"+a.styled(out, ansiCyan, "  Token usage by runtime and model:"))
	records, err := usage.ReadRecords(usage.Path(a.stateHome()))
	var linked []usage.Record
	if err == nil {
		ids := map[string]bool{}
		for _, run := range runs {
			ids[run.RunID] = true
		}
		for _, rec := range records {
			if ids[rec.RunID] {
				linked = append(linked, rec)
			}
		}
	}
	a.sdlcUsageTable(out, runs, linked)
	if err != nil {
		fmt.Fprintln(out, "    Jev usage file unavailable; Jev rows may be missing.")
	}
	fmt.Fprintf(out, "\n  %s     %s\n", a.styled(out, ansiCyan, "Next:"), summaryText(a, sdlcSummaryNext(root, rootID)))
	fmt.Fprintf(out, "  %s     jevkit sdlc logs %s\n", a.styled(out, ansiCyan, "Logs:"), rootID)
}

func (a *App) sdlcUsageTable(out io.Writer, runs []ledger.Run, linked []usage.Record) {
	type binding struct{ runtime, model string }
	groups := map[binding]*runtimeTotals{}
	for _, run := range runs {
		seen := map[string]ledger.InvocationUsage{}
		for _, u := range run.Usage {
			seen[u.Invocation] = u
		}
		for _, u := range seen {
			key := binding{u.Runtime, u.Model}
			if groups[key] == nil {
				groups[key] = &runtimeTotals{}
			}
			addRuntime(groups[key], u)
		}
	}
	jev := usage.Aggregate(linked, usage.Filter{}, a.getenv)
	for _, rec := range linked {
		if rec.Transport == usage.TransportFixture {
			continue
		}
		key := binding{"jev", rec.Model}
		if groups[key] == nil {
			groups[key] = &runtimeTotals{}
		}
		g := groups[key]
		g.Invocations++
		if rec.UsageSource == usage.SourceMeasured {
			g.InputTokens += int64(rec.InputTokens)
			g.OutputTokens += int64(rec.OutputTokens)
		} else {
			g.UnknownInput++
			g.UnknownOutput++
		}
	}
	keys := make([]binding, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].runtime != keys[j].runtime {
			return keys[i].runtime < keys[j].runtime
		}
		return keys[i].model < keys[j].model
	})
	if len(keys) == 0 {
		fmt.Fprintln(out, "    No recorded model usage.")
		return
	}
	rows := make([][]string, 0, len(keys))
	for _, key := range keys {
		g := groups[key]
		model := key.model
		if model == "" {
			model = "(unknown)"
		}
		cost := "—"
		if key.runtime == "jev" && jev.Cost != nil && g.UnknownInput == 0 {
			cost = fmt.Sprintf("~$%.6f", float64(g.InputTokens)*jev.Cost.InputUSDPerMTok/1e6+float64(g.OutputTokens)*jev.Cost.OutputUSDPerMTok/1e6)
		} else if g.SuppliedCostUSD != nil {
			cost = fmt.Sprintf("$%.6f", *g.SuppliedCostUSD)
		}
		rows = append(rows, []string{summaryText(a, key.runtime), summaryText(a, model), fmt.Sprint(g.Invocations), usageCount(g.InputTokens, g.UnknownInput), usageCount(g.OutputTokens, g.UnknownOutput), cost})
	}
	writeTable(out, []string{a.styled(out, ansiCyan, "RUNTIME"), a.styled(out, ansiCyan, "MODEL"), a.styled(out, ansiCyan, "CALLS"), a.styled(out, ansiCyan, "INPUT"), a.styled(out, ansiCyan, "OUTPUT"), a.styled(out, ansiCyan, "COST")}, rows)
	fmt.Fprintln(out, "    Cost: — unavailable; ~ estimated Jev cost. Runtime cost includes only calls that reported it.")
}

func usageCount(known int64, unknown int) string {
	if unknown > 0 {
		return fmt.Sprintf("%s (%d unknown)", formatInt(known), unknown)
	}
	return formatInt(known)
}

func formatInt(n int64) string {
	digits := strconv.FormatInt(n, 10)
	start := 0
	if digits[0] == '-' {
		start = 1
	}
	groups := (len(digits) - start - 1) / 3
	if groups == 0 {
		return digits
	}

	formatted := make([]byte, 0, len(digits)+groups)
	formatted = append(formatted, digits[:start]...)
	for i := start; i < len(digits); i++ {
		if i > start && (len(digits)-i)%3 == 0 {
			formatted = append(formatted, ',')
		}
		formatted = append(formatted, digits[i])
	}
	return string(formatted)
}

func summaryText(a *App, value string) string {
	return shortProgressText(ttyClean(a.sdlcRedactedDisplay(value)), 150)
}

func sdlcSummaryNext(run ledger.Run, rootID string) string {
	if run.Adaptive == nil {
		return "Inspect the saved run and logs."
	}
	st := run.Adaptive
	switch st.Stage {
	case adaptive.Done:
		return "Review the saved logs and artifacts if needed."
	case adaptive.Paused:
		if st.Outcome == "child-plan-approval-required" {
			return "Review the child plan, then run: jevkit sdlc resume " + rootID + " --approve-plan"
		}
		if st.Outcome == "plan-approval-required" {
			return "Review plan.md, then run: jevkit sdlc resume " + rootID + " --approve-plan"
		}
		if st.Outcome == "automatic-child-paused" && run.AutoChildRunID != "" {
			return "Inspect child " + run.AutoChildRunID + ", then resume the parent: jevkit sdlc resume " + rootID
		}
		if st.Outcome == "review-workspace-drift" || st.Outcome == "review-recovery-invalid" {
			return "Reassess the changed workspace: jevkit sdlc resume " + rootID + " --retry-failed"
		}
		if failedPauseRole(st.Outcome) != "" {
			return "Fix the cause or enroll another eligible agent, then run: jevkit sdlc resume " + rootID + " --retry-failed"
		}
		if st.PendingDecision != "" {
			return "Address the pending decision, then run: jevkit sdlc resume " + rootID
		}
		if strings.HasPrefix(st.Outcome, "no-eligible-") {
			if st.Profile != "" {
				return "Enroll an eligible agent and start a new run. Check: jevkit sdlc doctor --policy " + st.Profile
			}
			return "Enroll an eligible agent and start a new run. Check: jevkit sdlc doctor"
		}
		return "Review the pause cause and limits; start a new run after addressing them."
	default:
		if run.RequirePlanApproval && st.PlanRevision != "" && run.ApprovedPlanRevision != st.PlanRevision {
			return "Review plan.md, then run: jevkit sdlc resume " + rootID + " --approve-plan"
		}
		return "Continue this run: jevkit sdlc resume " + rootID
	}
}
