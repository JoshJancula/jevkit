package sdlc

import (
	"context"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/enrollment"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
)

func budgetDefaults(run ledger.Run, p enrollment.Policy) ledger.Allowances {
	l := ledger.Allowances{Assignments: p.MaxAssignments, Revisions: p.MaxRevisions,
		Children: sdlcMaxChildRuns, Seconds: float64(p.MaxRunSeconds), CostUSD: p.MaxEstimatedCostUSD}
	if run.Adaptive != nil {
		if run.Adaptive.MaxAssignments > 0 {
			l.Assignments = run.Adaptive.MaxAssignments
		}
		if run.Adaptive.MaxRevisions > 0 {
			l.Revisions = run.Adaptive.MaxRevisions
		}
		if run.Adaptive.MaxEstimatedCostUSD > 0 {
			l.CostUSD = run.Adaptive.MaxEstimatedCostUSD
		}
	}
	if run.StageFlow != nil {
		l.Steps = run.StageFlow.Workflow.MaxSteps
	}
	if run.BudgetDefaults != nil {
		l = *run.BudgetDefaults
	}
	return l
}

func snapshotBudget(run *ledger.Run, p enrollment.Policy) {
	l := budgetDefaults(*run, p)
	run.BudgetDefaults = &l
	run.InvocationSeconds = float64(p.MaxInvocationSeconds)
	run.Adaptive.TreeBudget = true
}

func (a *App) initialBudget(root ledger.Run, p enrollment.Policy) (ledger.Budget, error) {
	now := a.Clock()
	b := ledger.NewBudget(budgetDefaults(root, p), now, root.InvocationSeconds)
	if b.InvocationSeconds <= 0 {
		b.InvocationSeconds = float64(p.MaxInvocationSeconds)
	}
	if root.TreeUsage != nil {
		u := root.TreeUsage
		b.Usage = ledger.Allowances{Assignments: u.Assignments, Revisions: u.Revisions, Children: u.ChildRuns, Steps: u.StageSteps, CostUSD: u.EstimatedCostUSD}
	} else if root.Adaptive != nil {
		b.Usage.Assignments, b.Usage.Revisions, b.Usage.CostUSD = root.Adaptive.AssignmentCount, root.Adaptive.RevisionCount, root.Adaptive.EstimatedCostUSD
	}
	if root.BudgetDefaults == nil {
		// Legacy ledgers did not record idle periods. Do not pretend elapsed
		// time is measured active work or charge time after migration.
		created, err := time.Parse(time.RFC3339, root.CreatedAt)
		if err != nil {
			return b, err
		}
		b.Usage.Seconds = math.Min(b.Limits.Seconds, math.Max(0, now.Sub(created).Seconds()))
		b.TimeEstimated = true
		if seconds, ok := a.historicalActiveTime(root); ok {
			b.Usage.Seconds = seconds
			b.TimeEstimated = false
		}
	}
	// Existing host assignments retain their admission and get an expiry based
	// on the last durable update, never a fresh full timeout on each restart.
	runs, _ := a.sdlcTree(root.RunID)
	for _, run := range runs {
		if run.Adaptive == nil {
			continue
		}
		for id, assignment := range run.Adaptive.Assignments {
			at, err := time.Parse(time.RFC3339, run.UpdatedAt)
			if err != nil {
				at = now
			}
			expires := at.Add(time.Duration(b.InvocationSeconds * float64(time.Second)))
			b.Reservations[id] = ledger.Reservation{RunID: run.RunID, Revision: assignment.Role == "implementer", ExpiresAt: expires}
			if expires.After(now) {
				b.Activities[id] = ledger.Activity{Host: true, Seen: at, Until: expires}
			}
		}
	}
	return b, nil
}

// Older invocation decisions sometimes provide complete start/end evidence.
// Merge their intervals rather than counting simultaneous workers twice.
func (a *App) historicalActiveTime(root ledger.Run) (float64, bool) {
	runs, err := a.sdlcTree(root.RunID)
	if err != nil {
		return 0, false
	}
	type interval struct{ start, end time.Time }
	var intervals []interval
	for _, run := range runs {
		decisions, err := ledger.Open(a.SDLCRunsDir(), run.RunID).ReadDecisions()
		if err != nil {
			return 0, false
		}
		starts := map[string]time.Time{}
		completed := 0
		for _, d := range decisions {
			at, err := time.Parse(time.RFC3339, d.At)
			if err != nil {
				continue
			}
			if d.Kind == "agent-selection" && d.Invocation != "" {
				starts[d.Invocation] = at
			}
			if d.Kind == "invocation-outcome" {
				if start, ok := starts[d.Invocation]; ok && !at.Before(start) {
					intervals = append(intervals, interval{start, at})
					delete(starts, d.Invocation)
					completed++
				}
			}
		}
		if len(starts) > 0 || run.Adaptive != nil && completed < run.Adaptive.AssignmentCount {
			return 0, false
		}
	}
	if len(intervals) == 0 {
		return 0, false
	}
	sort.Slice(intervals, func(i, j int) bool { return intervals[i].start.Before(intervals[j].start) })
	start, end := intervals[0].start, intervals[0].end
	seconds := 0.0
	for _, x := range intervals[1:] {
		if x.start.After(end) {
			seconds += end.Sub(start).Seconds()
			start, end = x.start, x.end
		} else if x.end.After(end) {
			end = x.end
		}
	}
	return seconds + end.Sub(start).Seconds(), true
}

func (a *App) updateBudget(run ledger.Run, p enrollment.Policy, fn func(*ledger.Budget) error) (ledger.Budget, error) {
	return a.updateOwnedBudget(run, p, "", "", fn)
}

func (a *App) updateOwnedBudget(run ledger.Run, p enrollment.Policy, id, owner string, fn func(*ledger.Budget) error) (ledger.Budget, error) {
	root, err := a.rootRun(run)
	if err != nil {
		return ledger.Budget{}, err
	}
	var result ledger.Budget
	var warnings []string
	err = ledger.Open(a.SDLCRunsDir(), root.RunID).UpdateBudget(func() (ledger.Budget, error) { return a.initialBudget(root, p) }, func(b *ledger.Budget) error {
		if id != "" {
			if activity, ok := b.Activities[id]; ok && activity.Owner == owner {
				activity.Seen = a.Clock()
				activity.Until = a.Clock().Add(15 * time.Second)
				b.Activities[id] = activity
			}
		}
		b.Tick(a.Clock())
		if err := fn(b); err != nil {
			return err
		}
		warnings = b.Warnings()
		result = *b
		return nil
	})
	if err == nil && a.Stdout != nil {
		for _, warning := range warnings {
			a.Outf("Budget warning: %s\n", warning)
		}
	}
	return result, err
}

// budgetView never writes: watch/show/status must remain observational.
func (a *App) budgetView(run ledger.Run, p enrollment.Policy) (ledger.Budget, error) {
	root, err := a.rootRun(run)
	if err != nil {
		return ledger.Budget{}, err
	}
	b, err := ledger.Open(a.SDLCRunsDir(), root.RunID).ReadBudget()
	if os.IsNotExist(err) {
		b, err = a.initialBudget(root, p)
	}
	if err == nil {
		b.Tick(a.Clock())
	}
	return b, err
}

func budgetKind(st adaptive.State) string {
	phase := st.Stage
	if st.BudgetPhase != "" {
		phase = st.BudgetPhase
	}
	switch phase {
	case adaptive.Implementing:
		return "revision"
	case adaptive.Planning, adaptive.Assessing, adaptive.Specializing:
		return "assignment"
	case "question":
		return "step"
	case "spawn":
		return "child"
	}
	return "work"
}

func (a *App) budgetGate(run *ledger.Run, p enrollment.Policy, kind string) error {
	b, err := a.updateBudget(*run, p, func(*ledger.Budget) error { return nil })
	if err != nil {
		return err
	}
	run.Adaptive.TreeBudget = true
	run.Adaptive.MaxAssignments, run.Adaptive.MaxRevisions = b.Limits.Assignments, b.Limits.Revisions
	run.Adaptive.MaxEstimatedCostUSD = b.Limits.CostUSD
	blocked := budgetBlockers(*run, b, kind)
	if len(blocked) == 0 {
		return nil
	}
	run.Adaptive.BudgetBlockers = blocked
	run.Adaptive.PendingReason = "budget exhausted: " + strings.Join(blocked, ", ")
	reason := "assignment-budget-exhausted"
	switch blocked[0] {
	case "time":
		reason = "run-time-budget-exhausted"
	case "cost-usd":
		reason = "cost-budget-exhausted"
	case "revisions":
		reason = "revision-budget-exhausted"
	case "children":
		reason = "workflow-child-budget-exhausted"
	case "steps":
		reason = "stage-step-budget-exhausted"
	}
	run.Adaptive.Pause(reason)
	if err := ledger.Open(a.SDLCRunsDir(), run.RunID).WriteRun(*run); err != nil {
		return err
	}
	return fmt.Errorf("run %s %s: budgets exhausted: %s; %s", run.RunID, run.Adaptive.Stage, strings.Join(blocked, ", "), a.budgetCommand(*run, b))
}

func budgetBlockers(run ledger.Run, b ledger.Budget, kind string) []string {
	blocked := b.Blockers(kind)
	if kind == "step" && run.StageFlow != nil && run.StageFlow.Steps >= run.StageFlow.Workflow.MaxSteps+b.WorkflowSteps[run.RunID] {
		for _, key := range blocked {
			if key == "steps" {
				return blocked
			}
		}
		blocked = append(blocked, "steps")
	}
	return blocked
}

func (a *App) reserveAssignment(run *ledger.Run, p enrollment.Policy, assignment adaptive.Assignment, fanout bool) error {
	kind := "assignment"
	if assignment.Role == "implementer" {
		kind = "revision"
	}
	if fanout {
		kind = "fanout"
	}
	b, err := a.updateBudget(*run, p, func(b *ledger.Budget) error {
		if err := b.Reserve(assignment.InvocationID, run.RunID, kind); err != nil {
			return err
		}
		expires := a.Clock().Add(time.Duration(b.InvocationSeconds * float64(time.Second)))
		reservation := b.Reservations[assignment.InvocationID]
		if reservation.ExpiresAt.IsZero() {
			reservation.ExpiresAt = expires
			b.Reservations[assignment.InvocationID] = reservation
		}
		if _, exists := b.Activities[assignment.InvocationID]; !exists && !reservation.Completed {
			b.Activities[assignment.InvocationID] = ledger.Activity{Host: true, Seen: a.Clock(), Until: expires}
		}
		return nil
	})
	if err == nil {
		syncTreeUsage(run, b)
	}
	return err
}

func syncTreeUsage(run *ledger.Run, b ledger.Budget) {
	if run.ParentRunID == "" {
		run.TreeUsage = &ledger.TreeUsage{Assignments: b.Usage.Assignments, Revisions: b.Usage.Revisions, ChildRuns: b.Usage.Children, StageSteps: b.Usage.Steps, EstimatedCostUSD: b.Usage.CostUSD}
	}
}

func (a *App) completeBudget(run *ledger.Run, id string, changed bool, cost float64) error {
	b, err := a.updateBudget(*run, mustPolicy(a), func(b *ledger.Budget) error { return b.Complete(id, changed, cost) })
	if err == nil {
		syncTreeUsage(run, b)
	}
	return err
}

// A driver owns a short heartbeat lease; a crashed driver cannot charge all
// subsequent offline time. Independent invocations share the same clock union.
func (a *App) budgetActivity(ctx context.Context, run ledger.Run, id string) (func(), error) {
	p := mustPolicy(a)
	owner := fmt.Sprintf("%d/%d", os.Getpid(), time.Now().UnixNano())
	beat := func() error {
		_, err := a.updateOwnedBudget(run, p, id, owner, func(b *ledger.Budget) error {
			if reservation, ok := b.Reservations[id]; ok && reservation.Completed {
				return nil
			}
			if activity, exists := b.Activities[id]; exists && !activity.Host && activity.Owner != owner && a.Clock().Before(activity.Until) {
				return fmt.Errorf("activity %s is owned by another driver", id)
			}
			b.Activities[id] = ledger.Activity{Owner: owner, Seen: a.Clock(), Until: a.Clock().Add(15 * time.Second)}
			return nil
		})
		return err
	}
	if err := beat(); err != nil {
		return nil, err
	}
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				_ = beat()
			}
		}
	}()
	return func() {
		close(done)
		<-stopped
		_, _ = a.updateOwnedBudget(run, p, id, owner, func(b *ledger.Budget) error {
			if activity := b.Activities[id]; activity.Owner == owner {
				delete(b.Activities, id)
			}
			return nil
		})
	}, nil
}

func (a *App) invocationTimeout(run ledger.Run, p enrollment.Policy) time.Duration {
	b, err := a.budgetView(run, p)
	if err == nil && b.InvocationSeconds > 0 {
		return time.Duration(b.InvocationSeconds * float64(time.Second))
	}
	return time.Duration(p.MaxInvocationSeconds) * time.Second
}

func extensionSuggestion(b ledger.Budget, blocked []string) ledger.Allowances {
	var add ledger.Allowances
	units := func(original, limit, used, unit float64) float64 {
		return math.Max(math.Ceil(original*.25/unit)*unit, math.Ceil((used-limit+unit)/unit)*unit)
	}
	for _, key := range blocked {
		switch key {
		case "assignments":
			add.Assignments = int(units(float64(b.Original.Assignments), float64(b.Limits.Assignments), float64(b.Usage.Assignments), 1))
		case "revisions":
			add.Revisions = int(units(float64(b.Original.Revisions), float64(b.Limits.Revisions), float64(b.Usage.Revisions+b.ReservedRevisions()), 1))
		case "children":
			add.Children = int(units(float64(b.Original.Children), float64(b.Limits.Children), float64(b.Usage.Children), 1))
		case "steps":
			add.Steps = int(units(float64(b.Original.Steps), float64(b.Limits.Steps), float64(b.Usage.Steps), 1))
		case "time":
			add.Seconds = units(b.Original.Seconds, b.Limits.Seconds, b.Usage.Seconds, 60)
		case "cost-usd":
			add.CostUSD = units(b.Original.CostUSD, b.Limits.CostUSD, b.Usage.CostUSD, .01)
		}
	}
	return add
}

func extensionFlags(add ledger.Allowances) string {
	var flags []string
	if add.Assignments > 0 {
		flags = append(flags, fmt.Sprintf("--add-assignments %d", add.Assignments))
	}
	if add.Revisions > 0 {
		flags = append(flags, fmt.Sprintf("--add-revisions %d", add.Revisions))
	}
	if add.Seconds > 0 {
		flags = append(flags, fmt.Sprintf("--add-time %dm", int(math.Ceil(add.Seconds/60))))
	}
	if add.CostUSD > 0 {
		flags = append(flags, fmt.Sprintf("--add-cost-usd %.2f", add.CostUSD))
	}
	if add.Steps > 0 {
		flags = append(flags, fmt.Sprintf("--add-steps %d", add.Steps))
	}
	if add.Children > 0 {
		flags = append(flags, fmt.Sprintf("--add-children %d", add.Children))
	}
	return strings.Join(flags, " ")
}

func (a *App) budgetRecoveryRun(run ledger.Run) ledger.Run {
	for depth := 0; depth <= sdlcMaxChildDepth; depth++ {
		id := run.AutoChildRunID
		if run.StageFlow != nil && run.StageFlow.ChildRunID != "" {
			id = run.StageFlow.ChildRunID
		}
		if id == "" {
			break
		}
		child, err := ledger.Open(a.SDLCRunsDir(), id).ReadRun()
		if err != nil || child.Adaptive == nil || !adaptive.BudgetPause(child.Adaptive.Outcome) {
			break
		}
		run = child
	}
	return run
}

func recoveryBudgetKind(run ledger.Run) string {
	kind := budgetKind(*run.Adaptive)
	if run.Fanout != nil && adaptive.BudgetPause(run.Fanout.PauseReason) {
		kind = "fanout"
	}
	if run.StageFlow != nil && run.StageFlow.PendingAnswer != "" {
		kind = "step"
	}
	// Legacy records may lack their phase; the known limit still yields a command.
	if run.Adaptive.BudgetPhase == "" && adaptive.BudgetPause(run.Adaptive.Outcome) {
		switch run.Adaptive.Outcome {
		case "assignment-budget-exhausted":
			kind = "assignment"
		case "revision-budget-exhausted":
			kind = "revision"
		case "workflow-child-budget-exhausted":
			kind = "child"
		case "stage-step-budget-exhausted":
			kind = "step"
		case "fanout-budget-exhausted":
			kind = "fanout"
		}
	}
	return kind
}

func runExtensionSuggestion(run ledger.Run, b ledger.Budget) ledger.Allowances {
	blocked := budgetBlockers(run, b, recoveryBudgetKind(run))
	add := extensionSuggestion(b, blocked)
	if run.StageFlow != nil {
		for _, key := range blocked {
			if key == "steps" {
				local := run.StageFlow.Workflow.MaxSteps
				suggestion := max((local+3)/4, run.StageFlow.Steps-local-b.WorkflowSteps[run.RunID]+1)
				add.Steps = max(add.Steps, suggestion)
			}
		}
	}
	return add
}

func (a *App) budgetCommand(run ledger.Run, b ledger.Budget) string {
	run = a.budgetRecoveryRun(run)
	return strings.TrimSpace("jevkit sdlc resume " + run.RunID + " " + extensionFlags(runExtensionSuggestion(run, b)))
}
