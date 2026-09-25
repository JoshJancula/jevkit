package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/OWNER/jevkit/internal/sdlc/adaptive"
	"github.com/OWNER/jevkit/internal/sdlc/ledger"
)

func (a *App) sdlcResumeCmd() *cobra.Command {
	var step, silent, retryFailed, approvePlan bool
	var sessionStrategy string
	c := &cobra.Command{
		Use:   "resume <run-id>",
		Short: "approve a plan or continue an active run by ID",
		Long: `Resume continues an active run until it finishes or pauses. In a
terminal, a pending plan is displayed for approval or a change request. For
redirected runs, review the saved plan.md and use --approve-plan to approve
that exact revision and continue. Use --step
to execute just the next question or agent action and inspect the result.

Run starts a new task; run --step leaves an active run ID for resume. A host
integration can also leave an active run. A pending specialist decision from
an older run or required specialist policy can be retried after changing the
policy or enrolling the missing expert. An older pending delegation decision
can also be retried.
Runs paused by hard limits cannot continue.`,
		Example: "  jevkit sdlc resume RUN_ID --approve-plan\n  jevkit sdlc resume RUN_ID --step",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if approvePlan && retryFailed {
				return usagef("--approve-plan and --retry-failed cannot be combined")
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
					if approvePlan {
						if err := a.sdlcApprovePlan(args[0]); err != nil {
							return err
						}
						approvePlan = false
					}
					drive := func() error {
						worker := a.sdlcDashboardWorker()
						return worker.sdlcResume(cmd.Context(), args[0], step, retryFailed, approvePlan)
					}
					if !step {
						return a.sdlcInteractiveDrive(cmd.Context(), args[0], drive, false)
					}
					if targetID, _, err := a.sdlcApprovalTarget(args[0]); err != nil {
						return err
					} else if targetID != "" {
						return a.sdlcInteractiveDrive(cmd.Context(), args[0], drive, true)
					}
					return a.sdlcWatchDrive(cmd.Context(), args[0], drive, func(strategy string) error {
						return a.sdlcDashboardRetry(cmd.Context(), args[0], strategy)
					})
				}
				err := a.sdlcResume(cmd.Context(), args[0], step, retryFailed, approvePlan)
				a.sdlcFinalSummary(a.Stdout, args[0], err)
				return err
			}
			out := a.Stdout
			a.Stdout = io.Discard
			defer func() { a.Stdout = out }()
			completed := ""
			if initial, err := ledger.Open(a.sdlcRunsDir(), args[0]).ReadRun(); err == nil && initial.Adaptive != nil {
				completed = initial.Adaptive.Stage
			}
			err := a.sdlcResume(cmd.Context(), args[0], step, retryFailed, approvePlan)
			a.sdlcSilentStatus(out, args[0], step, completed)
			return err
		},
	}
	c.Flags().BoolVar(&step, "step", false, "execute one question or agent action, then stop")
	c.Flags().BoolVar(&silent, "silent", false, "show only final status")
	c.Flags().BoolVar(&retryFailed, "retry-failed", false, "retry failed agent bindings after fixing their invocation error")
	c.Flags().BoolVar(&approvePlan, "approve-plan", false, "approve the saved plan.md revision and continue the run")
	c.Flags().StringVar(&sessionStrategy, "session-strategy", "", "persist session policy: auto, fresh, resume or compact")
	return c
}

func (a *App) sdlcResume(ctx context.Context, runID string, step, retryFailed, approvePlan bool) error {
	if !sdlcRunIDRE.MatchString(runID) {
		return usagef("invalid run ID")
	}
	if approvePlan {
		if retryFailed {
			return usagef("--approve-plan and --retry-failed cannot be combined")
		}
		if err := a.sdlcApprovePlan(runID); err != nil {
			return err
		}
	}
	run, err := ledger.Open(a.sdlcRunsDir(), runID).ReadRun()
	if err != nil {
		return failf("read run: %v", err)
	}
	if run.Adaptive == nil {
		return failf("run %s has an unsupported run format", runID)
	}
	if run.WorkDir != "" {
		old := a.WorkDir
		a.WorkDir = run.WorkDir
		defer func() { a.WorkDir = old }()
	}
	if err := a.recoverReview(ctx, runID); err != nil {
		return err
	}
	run, err = ledger.Open(a.sdlcRunsDir(), runID).ReadRun()
	if err != nil {
		return err
	}
	a.sdlcProgress = &sdlcProgress{root: runID, out: a.Stdout, seen: map[string]int{}, last: map[string]string{}}
	defer func() { a.sdlcProgress = nil }()
	a.progressFlush()
	a.sdlcSuppressLegacy = true
	defer func() { a.sdlcSuppressLegacy = false }()
	if run.Adaptive.Stage == adaptive.Paused {
		if run.Adaptive.Outcome == "plan-approval-required" && !approvePlan {
			return failf("run %s is awaiting plan approval; review plan.md, then use jevkit sdlc resume %s --approve-plan", runID, runID)
		}
		if run.Adaptive.Outcome == "child-plan-approval-required" && !approvePlan {
			return failf("run %s is awaiting a child plan approval; review the child plan, then use jevkit sdlc resume %s --approve-plan", runID, runID)
		}
		if retryFailed {
			pauseOutcome := run.Adaptive.Outcome
			role := failedPauseRole(run.Adaptive.Outcome)
			if run.Adaptive.Outcome == "review-workspace-drift" || run.Adaptive.Outcome == "review-recovery-invalid" {
				role = "assessor"
			}
			if role == "" {
				return usagef("--retry-failed requires a run paused after an agent failure")
			}
			store := ledger.Open(a.sdlcRunsDir(), runID)
			run.Adaptive.Stage = map[string]string{"planner": adaptive.Planning, "implementer": adaptive.Implementing, "assessor": adaptive.Assessing}[role]
			run.Adaptive.Outcome, run.Adaptive.PendingReason = "", ""
			run.Adaptive.Excluded = map[string]bool{}
			run.Adaptive.ExcludedBindings = map[string]bool{}
			run.Adaptive.ExcludedRuntimes = map[string]bool{}
			if pauseOutcome == "review-recovery-invalid" {
				patch, err := ledger.Open(a.sdlcRunsDir(), runID).ReadArtifact("patch.diff")
				if err != nil {
					return failf("read patch.diff before reassessment: %v", err)
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
				child, err := ledger.Open(a.sdlcRunsDir(), run.AutoChildRunID).ReadRun()
				if err != nil {
					return failf("read automatic child: %v", err)
				}
				if child.Adaptive != nil && child.Adaptive.Stage == adaptive.Done {
					run.Adaptive.Stage = adaptive.Implementing
					run.Adaptive.Outcome = ""
					if err := ledger.Open(a.sdlcRunsDir(), runID).WriteRun(run); err != nil {
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
					return failf("run %s is paused (%s): %s; use jevkit sdlc resume %s --retry-failed to reassess", runID, run.Adaptive.Outcome, run.Adaptive.PendingReason, runID)
				}
				return failf("run %s is paused (%s); start a new task after addressing the cause", runID, run.Adaptive.Outcome)
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
				_ = ledger.Open(a.sdlcRunsDir(), runID).WriteRun(run)
				return failf("run %s paused: %s", runID, run.Adaptive.Outcome)
			}
			store := ledger.Open(a.sdlcRunsDir(), runID)
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
				return failf("retry specialist decision: %v", err)
			}
			run, err = store.ReadRun()
			if err != nil {
				return err
			}
			if run.Adaptive.Stage == adaptive.Paused {
				return failf("run %s paused: %s", runID, run.Adaptive.Outcome)
			}
		}
	}
	if run.Adaptive.Stage == adaptive.Done {
		a.outf("run %s is already complete (%s)\n", runID, run.Adaptive.Outcome)
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
	run, err := ledger.Open(a.sdlcRunsDir(), runID).ReadRun()
	if err != nil {
		return err
	}
	worker := a.sdlcDashboardWorker()
	return worker.sdlcResume(ctx, runID, false, run.Adaptive != nil && (failedPauseRole(run.Adaptive.Outcome) != "" || run.Adaptive.Outcome == "review-workspace-drift" || run.Adaptive.Outcome == "review-recovery-invalid"), false)
}
