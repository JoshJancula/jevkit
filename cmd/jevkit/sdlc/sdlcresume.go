package sdlc

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/security/review"
)

func (a *App) sdlcResumeCmd() *cobra.Command {
	var step, silent, retryFailed, approvePlan, authorizeChecks bool
	var authorizeChecksDigest, sessionStrategy, guidance string
	var add ledger.Allowances
	var addTime, invocationTimeout time.Duration
	c := &cobra.Command{
		Use:   "resume <run-id>",
		Short: "approve a plan or continue an active run by ID",
		Long: `Resume continues an active run until it finishes or pauses. In a
terminal, a pending plan is displayed for approval or a change request. For
redirected runs, review the saved plan.md, checks.json, and subtasks.json and
use --approve-plan to approve those exact digests and continue. Under --auto,
planner-proposed commands still require --authorize-checks (or an exact digest);
--auto alone is not permission. Use --step to execute just the next question or
agent action and inspect the result.

Run starts a new task; run --step leaves an active run ID for resume. A host
integration can also leave an active run. A pending specialist decision from
an older run or required specialist policy can be retried after changing the
policy or enrolling the missing expert. An older pending delegation decision
can also be retried.
Budget pauses are recoverable on the same run tree. Explicit --add-* flags grant
additional allowance without resetting usage, artifacts or approvals. Interactive
resume offers editable amounts and a final confirmation. Plain noninteractive
resume prints the matching extension command; --auto never grants budget.
Time budgets count active work, excluding operator waits and offline periods.
Invocation timeouts are separate; retry with --retry-failed and optionally
--invocation-timeout 45m. --session-strategy fresh explicitly approves rebuilding
context from saved artifacts when a prior session cannot be reused.`,
		Example: "  jevkit sdlc resume RUN_ID --add-assignments 5 --add-revisions 1 --add-time 90m\n  jevkit sdlc resume RUN_ID --approve-plan\n  jevkit sdlc resume RUN_ID --authorize-checks\n  jevkit sdlc resume RUN_ID --guidance \"the vet failure is pre-existing; only fix the new tests\"\n  jevkit sdlc resume RUN_ID --step",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			for name, value := range map[string]float64{"add-assignments": float64(add.Assignments), "add-revisions": float64(add.Revisions), "add-steps": float64(add.Steps), "add-children": float64(add.Children), "add-cost-usd": add.CostUSD, "add-time": addTime.Seconds(), "invocation-timeout": invocationTimeout.Seconds()} {
				if cmd.Flags().Changed(name) && (value <= 0 || math.IsNaN(value) || math.IsInf(value, 0)) {
					return app.Usagef("--%s must be positive and finite", name)
				}
			}
			add.Seconds = addTime.Seconds()

			if approvePlan && retryFailed {
				return app.Usagef("--approve-plan and --retry-failed cannot be combined")
			}
			if authorizeChecks && retryFailed {
				return app.Usagef("--authorize-checks and --retry-failed cannot be combined")
			}
			if sessionStrategy != "" {
				if err := validateSessionStrategy(sessionStrategy); err != nil {
					return err
				}
			}
			if add != (ledger.Allowances{}) || invocationTimeout > 0 {
				if err := a.extendBudget(args[0], add, invocationTimeout, "operator resume flags: "+extensionFlags(add)); err != nil {
					return err
				}
			} else if a.sdlcInteractive() && !silent {
				run, err := ledger.Open(a.SDLCRunsDir(), args[0]).ReadRun()
				if err != nil {
					return err
				}
				if run.Adaptive != nil && (adaptive.BudgetPause(run.Adaptive.Outcome) || a.budgetRecoveryRun(run).RunID != run.RunID) {
					accepted, err := a.askBudgetExtension(cmd.Context(), args[0])
					if err != nil {
						return err
					}
					if !accepted {
						return nil
					}
				}
			}
			if strings.TrimSpace(guidance) != "" {
				if err := a.sdlcSetOperatorGuidance(args[0], guidance); err != nil {
					return err
				}
				// Guidance on a failure pause is an instruction to try again.
				if run, err := ledger.Open(a.SDLCRunsDir(), args[0]).ReadRun(); err == nil && run.Adaptive != nil &&
					run.Adaptive.Stage == adaptive.Paused && pauseRetryable(run.Adaptive.Outcome) && !approvePlan && !authorizeChecks && authorizeChecksDigest == "" {
					retryFailed = true
				}
			}
			if sessionStrategy != "" {
				if err := validateSessionStrategy(sessionStrategy); err != nil {
					return err
				}
				if err := a.setRunSessionStrategy(args[0], sessionStrategy); err != nil {
					return err
				}
			}
			if !silent {
				if a.sdlcInteractive() {
					driveCtx, cancel := context.WithCancel(cmd.Context())
					defer cancel()
					if approvePlan {
						if err := a.sdlcApprovePlan(args[0]); err != nil {
							return err
						}
						approvePlan = false
					}
					if authorizeChecks || authorizeChecksDigest != "" {
						if err := a.sdlcAuthorizeChecks(args[0], authorizeChecksDigest); err != nil {
							return err
						}
						authorizeChecks = false
						authorizeChecksDigest = ""
					}
					drive := func() error {
						worker := a.sdlcDashboardWorker()
						return worker.sdlcResume(driveCtx, args[0], step, retryFailed, approvePlan, false, "")
					}
					if !step {
						return a.sdlcInteractiveDrive(driveCtx, args[0], drive, false)
					}
					if targetID, _, err := a.sdlcApprovalTarget(args[0]); err != nil {
						return err
					} else if targetID != "" {
						return a.sdlcInteractiveDrive(driveCtx, args[0], drive, true)
					}
					return a.sdlcWatchDrive(driveCtx, args[0], drive, func(strategy string) error {
						return a.sdlcDashboardRetry(driveCtx, args[0], strategy)
					})
				}
				err := a.sdlcResume(cmd.Context(), args[0], step, retryFailed, approvePlan, authorizeChecks, authorizeChecksDigest)
				a.sdlcFinalSummary(a.Stdout, args[0], err)
				return err
			}
			out := a.Stdout
			a.Stdout = io.Discard
			defer func() { a.Stdout = out }()
			completed := ""
			if initial, err := ledger.Open(a.SDLCRunsDir(), args[0]).ReadRun(); err == nil && initial.Adaptive != nil {
				completed = initial.Adaptive.Stage
			}
			err := a.sdlcResume(cmd.Context(), args[0], step, retryFailed, approvePlan, authorizeChecks, authorizeChecksDigest)
			a.sdlcSilentStatus(out, args[0], step, completed)
			return err
		},
	}
	c.Flags().IntVar(&add.Assignments, "add-assignments", 0, "explicitly add assignments to this run tree")
	c.Flags().IntVar(&add.Revisions, "add-revisions", 0, "explicitly add revisions to this run tree")
	c.Flags().DurationVar(&addTime, "add-time", 0, "explicitly add active work time (for example 90m)")
	c.Flags().Float64Var(&add.CostUSD, "add-cost-usd", 0, "explicitly add estimated USD allowance; admitted work may exceed it")
	c.Flags().IntVar(&add.Steps, "add-steps", 0, "add tree transitions and the selected authored workflow's allowance")
	c.Flags().IntVar(&add.Children, "add-children", 0, "explicitly add child runs; nesting depth stays unchanged")
	c.Flags().DurationVar(&invocationTimeout, "invocation-timeout", 0, "set timeout for subsequent invocations in this tree (for example 45m)")
	c.Flags().BoolVar(&step, "step", false, "execute one question or agent action, then stop")
	c.Flags().BoolVar(&silent, "silent", false, "show only final status")
	c.Flags().BoolVar(&retryFailed, "retry-failed", false, "retry failed agent bindings or a handoff-budget pause")
	c.Flags().BoolVar(&approvePlan, "approve-plan", false, "approve the saved plan.md, checks.json, and subtasks.json digests and continue the run")
	c.Flags().BoolVar(&authorizeChecks, "authorize-checks", false, "authorize the current checks.json digest for this run; --auto alone is not permission")
	c.Flags().StringVar(&authorizeChecksDigest, "authorize-checks-digest", "", "exact checks.json digest allowance for this run")
	c.Flags().StringVar(&sessionStrategy, "session-strategy", "", "persist session policy: auto, fresh, resume or compact")
	c.Flags().StringVar(&guidance, "guidance", "", "tell the next agent how to proceed; on a failure pause this also retries")
	return c
}

func (a *App) sdlcResume(ctx context.Context, runID string, step, retryFailed, approvePlan bool, authorizeChecks bool, authorizeChecksDigest string) error {
	if !app.RunIDPattern.MatchString(runID) {
		return app.Usagef("invalid run ID")
	}
	if rec, ok := review.PendingRun(a.StateHome(), runID); ok {
		return app.Failf("run %s awaits prompt-injection review %s; use jevkit security review %s", runID, rec.ID, rec.ID)
	}
	if approvePlan {
		if retryFailed {
			return app.Usagef("--approve-plan and --retry-failed cannot be combined")
		}
		if err := a.sdlcApprovePlan(runID); err != nil {
			return err
		}
	}
	if authorizeChecks || authorizeChecksDigest != "" {
		if retryFailed {
			return app.Usagef("--authorize-checks and --retry-failed cannot be combined")
		}
		if err := a.sdlcAuthorizeChecks(runID, authorizeChecksDigest); err != nil {
			return err
		}
	}
	run, err := ledger.Open(a.SDLCRunsDir(), runID).ReadRun()
	if err != nil {
		return app.Failf("read run: %v", err)
	}
	if run.Adaptive == nil {
		return app.Failf("run %s has an unsupported run format", runID)
	}
	if run.Adaptive.Stage == adaptive.Paused && run.Adaptive.Outcome == "injection-review-required" {
		if run.Adaptive.PendingPhase == "" {
			return app.Failf("run %s cannot resume: missing prior stage", runID)
		}
		run.Adaptive.Stage = run.Adaptive.PendingPhase
		run.Adaptive.PendingPhase = ""
		run.Adaptive.Outcome = ""
		run.Adaptive.PendingReason = ""
		if err := ledger.Open(a.SDLCRunsDir(), runID).WriteRun(run); err != nil {
			return err
		}
	}
	if run.WorkDir != "" {
		old := a.WorkDir
		a.WorkDir = run.WorkDir
		defer func() { a.WorkDir = old }()
	}
	wasBudget := adaptive.BudgetPause(run.Adaptive.Outcome)
	if err := a.continueBudgetTree(runID, retryFailed); err != nil {
		return err
	}
	if wasBudget {
		retryFailed = false
	}
	run, err = ledger.Open(a.SDLCRunsDir(), runID).ReadRun()
	if err != nil {
		return err
	}
	if run.Adaptive.Stage == adaptive.Paused && (run.Adaptive.Outcome == "invocation-timeout" || run.Adaptive.Outcome == "session-recovery-required") {
		if run.Adaptive.Outcome == "session-recovery-required" && run.SessionStrategy != "fresh" && run.SessionStrategy != "compact" {
			return app.Failf("approve artifact context recovery: jevkit sdlc resume %s --session-strategy fresh", runID)
		}
		if run.Adaptive.Outcome == "invocation-timeout" && !retryFailed {
			return app.Failf("invocation timed out; jevkit sdlc resume %s --retry-failed --invocation-timeout 45m", runID)
		}
		phase := run.Adaptive.RetryPhase
		if phase == "" {
			phase, err = recoverBudgetPhase(run, retryFailed)
			if err != nil {
				return err
			}
		}
		if run.Adaptive.Outcome == "invocation-timeout" && run.Fanout != nil {
			for id, sub := range run.Fanout.Subtasks {
				if sub.Status == adaptive.SubtaskTimedOut {
					data, _ := json.MarshalIndent(sub, "", "  ")
					if err := ledger.Open(a.SDLCRunsDir(), runID).WriteArtifact(fmt.Sprintf("fanout/%s-attempt-%d.json", id, sub.Attempt), data); err != nil {
						return err
					}
					sub.Status = adaptive.SubtaskPending
					sub.Assignment = nil
					sub.Result = nil
					sub.Usage = nil
					sub.PauseReason = ""
					run.Fanout.Subtasks[id] = sub
				}
			}
			run.Fanout.PauseReason = ""
		}
		run.Adaptive.Stage, run.Adaptive.Outcome, run.Adaptive.RetryPhase = phase, "", ""
		if err := ledger.Open(a.SDLCRunsDir(), runID).WriteRun(run); err != nil {
			return err
		}
		retryFailed = false
	}
	if err := a.recoverReview(ctx, runID); err != nil {
		return err
	}
	run, err = ledger.Open(a.SDLCRunsDir(), runID).ReadRun()
	if err != nil {
		return err
	}
	a.sdlcProgress = &sdlcProgress{root: runID, out: a.Stdout, seen: map[string]int{}, last: map[string]string{}}
	defer func() { a.sdlcProgress = nil }()
	a.progressFlush()
	a.SuppressOutput = true
	defer func() { a.SuppressOutput = false }()
	if run.Adaptive.Stage == adaptive.Paused {
		if run.Adaptive.Outcome == "plan-approval-required" && !approvePlan {
			return app.Failf("run %s is awaiting plan approval; review plan.md, checks.json, and subtasks.json, then use jevkit sdlc resume %s --approve-plan", runID, runID)
		}
		if run.Adaptive.Outcome == "child-plan-approval-required" && !approvePlan {
			return app.Failf("run %s is awaiting a child plan approval; review the child plan, then use jevkit sdlc resume %s --approve-plan", runID, runID)
		}
		if run.Adaptive.Outcome == "command-authorization-required" && !authorizeChecks && authorizeChecksDigest == "" {
			return app.Failf("run %s is awaiting command authorization; review checks.json, then use jevkit sdlc resume %s --authorize-checks", runID, runID)
		}
		if retryFailed && run.Adaptive.Outcome == adaptive.OutcomeVerificationEnvironment {
			// The candidate is unchanged; re-run the supervisor checks without
			// spending an implementer revision.
			store := ledger.Open(a.SDLCRunsDir(), runID)
			run.Adaptive.Stage = adaptive.Verifying
			run.Adaptive.Outcome, run.Adaptive.PendingReason = "", ""
			if err := store.WriteRun(run); err != nil {
				return err
			}
			_ = a.recordDecision(store, ledger.Decision{RunID: runID, Kind: "retry", Stage: adaptive.Verifying, Choice: "retry verification", Next: "re-run authorized checks"})
		} else if retryFailed {
			pauseOutcome := run.Adaptive.Outcome
			role := failedPauseRole(run.Adaptive.Outcome)
			if pauseOutcome == "handoff-budget-exhausted" && run.Adaptive.PendingPhase == "" {
				return app.Failf("run %s cannot retry handoff: missing prior stage", runID)
			}
			if run.Adaptive.Outcome == "review-workspace-drift" || run.Adaptive.Outcome == "review-recovery-invalid" {
				role = "assessor"
			}
			if role == "" && pauseOutcome != "handoff-budget-exhausted" {
				return app.Usagef("--retry-failed requires a run paused after an agent failure")
			}
			store := ledger.Open(a.SDLCRunsDir(), runID)
			if pauseOutcome == "handoff-budget-exhausted" {
				run.Adaptive.Stage = run.Adaptive.PendingPhase
				run.Adaptive.PendingPhase = ""
			} else {
				run.Adaptive.Stage = map[string]string{"planner": adaptive.Planning, "implementer": adaptive.Implementing, "assessor": adaptive.Assessing}[role]
			}
			run.Adaptive.Outcome, run.Adaptive.PendingReason = "", ""
			run.Adaptive.Excluded = map[string]bool{}
			run.Adaptive.ExcludedBindings = map[string]bool{}
			run.Adaptive.HandoffExcluded = nil
			run.Adaptive.HandoffCount = 0
			run.Adaptive.HandoffFallbackUsed = false
			run.Adaptive.LastHandoffBinding = ""
			run.Adaptive.PendingFocus = ""
			run.Adaptive.ExcludedRuntimes = map[string]bool{}
			run.Adaptive.NoProgressCount = 0
			if pauseOutcome == "review-recovery-invalid" {
				patch, err := ledger.Open(a.SDLCRunsDir(), runID).ReadArtifact("patch.diff")
				if err != nil {
					return app.Failf("read patch.diff before reassessment: %v", err)
				}
				run.Adaptive.DiffRevision = fmt.Sprintf("%x", sha256.Sum256(patch))
				run.Adaptive.Assessments = nil
			}
			if run.ReviewRecovery != nil && (pauseOutcome == "review-workspace-drift" || pauseOutcome == "review-recovery-invalid") {
				run.ReviewRecovery.Applied = true
			}
			if err := store.WriteRun(run); err != nil {
				return err
			}
			if run.ReviewRecovery != nil && !run.ReviewRecovery.Applied {
				if err := a.recoverReview(ctx, runID); err != nil {
					return err
				}
				run, err = store.ReadRun()
				if err != nil {
					return err
				}
			}
			_ = a.recordDecision(store, ledger.Decision{RunID: runID, Kind: "retry", Stage: run.Adaptive.Stage, Choice: "retry failed bindings", Next: "reserve next assignment"})
		} else {
			if run.Adaptive.Outcome == "automatic-child-paused" && run.AutoChildRunID != "" {
				child, err := ledger.Open(a.SDLCRunsDir(), run.AutoChildRunID).ReadRun()
				if err != nil {
					return app.Failf("read automatic child: %v", err)
				}
				if child.Adaptive != nil && (child.Adaptive.Stage == adaptive.Done || child.Adaptive.Stage != adaptive.Paused) {
					run.Adaptive.Stage = adaptive.Implementing
					run.Adaptive.Outcome = ""
					if err := ledger.Open(a.SDLCRunsDir(), runID).WriteRun(run); err != nil {
						return err
					}
				}
			}
			if run.Adaptive.Stage != adaptive.Paused {
				if step {
					return a.sdlcDrive(ctx, runID)
				}
				return a.sdlcDriveUntilDone(ctx, runID)
			}
			if run.Adaptive.PendingDecision == "" {
				if run.Adaptive.Outcome == "review-workspace-drift" || run.Adaptive.Outcome == "review-recovery-invalid" {
					return app.Failf("run %s is paused (%s): %s; use jevkit sdlc resume %s --retry-failed to reassess", runID, run.Adaptive.Outcome, run.Adaptive.PendingReason, runID)
				}
				return app.Failf("run %s is paused (%s); start a new task after addressing the cause", runID, run.Adaptive.Outcome)
			}
			policy, _, err := a.sdlcEnrollment()
			if err != nil {
				return err
			}
			remaining, err := a.treeRemaining(run, policy)
			if err != nil {
				return err
			}
			if remaining <= 0 {
				run.Adaptive.Pause("run-time-budget-exhausted")
				_ = ledger.Open(a.SDLCRunsDir(), runID).WriteRun(run)
				return app.Failf("run %s paused: %s", runID, run.Adaptive.Outcome)
			}
			store := ledger.Open(a.SDLCRunsDir(), runID)
			if run.Adaptive.PendingDecision == "delegation" {
				run.Adaptive.Stage = run.Adaptive.PendingPhase
				run.Adaptive.PendingDecision, run.Adaptive.PendingPhase, run.Adaptive.Outcome = "", "", ""
				if err := store.WriteRun(run); err != nil {
					return err
				}
			} else if err := store.WithRunLock(func() error {
				fresh, err := store.ReadRun()
				if err != nil {
					return err
				}
				st := *fresh.Adaptive
				kind := st.PendingDecision
				st.Stage = st.PendingPhase
				st.Outcome = ""
				a.scheduleSpecialists(ctx, fresh, &st, kind)
				fresh.Adaptive = &st
				return store.WriteRun(fresh)
			}); err != nil {
				return app.Failf("retry specialist decision: %v", err)
			}
			run, err = store.ReadRun()
			if err != nil {
				return err
			}
			if run.Adaptive.Stage == adaptive.Paused {
				return app.Failf("run %s paused: %s", runID, run.Adaptive.Outcome)
			}
		}
	}
	if run.Adaptive.Stage == adaptive.Done && (run.StageFlow == nil || run.StageFlow.PendingAnswer == "") {
		a.Outf("run %s is already complete (%s)\n", runID, run.Adaptive.Outcome)
		return nil
	}
	if step {
		return a.sdlcDrive(ctx, runID)
	}
	return a.sdlcDriveUntilDone(ctx, runID)
}

func failedPauseRole(outcome string) string {
	for _, candidate := range []string{"planner", "implementer", "assessor"} {
		if outcome == candidate+"-failed" || strings.HasSuffix(outcome, candidate) || strings.HasPrefix(outcome, candidate+"-invocations-exhausted") {
			return candidate
		}
	}
	return ""
}

func (a *App) sdlcDashboardRetry(ctx context.Context, runID, strategy string) error {
	if err := a.setRunSessionStrategy(runID, strategy); err != nil {
		return err
	}
	run, err := ledger.Open(a.SDLCRunsDir(), runID).ReadRun()
	if err != nil {
		return err
	}
	worker := a.sdlcDashboardWorker()
	return worker.sdlcResume(ctx, runID, false, run.Adaptive != nil && pauseRetryable(run.Adaptive.Outcome), false, false, "")
}

// pauseRetryable reports whether resume --retry-failed can continue a run
// paused with this outcome.
func pauseRetryable(outcome string) bool {
	switch outcome {
	case "invocation-timeout", "review-workspace-drift", "review-recovery-invalid", "handoff-budget-exhausted", adaptive.OutcomeVerificationEnvironment:
		return true
	}
	return failedPauseRole(outcome) != ""
}

// operatorGuidanceConsumed reports whether an invocation outcome means an agent
// actually worked with the operator's guidance, so it should not be repeated.
func operatorGuidanceConsumed(outcome string) bool {
	switch outcome {
	case "invocation-failed", "auth-failed", "handoff", "failed", "timed-out", "run-time-exhausted":
		return false
	}
	return true
}

// sdlcSetOperatorGuidance stores guidance for the next agent on a run and
// records it in the decision log.
func (a *App) sdlcSetOperatorGuidance(runID, guidance string) error {
	guidance = strings.TrimSpace(guidance)
	if guidance == "" {
		return nil
	}
	if !app.RunIDPattern.MatchString(runID) {
		return app.Usagef("invalid run ID")
	}
	store := ledger.Open(a.SDLCRunsDir(), runID)
	return store.WithRunLock(func() error {
		run, err := store.ReadRun()
		if err != nil {
			return app.Failf("read run: %v", err)
		}
		if run.Adaptive != nil && run.Adaptive.Stage == adaptive.Done {
			return app.Failf("run %s is already complete; start a new run instead", runID)
		}
		run.OperatorGuidance = guidance
		if err := store.WriteRun(run); err != nil {
			return err
		}
		stage := ""
		if run.Adaptive != nil {
			stage = run.Adaptive.Stage
		}
		return a.recordDecision(store, ledger.Decision{RunID: runID, Kind: "operator-guidance", Stage: stage,
			Choice: "guidance recorded", Detail: guidance, Next: "include in the next agent prompt"})
	})
}
