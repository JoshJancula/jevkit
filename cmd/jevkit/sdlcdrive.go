package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/OWNER/jevkit/internal/sdlc/adaptive"
	"github.com/OWNER/jevkit/internal/sdlc/enrollment"
	"github.com/OWNER/jevkit/internal/sdlc/ledger"
	"github.com/OWNER/jevkit/internal/sdlc/worker"
)

func (a *App) sdlcDriveCmd() *cobra.Command {
	var untilDone bool
	c := &cobra.Command{Use: "drive <run-id>", Hidden: true, Short: "ask workflow questions and launch enrolled CLI agents", Args: cobra.ExactArgs(1),
		Long: `Drive continues a saved run. Use sdlc run to start and execute a new
task in one command. For a custom stage
workflow, it asks the current Jev question and follows its answer route. At a
work stage, it chooses an eligible enrolled agent, launches its CLI runtime,
and records the plan, workspace diff, or assessment.

One call runs one step. --until-done repeats until the run completes or pauses.
Host integrations can use next and report to execute native agents.`,
		Example: "  jevkit sdlc drive RUN_ID\n  jevkit sdlc drive RUN_ID --until-done",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !untilDone {
				return a.sdlcDrive(cmd.Context(), args[0])
			}
			return a.sdlcDriveUntilDone(cmd.Context(), args[0])
		}}
	c.Flags().BoolVar(&untilDone, "until-done", false, "continue until the run completes or pauses")
	return c
}

func (a *App) sdlcDriveUntilDone(ctx context.Context, runID string) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		run, err := ledger.Open(a.sdlcRunsDir(), runID).ReadRun()
		if err != nil {
			return failf("read run: %v", err)
		}
		if run.Adaptive == nil {
			return failf("run %s has an unsupported run format", runID)
		}
		if run.Adaptive.Role() == "" && run.Adaptive.Stage != "question" && run.Adaptive.Stage != "spawn" {
			a.outf("run %s: %s", runID, run.Adaptive.Stage)
			if run.Adaptive.Outcome != "" {
				a.outf(" (%s)", run.Adaptive.Outcome)
			}
			a.outf("\n")
			return nil
		}
		if err := a.sdlcDrive(ctx, runID); err != nil {
			return err
		}
	}
}

func (a *App) pauseDriveError(runID string, cause error) error {
	store := ledger.Open(a.sdlcRunsDir(), runID)
	return store.WithRunLock(func() error {
		run, err := store.ReadRun()
		if err != nil {
			return err
		}
		if run.Adaptive == nil || run.Adaptive.Stage == adaptive.Paused || run.Adaptive.Stage == adaptive.Done {
			return nil
		}
		st := *run.Adaptive
		role := st.Role()
		next := "inspect run logs; jevkit sdlc resume " + runID + " --retry-failed"
		if role == "" {
			role = "driver"
			next = "inspect run logs and start a new run after fixing the error"
		}
		st.PendingReason = cause.Error()
		st.Pause(role + "-failed")
		run.Adaptive = &st
		run.UpdatedAt = a.now().UTC().Format(time.RFC3339)
		if err := store.WriteRun(run); err != nil {
			return err
		}
		return a.recordDecision(store, ledger.Decision{RunID: runID, Kind: "stage-transition", Stage: role,
			Trigger: "driver error", Choice: adaptive.Paused, Outcome: st.Outcome, Detail: cause.Error(),
			Next: next})
	})
}

func (a *App) sdlcDrive(ctx context.Context, runID string) (driveErr error) {
	ctx = withUsageRun(ctx, runID)
	defer a.progressFlush()
	if !sdlcRunIDRE.MatchString(runID) {
		return usagef("invalid run ID")
	}
	defer func() {
		if driveErr != nil && ctx.Err() == nil {
			_ = a.pauseDriveError(runID, driveErr)
		}
	}()
	store := ledger.Open(a.sdlcRunsDir(), runID)
	run, err := store.ReadRun()
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
	if paused, err := a.sdlcPlanApprovalGate(runID); err != nil {
		return err
	} else if paused {
		return nil
	}
	if handled, err := a.retryActiveSpecialistDecision(ctx, runID); err != nil {
		return err
	} else if handled {
		run, err = store.ReadRun()
		if err != nil {
			return err
		}
	}
	if paused, err := a.sdlcPlanApprovalGate(runID); err != nil {
		return err
	} else if paused {
		return nil
	}
	if run.StageFlow != nil && run.Adaptive.Stage == "question" {
		return a.sdlcDriveQuestion(ctx, runID, store)
	}
	if run.StageFlow != nil && run.Adaptive.Stage == "spawn" {
		return a.sdlcDriveSpawn(ctx, runID, store)
	}
	if run.Adaptive.Role() == "" {
		a.outf("run %s: %s (%s)\n", runID, run.Adaptive.Stage, run.Adaptive.Outcome)
		return nil
	}
	if handled, err := a.sdlcMaybeDelegate(ctx, run); handled || err != nil {
		return err
	}
	if len(run.Adaptive.Assignments) > 0 {
		return failf("run %s has pending assignments; report them before driving another action", runID)
	}
	assignment, err := a.sdlcAssignNext(ctx, runID)
	if err != nil {
		return err
	}
	if assignment == nil {
		return nil
	}
	a.progressFlush()
	policy, roster, err := a.sdlcEnrollment()
	if err != nil {
		return failf("%v", err)
	}
	remaining, err := a.treeRemaining(run, policy)
	if err != nil {
		return a.sdlcFailAssignment(runID, *assignment, err)
	}
	if remaining <= 0 {
		return a.sdlcTimeoutAssignment(runID, *assignment, "run-time-exhausted")
	}
	var agent *enrollment.Agent
	for i := range roster.Agents {
		if roster.Agents[i].ID == assignment.AgentID {
			agent = &roster.Agents[i]
			break
		}
	}
	if agent == nil {
		return a.sdlcFailAssignment(runID, *assignment, fmt.Errorf("agent was removed from roster"))
	}
	if a.sdlcProgress != nil {
		a.outf("  live agent output: jevkit sdlc logs %s --follow --invocation %s\n", runID, assignment.InvocationID)
	}
	a.outf("  %s: %s", assignment.Role, agent.ID)
	if agent.Via == enrollment.Runtime {
		a.outf(" via %s", agent.Runtime)
	}
	a.outf("\n")
	if agent.Via != enrollment.Runtime && a.SdlcExecutor == nil {
		return a.sdlcFailAssignment(runID, *assignment, fmt.Errorf("native and host-self assignments need a host executor"))
	}
	req := worker.Request{Agent: *agent, Assignment: *assignment, Task: run.Task, WorkDir: a.WorkDir, Workspace: a.WorkDir, AllowRead: run.AllowRead, Yolo: a.Yolo || truthy(a.getenv("JEVKIT_YOLO")), SecurityPolicy: a.SecurityPolicy, LogDir: store.Dir + "/logs"}
	if a.sdlcProgress != nil {
		live, err := a.sdlcLiveOutput(agent.ID)
		if err != nil {
			return a.sdlcFailAssignment(runID, *assignment, err)
		}
		req.LiveOutput = live
	}
	if agent.Via == enrollment.Runtime {
		strategy, sessionID, err := a.chooseSession(ctx, store, run, *assignment, policy.SessionStrategy)
		if err != nil {
			return a.sdlcFailAssignment(runID, *assignment, err)
		}
		if strategy == "compact" && agent.Runtime == "codex" {
			var compactErr error
			compactErr = worker.CompactCodex(ctx, agent.Binary, a.WorkDir, sessionID)
			if err := compactErr; err != nil {
				_ = a.recordDecision(store, ledger.Decision{RunID: runID, Kind: "session-compaction", Stage: run.Adaptive.Stage, Invocation: assignment.InvocationID, Choice: "failed", Outcome: err.Error(), Next: "pause run"})
				return a.sdlcFailAssignment(runID, *assignment, err)
			}
			_ = a.recordDecision(store, ledger.Decision{RunID: runID, Kind: "session-compaction", Stage: run.Adaptive.Stage, Invocation: assignment.InvocationID, Choice: "completed", Next: "resume compacted session"})
		}
		req.SessionID, req.CaptureSession = sessionID, true
		req.Compact = strategy == "compact" && agent.Runtime == "claude"
	}
	if assignment.Objective != "" {
		req.Task += "\n\nStage objective: " + assignment.Objective
	}
	if assignment.Role == "planner" && run.PlanFeedback != "" {
		previous, err := store.ReadArtifact("plan.md")
		if err != nil {
			return a.sdlcFailAssignment(runID, *assignment, fmt.Errorf("read previous plan: %w", err))
		}
		req.Plan = string(previous)
		req.Task += "\n\nHuman feedback on the previous plan: " + run.PlanFeedback + "\nRevise the plan to address this feedback before implementation."
	}
	if assignment.Role == "implementer" {
		if run.ReviewRecovery != nil && run.ReviewRecovery.Applied && run.ReviewRecovery.Outcome == "changes-required" {
			req.Task += "\n\nLast assessment (" + run.ReviewRecovery.Invocation + "):\n" + run.ReviewRecovery.Content
			if len(run.ReviewRecovery.Paths) > 0 {
				req.Task += "\nPaths observed changing during assessment (editor unknown):\n" + strings.Join(run.ReviewRecovery.Paths, "\n")
			}
		}
		if advice, err := store.ReadArtifact("research-advice.md"); err == nil {
			req.Task += "\n\nResearch advice:\n" + string(advice)
		}
	}
	if assignment.Role != "planner" {
		plan, err := store.ReadArtifact("plan.md")
		if assignment.Role == "implementer" && run.RequirePlanApproval {
			if err != nil || fmt.Sprintf("%x", sha256.Sum256(plan)) != run.ApprovedPlanRevision || run.ApprovedPlanRevision != run.Adaptive.PlanRevision {
				return a.sdlcFailAssignment(runID, *assignment, fmt.Errorf("approved plan.md changed before implementation"))
			}
		}
		if err != nil && assignment.Role == "assessor" {
			return a.sdlcFailAssignment(runID, *assignment, fmt.Errorf("read plan: %w", err))
		}
		if err == nil {
			req.Plan = string(plan)
		}
	}
	if assignment.Role == "assessor" || assignment.Role == "qa" || assignment.Role == "security" || assignment.Role == "code-review" {
		if assignment.Role == "assessor" && run.ReviewRecovery != nil && run.ReviewRecovery.Applied && len(run.ReviewRecovery.Paths) > 0 {
			req.Task += "\n\nWorkspace paths observed changing during the previous assessment (editor unknown). Inspect the current workspace before approving:\n" + strings.Join(run.ReviewRecovery.Paths, "\n")
		}
		diff, err := store.ReadArtifact("patch.diff")
		if err != nil {
			return a.sdlcFailAssignment(runID, *assignment, fmt.Errorf("read diff: %w", err))
		}
		if err := validateReviewArtifact(diff); err != nil {
			return a.sdlcPauseUnsafeReview(runID, *assignment, err)
		}
		req.Diff = string(diff)
		req.DiffPath = store.Dir + "/artifacts/patch.diff"
	}
	executor := a.SdlcExecutor
	if executor == nil {
		executor = worker.CLIExecutor{}
	}
	timeout := time.Duration(policy.MaxInvocationSeconds) * time.Second
	timeoutOutcome := "timed-out"
	if remaining < timeout {
		timeout, timeoutOutcome = remaining, "run-time-exhausted"
	}
	stepCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	reply, execErr := executor.Execute(stepCtx, req)
	if execErr != nil {
		if reply.InputTokens != nil || reply.OutputTokens != nil || reply.CostReported {
			if err := a.saveInvocationUsage(store, *assignment, agent.Model, reply); err != nil {
				return a.sdlcFailAssignment(runID, *assignment, err)
			}
		}
		if req.Compact {
			_ = a.recordDecision(store, ledger.Decision{RunID: runID, Kind: "session-compaction", Invocation: assignment.InvocationID, Runtime: "claude", Choice: "failed", Outcome: execErr.Error(), Next: "pause run"})
			return a.sdlcFailAssignment(runID, *assignment, execErr)
		}
		if errors.Is(execErr, context.DeadlineExceeded) {
			return a.sdlcTimeoutAssignment(runID, *assignment, timeoutOutcome)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		var invocationFailure *worker.InvocationFailure
		if !errors.As(execErr, &invocationFailure) {
			return a.sdlcFailAssignment(runID, *assignment, execErr)
		}
		a.outf("  %s invocation failed: %v; checking other %s agents\n", agent.ID, execErr, assignment.Role)
		return a.sdlcRecordResult(runID, adaptive.Result{InvocationID: assignment.InvocationID, AgentID: assignment.AgentID, Outcome: "invocation-failed", Revision: assignment.Revision, Reason: execErr.Error()}, "", nil)
	}
	if req.Compact {
		if !reply.CompactCompleted {
			err := fmt.Errorf("Claude /compact did not confirm completion")
			_ = a.recordDecision(store, ledger.Decision{RunID: runID, Kind: "session-compaction", Invocation: assignment.InvocationID, Runtime: "claude", Choice: "failed", Outcome: err.Error(), Next: "pause run"})
			return a.sdlcFailAssignment(runID, *assignment, err)
		}
		_ = a.recordDecision(store, ledger.Decision{RunID: runID, Kind: "session-compaction", Invocation: assignment.InvocationID, Runtime: "claude", Choice: "completed", Next: "continue agent work"})
	}
	if reply.ReportedOutcome != "" && reply.ReportedOutcome != reply.Outcome {
		_ = a.recordDecision(store, ledger.Decision{RunID: runID, Kind: "reply-normalization", Stage: run.Adaptive.Stage, Invocation: assignment.InvocationID, Runtime: agent.Runtime, Trigger: reply.ReportedOutcome, Choice: reply.Outcome, Detail: "accepted an unambiguous role-prefixed outcome", Next: "apply agent result"})
	}
	if assignment.Role == "assessor" && len(reply.WorkspaceDrift) > 0 {
		if err := a.saveReviewRecovery(store, *assignment, agent.Model, reply); err != nil {
			return a.sdlcFailAssignment(runID, *assignment, err)
		}
	}
	if err := a.saveInvocationUsage(store, *assignment, agent.Model, reply); err != nil {
		return a.sdlcFailAssignment(runID, *assignment, err)
	}
	if assignment.Role == "assessor" && len(reply.WorkspaceDrift) > 0 {
		return a.finishReviewRecovery(runID, *assignment, reply)
	}
	result := adaptive.Result{InvocationID: assignment.InvocationID, AgentID: assignment.AgentID, Outcome: reply.Outcome, CostUSD: reply.CostUSD, Focus: reply.Focus, Reason: reply.Reason}
	artifactName := ""
	var artifact []byte
	if reply.Outcome == "planned" || reply.Outcome == "changed" {
		artifact = []byte(reply.Content)
		if len(artifact) == 0 {
			return a.sdlcFailAssignment(runID, *assignment, fmt.Errorf("empty %s artifact", reply.Outcome))
		}
		if reply.Outcome == "changed" && run.Adaptive.DiffRevision != "" {
			if prior, err := store.ReadArtifact("patch.diff"); err == nil && len(prior) > 0 {
				const maxCumulativeReport = 256 * 1024
				if len(prior)+len(artifact) <= maxCumulativeReport {
					artifact = append(append(prior, []byte("\n\nNext implementation invocation "+assignment.InvocationID+":\n")...), artifact...)
				} else {
					archive := "prior-patch-" + run.Adaptive.DiffRevision + ".diff"
					if err := store.WriteArtifact(archive, prior); err != nil {
						return a.sdlcFailAssignment(runID, *assignment, err)
					}
					artifact = append([]byte("Previous change report: "+store.Dir+"/artifacts/"+archive+"\nPrevious revision: "+run.Adaptive.DiffRevision+"\n\n"), artifact...)
				}
			}
		}
		result.Revision = fmt.Sprintf("%x", sha256.Sum256(artifact))
		if reply.Outcome == "planned" {
			artifactName = "plan.md"
		} else {
			artifactName = "patch.diff"
		}
	} else if assignment.Role == "assessor" || assignment.Role == "research" || assignment.Role == "qa" || assignment.Role == "security" || assignment.Role == "code-review" {
		result.Revision = assignment.Revision
	}
	if artifactName == "" && reply.Content != "" {
		artifactName = "responses/" + assignment.InvocationID + ".txt"
		if assignment.Role == "research" && reply.Outcome == "advice" {
			artifactName = "research-advice.md"
		}
		artifact = []byte(reply.Content)
	}
	if err := a.sdlcRecordResult(runID, result, artifactName, artifact); err != nil {
		return a.sdlcFailAssignment(runID, *assignment, err)
	}
	return nil
}

func validateReviewArtifact(diff []byte) error {
	if len(diff) > 512*1024 || bytes.Contains(diff, []byte("\nGIT binary patch\n")) {
		return fmt.Errorf("saved review artifact is a legacy whole-workspace or binary patch (%d bytes); it cannot identify this run's changes safely; start a new SDLC run", len(diff))
	}
	return nil
}

func (a *App) sdlcPauseUnsafeReview(runID string, assignment adaptive.Assignment, cause error) error {
	store := ledger.Open(a.sdlcRunsDir(), runID)
	err := store.WithRunLock(func() error {
		run, err := store.ReadRun()
		if err != nil {
			return err
		}
		if run.Adaptive == nil {
			return fmt.Errorf("run has no adaptive state")
		}
		st := *run.Adaptive
		st.PendingReason = cause.Error()
		st.Pause("unsafe-review-artifact")
		run.Adaptive = &st
		run.UpdatedAt = a.now().UTC().Format(time.RFC3339)
		if err := store.WriteRun(run); err != nil {
			return err
		}
		_ = store.AppendEvent(ledger.Event{At: run.UpdatedAt, RunID: runID, Stage: adaptive.Paused, Outcome: st.Outcome, Agent: assignment.AgentID, Runtime: assignment.Runtime, Invocation: assignment.InvocationID, Reason: cause.Error()})
		_ = a.recordDecision(store, ledger.Decision{RunID: runID, Kind: "stage-transition", Stage: adaptive.Assessing, Invocation: assignment.InvocationID, Trigger: "unsafe review artifact", Choice: adaptive.Paused, Outcome: st.Outcome, Detail: cause.Error(), Next: "start a new SDLC run"})
		return nil
	})
	if err != nil {
		return err
	}
	return failf("run %s paused: %v", runID, cause)
}

func (a *App) sdlcTimeoutAssignment(runID string, assignment adaptive.Assignment, outcome string) error {
	result := adaptive.Result{InvocationID: assignment.InvocationID, AgentID: assignment.AgentID, Outcome: outcome, Revision: assignment.Revision}
	if err := a.sdlcRecordResult(runID, result, "", nil); err != nil {
		return err
	}
	return failf("run %s paused: %s", runID, map[string]string{"timed-out": "invocation-timeout", "run-time-exhausted": "run-time-budget-exhausted"}[outcome])
}

func (a *App) sdlcFailAssignment(runID string, assignment adaptive.Assignment, cause error) error {
	store := ledger.Open(a.sdlcRunsDir(), runID)
	if err := store.WithRunLock(func() error {
		run, err := store.ReadRun()
		if err != nil {
			return err
		}
		if run.Adaptive == nil {
			return fmt.Errorf("run has no adaptive state")
		}
		st := *run.Adaptive
		st.PendingReason = cause.Error()
		st.Pause(assignment.Role + "-failed")
		run.Adaptive = &st
		run.UpdatedAt = a.now().UTC().Format(time.RFC3339)
		if err := store.WriteRun(run); err != nil {
			return err
		}
		_ = a.recordDecision(store, ledger.Decision{RunID: runID, Kind: "stage-transition", Stage: assignment.Role,
			Invocation: assignment.InvocationID, Runtime: assignment.Runtime, Trigger: "invocation error", Choice: adaptive.Paused,
			Outcome: st.Outcome, Detail: cause.Error(), Next: "fix the invocation error; jevkit sdlc resume " + runID + " --retry-failed"})
		return nil
	}); err != nil {
		return err
	}
	return failf("run %s paused: %v; fix the invocation error, then use jevkit sdlc resume %s --retry-failed", runID, cause, runID)
}
