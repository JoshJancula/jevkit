package sdlc

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	usagecmd "github.com/JoshJancula/jevkit/cmd/jevkit/usage"
	"github.com/JoshJancula/jevkit/internal/redact/config"
	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/usage"
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
		_, _ = fmt.Fprintf(out, "\nRun %s: status unavailable\n  Cause: %s\n  Next:  Verify the run ID and inspect its saved status.\n", ttyClean(rootID), cause)
		return
	}
	root := runs[0]
	stage, outcome := "unknown", ""
	if root.Adaptive != nil {
		stage, outcome = root.Adaptive.Stage, root.Adaptive.Outcome
	}
	_, _ = fmt.Fprintf(out, "\n%s\n  Run:      %s\n", a.Styled(out, app.ANSICyan, "Run summary"), rootID)
	if root.Workflow != "" {
		_, _ = fmt.Fprintf(out, "  Workflow: %s\n", summaryText(a, root.Workflow))
	}
	if root.Task != "" {
		_, _ = fmt.Fprintf(out, "  Task:     %s\n", summaryText(a, root.Task))
	}
	status := strings.ToUpper(ttyClean(stage))
	if outcome != "" {
		status += " (" + ttyClean(outcome) + ")"
	}
	statusColor := app.ANSICyan
	switch stage {
	case adaptive.Done:
		statusColor = app.ANSIGreen
	case adaptive.Paused:
		statusColor = app.ANSIYellow
	}
	_, _ = fmt.Fprintf(out, "  State:    %s\n", a.Styled(out, statusColor, status))
	for _, child := range runs[1:] {
		if child.Adaptive != nil {
			_, _ = fmt.Fprintf(out, "  Child:    %s — %s", ttyClean(child.RunID), ttyClean(child.Adaptive.Stage))
			if child.Adaptive.Outcome != "" {
				_, _ = fmt.Fprintf(out, " (%s)", ttyClean(child.Adaptive.Outcome))
			}
			_, _ = fmt.Fprintln(out)
		}
	}
	if stage == adaptive.Paused {
		cause := a.sdlcPauseCause(rootID, driveErr)
		if cause == "" {
			cause = outcome
		}
		if cause != "" {
			_, _ = fmt.Fprintf(out, "  Cause:    %s\n", summaryText(a, cause))
		}
	} else if driveErr != nil {
		_, _ = fmt.Fprintf(out, "  Note:     %s\n", summaryText(a, driveErr.Error()))
	}
	if root.Adaptive != nil && root.RequirePlanApproval && root.Adaptive.PlanRevision != "" && !planApprovalComplete(root) {
		_, _ = fmt.Fprintf(out, "  Plan:     %s\n", ledger.Open(a.SDLCRunsDir(), rootID).Dir+"/artifacts/plan.md")
	}
	if root.Adaptive != nil && root.Adaptive.Outcome == "command-authorization-required" {
		_, _ = fmt.Fprintf(out, "  Checks:   %s\n", ledger.Open(a.SDLCRunsDir(), rootID).Dir+"/artifacts/checks.json")
	}
	if root.Adaptive != nil && root.Adaptive.Outcome == "child-plan-approval-required" && root.StageFlow != nil {
		_, _ = fmt.Fprintf(out, "  Plan:     %s\n", ledger.Open(a.SDLCRunsDir(), root.StageFlow.ChildRunID).Dir+"/artifacts/plan.md")
	}

	var actions []ledger.Decision
	for _, run := range runs {
		decisions, err := ledger.Open(a.SDLCRunsDir(), run.RunID).ReadDecisions()
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
	_, _ = fmt.Fprintln(out, "\n"+a.Styled(out, app.ANSICyan, "  What happened:"))
	if len(actions) == 0 {
		var events []ledger.Event
		for _, run := range runs {
			items, err := ledger.Open(a.SDLCRunsDir(), run.RunID).ReadEvents()
			if err == nil {
				events = append(events, items...)
			}
		}
		sort.SliceStable(events, func(i, j int) bool { return events[i].At < events[j].At })
		if len(events) == 0 {
			_, _ = fmt.Fprintln(out, "    No completed agent action recorded yet.")
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
			_, _ = fmt.Fprintf(out, "    %s\n", summaryText(a, line))
		}
	}
	for _, d := range actions[max(0, len(actions)-4):] {
		role := d.Stage
		switch role {
		case adaptive.Planning:
			role = "planner"
		case adaptive.Implementing:
			role = "implementer"
		case adaptive.Verifying:
			role = "verifier"
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
		_, _ = fmt.Fprintf(out, "    %s\n", summaryText(a, line))
	}
	if stage == adaptive.Done && outcome == "answer" {
		a.sdlcSummaryAnswer(out, actions)
	}

	_, _ = fmt.Fprintln(out, "\n"+a.Styled(out, app.ANSICyan, "  Token usage by runtime and model:"))
	records, err := usage.ReadRecords(usage.Path(a.StateHome()))
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
		_, _ = fmt.Fprintln(out, "    Jev usage file unavailable; Jev rows may be missing.")
	}
	_, _ = fmt.Fprintf(out, "\n  %s     %s\n", a.Styled(out, app.ANSICyan, "Next:"), summaryText(a, sdlcSummaryNext(root, rootID)))
	_, _ = fmt.Fprintf(out, "  %s     jevkit sdlc logs %s\n", a.Styled(out, app.ANSICyan, "Logs:"), rootID)
}

// An answer run's useful result lives in an invocation artifact. Show it in
// shell scrollback after the live view closes, with the same redaction and
// terminal-control filtering used for other agent text.
func (a *App) sdlcSummaryAnswer(out io.Writer, actions []ledger.Decision) {
	const displayLimit = 64 << 10
	for i := len(actions) - 1; i >= 0; i-- {
		d := actions[i]
		if d.Kind != "invocation-outcome" || d.Choice != "answer" || d.Invocation == "" {
			continue
		}
		artifact := "responses/" + d.Invocation + ".txt"
		data, err := ledger.Open(a.SDLCRunsDir(), d.RunID).ReadArtifact(artifact)
		if err != nil || strings.TrimSpace(string(data)) == "" {
			continue
		}
		truncated := len(data) > displayLimit
		if truncated {
			data = data[:displayLimit]
		}
		cfg, err := config.Load(a.LoadOptions())
		if err != nil {
			break
		}
		redactor, err := cfg.Redactor()
		if err != nil {
			break
		}
		clean, err := redactor.Apply(string(data))
		if err != nil {
			break
		}
		_, _ = fmt.Fprintln(out, "\n  Answer:")
		for _, line := range strings.Split(strings.TrimRight(clean.Text, "\n"), "\n") {
			_, _ = fmt.Fprintf(out, "    %s\n", ttySafeLine(line))
		}
		if truncated {
			_, _ = fmt.Fprintln(out, "    … Answer shortened for terminal display; the saved response contains the full text.")
		}
		return
	}
	_, _ = fmt.Fprintln(out, "\n  Answer: Saved agent response unavailable; inspect the run logs and artifacts.")
}

func (a *App) sdlcUsageTable(out io.Writer, runs []ledger.Run, linked []usage.Record) {
	type binding struct{ runtime, model string }
	groups := map[binding]*usagecmd.RuntimeTotals{}
	for _, run := range runs {
		seen := map[string]ledger.InvocationUsage{}
		for _, u := range run.Usage {
			seen[u.Invocation] = u
		}
		for _, u := range seen {
			key := binding{u.Runtime, u.Model}
			if groups[key] == nil {
				groups[key] = &usagecmd.RuntimeTotals{}
			}
			usagecmd.AddRuntime(groups[key], u)
		}
	}
	jev := usage.Aggregate(linked, usage.Filter{}, a.Getenv)
	for _, rec := range linked {
		if rec.Transport == usage.TransportFixture {
			continue
		}
		key := binding{"jev", rec.Model}
		if groups[key] == nil {
			groups[key] = &usagecmd.RuntimeTotals{}
		}
		g := groups[key]
		if rec.Status == "" || rec.Status == "ok" || strings.HasPrefix(rec.Status, "http-2") {
			g.Invocations++
		}
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
		_, _ = fmt.Fprintln(out, "    No recorded model usage.")
		return
	}
	rows := make([][]string, 0, len(keys))
	var totalInvocations int
	var totalToolCalls int64
	var unknownToolCalls int
	for _, key := range keys {
		g := groups[key]
		if key.runtime != "jev" {
			totalInvocations += g.Invocations
			totalToolCalls += g.ToolCalls
			unknownToolCalls += g.UnknownToolCalls
		}
		model := key.model
		if model == "" {
			model = "(unknown)"
		}
		cost := "—"
		if key.runtime == "jev" && jev.Cost != nil {
			cost = fmt.Sprintf("~$%.6f", float64(g.InputTokens)*jev.Cost.InputUSDPerMTok/1e6+float64(g.OutputTokens)*jev.Cost.OutputUSDPerMTok/1e6)
			if g.UnknownInput > 0 {
				cost += fmt.Sprintf(" (+%d unavailable)", g.UnknownInput)
			}
		} else if g.SuppliedCostUSD != nil {
			cost = fmt.Sprintf("$%.6f", *g.SuppliedCostUSD)
			if g.UnknownCost > 0 {
				cost += fmt.Sprintf(" (+%d unavailable)", g.UnknownCost)
			}
		}
		toolCount := "—"
		if key.runtime != "jev" {
			toolCount = app.UsageCount(g.ToolCalls, g.UnknownToolCalls)
		}
		rows = append(rows, []string{summaryText(a, key.runtime), summaryText(a, model), fmt.Sprint(g.Invocations), toolCount, app.UsageCount(g.InputTokens, g.UnknownInput), app.UsageCount(g.OutputTokens, g.UnknownOutput), cost})
	}
	app.WriteTable(out, []string{a.Styled(out, app.ANSICyan, "RUNTIME"), a.Styled(out, app.ANSICyan, "MODEL"), a.Styled(out, app.ANSICyan, "INVOCATIONS"), a.Styled(out, app.ANSICyan, "TOOL CALLS"), a.Styled(out, app.ANSICyan, "INPUT"), a.Styled(out, app.ANSICyan, "OUTPUT"), a.Styled(out, app.ANSICyan, "COST")}, rows)
	_, _ = fmt.Fprintf(out, "    Total: %d invocations, %s tool calls\n", totalInvocations, app.UsageCount(totalToolCalls, unknownToolCalls))
	_, _ = fmt.Fprintln(out, "    Cost: — unavailable; ~ estimated Jev cost. Runtime cost includes only invocations that reported it.")
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
			return "Review plan.md, checks.json, and subtasks.json, then run: jevkit sdlc resume " + rootID + " --approve-plan"
		}
		if st.Outcome == "command-authorization-required" {
			return "Review checks.json, then run: jevkit sdlc resume " + rootID + " --authorize-checks"
		}
		if st.Outcome == "automatic-child-paused" && run.AutoChildRunID != "" {
			return "Inspect child " + run.AutoChildRunID + ", then resume the parent: jevkit sdlc resume " + rootID
		}
		if st.Outcome == "review-workspace-drift" || st.Outcome == "review-recovery-invalid" {
			return "Reassess the changed workspace: jevkit sdlc resume " + rootID + " --retry-failed"
		}
		if st.Outcome == adaptive.OutcomeVerificationEnvironment {
			return "Fix the supervisor environment named in the cause (e.g. which toolchain is first on PATH), then re-run verification: jevkit sdlc resume " + rootID + " --retry-failed"
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
		if run.RequirePlanApproval && st.PlanRevision != "" && !planApprovalComplete(run) {
			return "Review plan.md, checks.json, and subtasks.json, then run: jevkit sdlc resume " + rootID + " --approve-plan"
		}
		return "Continue this run: jevkit sdlc resume " + rootID
	}
}
