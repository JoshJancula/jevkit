package sdlc

import (
	"context"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
)

func (a *App) extendBudget(id string, add ledger.Allowances, timeout time.Duration, action string) error {
	run, err := ledger.Open(a.SDLCRunsDir(), id).ReadRun()
	if err != nil {
		return err
	}
	if run.Adaptive == nil || run.Adaptive.Stage == adaptive.Done {
		return fmt.Errorf("run %s cannot be extended", id)
	}
	root, err := a.rootRun(run)
	if err != nil {
		return err
	}
	b, err := a.updateBudget(run, mustPolicy(a), func(b *ledger.Budget) error {
		if add != (ledger.Allowances{}) {
			if err := b.Extend(id, action, add, a.Clock()); err != nil {
				return err
			}
		}
		if timeout > 0 {
			b.InvocationSeconds = timeout.Seconds()
		}
		return nil
	})
	if err != nil {
		return err
	}
	a.Outf("Budget for root tree %s (selected run %s): %d assignments, %d revisions, %s active work, $%.2f, %d steps, %d children. Usage preserved.\n", root.RunID, id, b.Limits.Assignments, b.Limits.Revisions, time.Duration(b.Limits.Seconds*float64(time.Second)), b.Limits.CostUSD, b.Limits.Steps, b.Limits.Children)
	return nil
}

func recoverBudgetPhase(run ledger.Run, retry bool) (string, error) {
	s := run.Adaptive
	if s.BudgetPhase != "" {
		return s.BudgetPhase, nil
	}
	if s.PendingPhase != "" {
		return s.PendingPhase, nil
	}
	if run.StageFlow != nil {
		stage, ok := run.StageFlow.Stage()
		if ok {
			if stage.Question != nil {
				return "question", nil
			}
			if stage.Spawn != nil {
				return "spawn", nil
			}
			if stage.Work != nil && retry {
				return map[string]string{"planner": adaptive.Planning, "implementer": adaptive.Implementing, "assessor": adaptive.Assessing}[stage.Work.Role], nil
			}
		}
	}
	if s.Outcome == "revision-budget-exhausted" && s.RepairFeedback != nil {
		return adaptive.Implementing, nil
	}
	if retry {
		if s.DiffRevision != "" {
			return adaptive.Verifying, nil
		}
		if s.PlanRevision != "" {
			return adaptive.Implementing, nil
		}
		return adaptive.Planning, nil
	}
	return "", fmt.Errorf("legacy checkpoint is ambiguous; approve retrying the affected stage in this run with jevkit sdlc resume %s --retry-failed", run.RunID)
}

// Reopen only budget checkpoints whose relevant allowances are now available.
// Read each run afresh under its own lock; never nest child locks under root.
func (a *App) continueBudgetTree(id string, retry bool) error {
	selected, err := ledger.Open(a.SDLCRunsDir(), id).ReadRun()
	if err != nil {
		return err
	}
	root, err := a.rootRun(selected)
	if err != nil {
		return err
	}
	runs, err := a.sdlcTree(root.RunID)
	if err != nil {
		return err
	}
	for _, candidate := range runs {
		if candidate.Adaptive == nil || !adaptive.BudgetPause(candidate.Adaptive.Outcome) {
			continue
		}
		store := ledger.Open(a.SDLCRunsDir(), candidate.RunID)
		if err := store.WithRunLock(func() error {
			run, err := store.ReadRun()
			if err != nil {
				return err
			}
			if !adaptive.BudgetPause(run.Adaptive.Outcome) {
				return nil
			}
			phase, err := recoverBudgetPhase(run, retry)
			if err != nil {
				return err
			}
			run.Adaptive.BudgetPhase = phase
			kind := budgetKind(*run.Adaptive)
			if run.Fanout != nil && adaptive.BudgetPause(run.Fanout.PauseReason) {
				kind = "fanout"
			}
			if run.StageFlow != nil && run.StageFlow.PendingAnswer != "" {
				kind = "step"
			}
			if err := a.budgetGate(&run, mustPolicy(a), kind); err != nil {
				return err
			}
			run.Adaptive.Stage, run.Adaptive.Outcome = phase, ""
			run.Adaptive.BudgetPhase, run.Adaptive.BudgetBlockers = "", nil
			if run.Fanout != nil && adaptive.BudgetPause(run.Fanout.PauseReason) {
				run.Fanout.PauseReason = ""
			}
			if run.StageFlow != nil && run.StageFlow.PendingAnswer != "" {
				if err := a.advanceBudgetFlow(&run, run.Adaptive, run.StageFlow.PendingAnswer); err != nil {
					return err
				}
			}
			return store.WriteRun(run)
		}); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) budgetDescription(run ledger.Run) string {
	if run.Adaptive == nil {
		return ""
	}
	b, err := a.budgetView(run, mustPolicy(a))
	if err != nil {
		return "Budget unavailable: " + err.Error()
	}
	root, _ := a.rootRun(run)
	label := "active work"
	if b.TimeEstimated {
		label += " (historical usage estimated)"
	}
	text := fmt.Sprintf("Root budget %s: %d/%d assignments; %d/%d revisions (+%d reserved); %s/%s %s; $%.2f/$%.2f reported cost; %d/%d steps; %d/%d children", root.RunID, b.Usage.Assignments, b.Limits.Assignments, b.Usage.Revisions, b.Limits.Revisions, b.ReservedRevisions(), time.Duration(b.Usage.Seconds*float64(time.Second)).Round(time.Second), time.Duration(b.Limits.Seconds*float64(time.Second)), label, b.Usage.CostUSD, b.Limits.CostUSD, b.Usage.Steps, b.Limits.Steps, b.Usage.Children, b.Limits.Children)
	if adaptive.BudgetPause(run.Adaptive.Outcome) {
		phase := run.Adaptive.BudgetPhase
		if phase == "" {
			phase = "recover legacy checkpoint"
		}
		text += fmt.Sprintf("\nCompleted: %d revisions, %d reviews. Outstanding: %s, %d admitted assignments. Saved agent sessions: %d.\nBlocking budgets: %s\nExtend and continue: %s", run.Adaptive.RevisionCount, len(run.Adaptive.Assessments)+len(run.Adaptive.SpecialistReviews), phase, len(run.Adaptive.Assignments), len(run.Sessions), strings.Join(budgetBlockers(a.budgetRecoveryRun(run), b, recoveryBudgetKind(a.budgetRecoveryRun(run))), ", "), a.budgetCommand(run, b))
	}
	return text
}

func (a *App) sdlcRecoveryNext(run ledger.Run, id string) string {
	if run.Adaptive != nil {
		if adaptive.BudgetPause(run.Adaptive.Outcome) || run.Adaptive.Outcome == "automatic-child-paused" {
			run = a.budgetRecoveryRun(run)
			b, err := a.budgetView(run, mustPolicy(a))
			if err == nil {
				return a.budgetDescription(run) + "\n" + a.budgetCommand(run, b)
			}
		}
		if run.Adaptive.Outcome == "session-recovery-required" {
			return "Approve recovery from saved artifacts: jevkit sdlc resume " + id + " --session-strategy fresh"
		}
		if run.Adaptive.Outcome == "invocation-timeout" {
			return "Retry this run: jevkit sdlc resume " + id + " --retry-failed --invocation-timeout 45m"
		}
	}
	return sdlcSummaryNext(run, id)
}

// This prompt uses the existing shared input pump, including from the dashboard.
// Enter accepts an editable amount, but only an explicit final yes grants it.
func (a *App) askBudgetExtension(ctx context.Context, id string) (bool, error) {
	run, err := ledger.Open(a.SDLCRunsDir(), id).ReadRun()
	if err != nil {
		return false, err
	}
	run = a.budgetRecoveryRun(run)
	id = run.RunID
	b, err := a.budgetView(run, mustPolicy(a))
	if err != nil {
		return false, err
	}
	blocked := budgetBlockers(run, b, recoveryBudgetKind(run))
	add := runExtensionSuggestion(run, b)
	a.Outf("\r\n%s\r\nExtend and continue (Esc or q cancels).\r\n", a.budgetDescription(run))
	read := func(prompt, def string) (string, error) {
		a.Outf("%s [%s]: ", prompt, def)
		var buf []byte
		for {
			k, err := a.sdlcReadKey(ctx)
			if err != nil {
				return "", err
			}
			switch k {
			case 3, 4, sdlcKeyEscape:
				return "", io.EOF
			case '\r', '\n':
				a.Outf("\r\n")
				v := strings.TrimSpace(string(buf))
				if v == "q" {
					return "", io.EOF
				}
				if v == "" {
					v = def
				}
				return v, nil
			case 127, 8:
				if len(buf) > 0 {
					buf = buf[:len(buf)-1]
					a.Outf("\b \b")
				}
			default:
				if k >= 32 && k < 127 {
					buf = append(buf, byte(k))
					a.Outf("%c", k)
				}
			}
		}
	}
	seen := map[string]bool{}
	for _, key := range blocked {
		if seen[key] {
			continue
		}
		seen[key] = true
		var current float64
		switch key {
		case "assignments":
			current = float64(add.Assignments)
		case "revisions":
			current = float64(add.Revisions)
		case "steps":
			current = float64(add.Steps)
		case "children":
			current = float64(add.Children)
		case "time":
			current = add.Seconds / 60
		case "cost-usd":
			current = add.CostUSD
		}
		label := key
		if key == "time" {
			label = "active minutes"
		}
		value, err := read("Additional "+label, strconv.FormatFloat(current, 'f', -1, 64))
		if err != nil {
			if err == io.EOF {
				return false, nil
			}
			return false, err
		}
		v, err := strconv.ParseFloat(value, 64)
		if err != nil || v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			return false, app.Usagef("extension must be positive and finite")
		}
		if key != "time" && key != "cost-usd" && (v != math.Trunc(v) || v >= float64(math.MaxInt)) {
			return false, app.Usagef("extension must be a positive whole number")
		}
		switch key {
		case "assignments":
			add.Assignments = int(v)
		case "revisions":
			add.Revisions = int(v)
		case "steps":
			add.Steps = int(v)
		case "children":
			add.Children = int(v)
		case "time":
			add.Seconds = v * 60
		case "cost-usd":
			add.CostUSD = v
		}
	}
	preview := b
	if err := preview.Extend(id, "preview", add, a.Clock()); err != nil {
		return false, err
	}
	a.Outf("Resulting totals: %d assignments, %d revisions, %s active time, $%.2f, %d steps, %d children.\r\n", preview.Limits.Assignments, preview.Limits.Revisions, time.Duration(preview.Limits.Seconds*float64(time.Second)), preview.Limits.CostUSD, preview.Limits.Steps, preview.Limits.Children)
	answer, err := read("Extend and continue? Type yes", "no")
	if err == io.EOF || answer != "yes" {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, a.extendBudget(id, add, 0, "interactive Extend and continue")
}
