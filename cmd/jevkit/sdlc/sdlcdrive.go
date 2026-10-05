package sdlc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/keepawake"
	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/enrollment"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/sdlc/worker"
	"github.com/JoshJancula/jevkit/internal/security/review"
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
		run, err := ledger.Open(a.SDLCRunsDir(), runID).ReadRun()
		if err != nil {
			return app.Failf("read run: %v", err)
		}
		if run.Adaptive == nil {
			return app.Failf("run %s has an unsupported run format", runID)
		}
		if (run.StageFlow == nil || run.StageFlow.PendingAnswer == "") && run.Adaptive.Role() == "" && len(run.Adaptive.Assignments) == 0 && run.Adaptive.Stage != "question" && run.Adaptive.Stage != "spawn" && run.Adaptive.Stage != adaptive.Verifying {
			a.Outf("run %s: %s", runID, run.Adaptive.Stage)
			if run.Adaptive.Outcome != "" {
				a.Outf(" (%s)", run.Adaptive.Outcome)
			}
			a.Outf("\n")
			return nil
		}
		if err := a.sdlcDrive(ctx, runID); err != nil {
			return err
		}
	}
}

func (a *App) pauseDriveError(runID string, cause error) error {
	store := ledger.Open(a.SDLCRunsDir(), runID)
	return store.WithRunLock(func() error {
		run, err := store.ReadRun()
		if err != nil {
			return err
		}
		if run.Adaptive == nil || run.Adaptive.Stage == adaptive.Paused || run.Adaptive.Stage == adaptive.Draining || run.Adaptive.Stage == adaptive.Done {
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
		run.UpdatedAt = a.Clock().UTC().Format(time.RFC3339)
		if err := store.WriteRun(run); err != nil {
			return err
		}
		return a.recordDecision(store, ledger.Decision{RunID: runID, Kind: "stage-transition", Stage: role,
			Trigger: "driver error", Choice: adaptive.Paused, Outcome: st.Outcome, Detail: cause.Error(),
			Next: next})
	})
}

func (a *App) sdlcDrive(ctx context.Context, runID string) (driveErr error) {
	ctx = app.WithUsageRun(ctx, runID)
	defer a.progressFlush()
	if !app.RunIDPattern.MatchString(runID) {
		return app.Usagef("invalid run ID")
	}
	defer keepawake.Hold()()
	defer func() {
		if driveErr != nil && ctx.Err() == nil {
			_ = a.pauseDriveError(runID, driveErr)
		}
	}()
	store := ledger.Open(a.SDLCRunsDir(), runID)
	run, err := store.ReadRun()
	if err != nil {
		return app.Failf("read run: %v", err)
	}
	if run.Adaptive == nil {
		return app.Failf("run %s has an unsupported run format", runID)
	}
	if run.StageFlow != nil && run.StageFlow.PendingAnswer != "" && run.Adaptive.Stage != adaptive.Paused {
		return store.WithRunLock(func() error {
			fresh, err := store.ReadRun()
			if err != nil {
				return err
			}
			if fresh.StageFlow.PendingAnswer == "" {
				return nil
			}
			if err := a.advanceBudgetFlow(&fresh, fresh.Adaptive, fresh.StageFlow.PendingAnswer); err != nil {
				return err
			}
			return store.WriteRun(fresh)
		})
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
	if run.Adaptive.Role() == "" && len(run.Adaptive.Assignments) == 0 && run.Adaptive.Stage != adaptive.Verifying {
		a.Outf("run %s: %s (%s)\n", runID, run.Adaptive.Stage, run.Adaptive.Outcome)
		return nil
	}
	if handled, err := a.sdlcMaybeDelegate(ctx, run); handled || err != nil {
		return err
	}
	if handled, err := a.sdlcDriveFanout(ctx, runID, store, run); handled || err != nil {
		return err
	}
	if run.Adaptive.ParallelReviews() {
		return a.sdlcDriveParallelReviews(ctx, runID)
	}
	if len(run.Adaptive.Assignments) > 0 {
		return app.Failf("run %s has pending assignments; report them before driving another action", runID)
	}
	assignment, err := a.sdlcAssignNext(ctx, runID)
	if err != nil {
		return err
	}
	if assignment == nil {
		return nil
	}
	return a.sdlcExecuteAssignment(ctx, runID, *assignment)
}

func (a *App) sdlcExecuteAssignment(ctx context.Context, runID string, assignmentVal adaptive.Assignment) error {
	assignment := &assignmentVal
	store := ledger.Open(a.SDLCRunsDir(), runID)
	run, err := store.ReadRun()
	if err != nil {
		return app.Failf("read run: %v", err)
	}
	if run.Adaptive == nil {
		return app.Failf("run %s has an unsupported run format", runID)
	}
	a.progressFlush()
	policy, roster, err := a.sdlcEnrollment()
	if err != nil {
		return app.Failf("%v", err)
	}
	if err := a.sdlcQuotaCheck(runID); err != nil {
		return a.sdlcFailAssignment(runID, *assignment, err)
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
	if assignment.ToolPolicyFingerprint != agent.Tools.Fingerprint() {
		return a.sdlcFailAssignment(runID, *assignment, fmt.Errorf("agent tool settings changed after assignment; retry to select an agent with the current settings"))
	}
	if assignment.RuntimeArgsFingerprint != agent.RuntimeArgsFingerprint() {
		return a.sdlcFailAssignment(runID, *assignment, fmt.Errorf("agent runtimeArgs changed after assignment; retry to select an agent with the current settings"))
	}
	if a.sdlcProgress != nil {
		a.Outf("  live agent output: jevkit sdlc logs %s --follow --invocation %s\n", runID, assignment.InvocationID)
	}
	a.Outf("  %s: %s", assignment.Role, agent.ID)
	if agent.Via == enrollment.Runtime {
		a.Outf(" via %s", agent.Runtime)
	}
	a.Outf("\n")
	if agent.Via != enrollment.Runtime && a.SdlcExecutor == nil {
		return a.sdlcFailAssignment(runID, *assignment, fmt.Errorf("native and host-self assignments need a host executor"))
	}
	req := worker.Request{Agent: *agent, Assignment: *assignment, Task: run.Task, OriginalTask: run.Task, WorkDir: a.WorkDir, Workspace: a.WorkDir, AllowRead: run.AllowRead, Yolo: a.Yolo || app.Truthy(a.Getenv("JEVKIT_YOLO")), SecurityPolicy: a.SecurityPolicy, SDLCRunID: runID, LogDir: store.Dir + "/logs", LogTailBytes: a.sdlcLogTailBytes()}
	a.applySDLCRuntimeIntegration(&req, run)
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
			return a.pauseSessionRecovery(runID, *assignment, err)
		}
		if strategy == "compact" && agent.Runtime == "codex" {
			if err := worker.CompactCodex(ctx, agent.Binary, a.WorkDir, sessionID); err != nil {
				_ = a.recordDecision(store, ledger.Decision{RunID: runID, Kind: "session-compaction", Stage: run.Adaptive.Stage, Invocation: assignment.InvocationID, Choice: "failed", Outcome: err.Error(), Next: "pause run"})
				return a.sdlcFailAssignment(runID, *assignment, err)
			}
			_ = a.recordDecision(store, ledger.Decision{RunID: runID, Kind: "session-compaction", Stage: run.Adaptive.Stage, Invocation: assignment.InvocationID, Choice: "completed", Next: "resume compacted session"})
		}
		if strategy == "fresh" && len(run.Sessions) > 0 {
			req.Task += "\n\nContext recovered from saved run artifacts. This is a new conversation; the original agent conversation is unavailable here."
		}
		req.SessionID, req.CaptureSession = sessionID, true
		req.Compact = strategy == "compact" && agent.Runtime == "claude"
	}
	if assignment.Role == "planner" && run.PlanFeedback != "" {
		previous, err := store.ReadArtifact("plan.md")
		if err != nil {
			return a.sdlcFailAssignment(runID, *assignment, fmt.Errorf("read previous plan: %w", err))
		}
		req.Plan = string(previous)
		feedback := worker.BoundText(run.PlanFeedback, worker.MaxPlanFeedbackInlineBytes)
		req.Task += "\n\nHuman feedback on the previous plan: " + feedback + "\nRevise the plan to address this feedback before implementation."
	}
	if run.OperatorGuidance != "" {
		req.Task += "\n\nOperator guidance (from the human running this SDLC after it paused; follow it): " + worker.BoundText(run.OperatorGuidance, worker.MaxPlanFeedbackInlineBytes)
	}
	if assignment.Role == "implementer" {
		if note := sdlcRepairContext(store, *run.Adaptive); note != "" {
			req.Task += "\n\n" + note
		}
		if note := sdlcVerificationFailureNote(store, run); note != "" {
			req.Task += "\n\n" + note
		}
		if run.ReviewRecovery != nil && run.ReviewRecovery.Applied && run.ReviewRecovery.Outcome == "changes-required" && run.ReviewRecovery.Revision == run.Adaptive.DiffRevision {
			assessmentPath := store.Dir + "/artifacts/last-assessment.md"
			summary := worker.BoundVerificationSummary(run.ReviewRecovery.Content, assessmentPath, worker.MaxVerificationSummaryBytes)
			req.Task += "\n\nLast assessment (" + run.ReviewRecovery.Invocation + "):\n" + summary
			if len(run.ReviewRecovery.Paths) > 0 {
				req.Task += "\nPaths observed changing during assessment (editor unknown):\n" + strings.Join(run.ReviewRecovery.Paths, "\n")
			}
		}
		if advice, err := store.ReadArtifact("research-advice.md"); err == nil {
			req.Task += "\n\nResearch advice:\n" + worker.BoundText(string(advice), worker.MaxResearchAdviceInlineBytes)
			if len(advice) > worker.MaxResearchAdviceInlineBytes {
				req.Task += "\nFull research advice saved locally: " + store.Dir + "/artifacts/research-advice.md"
			}
		}
	}
	if assignment.Role != "planner" {
		plan, err := store.ReadArtifact("plan.md")
		if assignment.Role == "implementer" && run.RequirePlanApproval {
			if err != nil || !planApprovalComplete(run) || adaptive.DigestHex(plan) != run.ApprovedPlanRevision {
				return a.sdlcFailAssignment(runID, *assignment, fmt.Errorf("approved plan artifacts changed before implementation"))
			}
			checks, cerr := store.ReadArtifact(adaptive.ArtifactChecks)
			subtasks, serr := store.ReadArtifact(adaptive.ArtifactSubtasks)
			if cerr != nil || serr != nil ||
				adaptive.DigestHex(checks) != run.ApprovedChecksRevision ||
				adaptive.DigestHex(subtasks) != run.ApprovedSubtasksRevision {
				return a.sdlcFailAssignment(runID, *assignment, fmt.Errorf("approved plan artifacts changed before implementation"))
			}
		}
		if assignment.Role == "implementer" {
			checks, _ := store.ReadArtifact(adaptive.ArtifactChecks)
			var parsed adaptive.ChecksFile
			_ = json.Unmarshal(checks, &parsed)
			if argvChecksNeedAuthorization(parsed.Checks, run.AuthorizedChecksRevision, adaptive.DigestHex(checks)) {
				return a.sdlcFailAssignment(runID, *assignment, fmt.Errorf("planner-proposed commands are not authorized for this run"))
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
		if run.Adaptive != nil && len(run.Adaptive.CheckReceipts) > 0 {
			req.Task += "\n\n" + adaptive.FormatReceiptsForAssessor(run.Adaptive.CheckReceipts, run.Adaptive.DiffRevision)
		} else if run.Verification != nil && len(run.Verification.Receipts) > 0 {
			rev := ""
			if run.Adaptive != nil {
				rev = run.Adaptive.DiffRevision
			}
			req.Task += "\n\n" + adaptive.FormatReceiptsForAssessor(run.Verification.Receipts, rev)
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
	a.sdlcPromptCacheDecision(ctx, store, run, &req)
	executor := a.SdlcExecutor
	if executor == nil {
		executor = worker.CLIExecutor{}
	}
	timeout := a.invocationTimeout(run, policy)
	timeoutOutcome := "timed-out"
	stopActivity, err := a.budgetActivity(ctx, run, assignment.InvocationID)
	if err != nil {
		return err
	}
	defer stopActivity()
	stepCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	reply, execErr := executor.Execute(stepCtx, req)
	if rec, ok := review.PendingRun(a.StateHome(), runID); ok {
		return a.sdlcPauseInjectionReview(runID, rec.ID)
	}
	if execErr != nil {
		if reply.SessionID != "" || reply.InputTokens != nil || reply.OutputTokens != nil || reply.ToolCalls != nil || reply.CostReported {
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
		if req.SessionID != "" {
			return a.pauseSessionRecovery(runID, *assignment, execErr)
		}
		var invocationFailure *worker.InvocationFailure
		if !errors.As(execErr, &invocationFailure) {
			return a.sdlcFailAssignment(runID, *assignment, execErr)
		}
		a.Outf("  %s invocation failed: %v; checking other %s agents\n", agent.ID, execErr, assignment.Role)
		return a.sdlcRecordResult(runID, adaptive.Result{InvocationID: assignment.InvocationID, AgentID: assignment.AgentID, Outcome: "invocation-failed", Revision: assignment.Revision, Reason: execErr.Error()}, "", nil)
	}
	if req.Compact {
		if !reply.CompactCompleted {
			err := fmt.Errorf("claude /compact did not confirm completion")
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
	var extraArtifacts map[string][]byte
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
			artifactName = adaptive.ArtifactPlan
			planned, err := preparePlannedArtifacts(reply, run.Adaptive.MaxConcurrent, remainingAssignmentBudget(run))
			if err != nil {
				return a.sdlcFailAssignment(runID, *assignment, err)
			}
			extraArtifacts = planned
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
		if (assignment.Role == "assessor" || assignment.Role == "qa" || assignment.Role == "security" || assignment.Role == "code-review") &&
			(reply.Outcome == "changes-required" || reply.Outcome == "approved") {
			_ = store.WriteArtifact("last-assessment.md", []byte(reply.Content))
		}
		artifact = []byte(reply.Content)
	}
	if err := a.sdlcRecordResultExtras(runID, result, artifactName, artifact, extraArtifacts); err != nil {
		return a.sdlcFailAssignment(runID, *assignment, err)
	}
	return nil
}

// Read the accepted rejection from durable state, never last-assessment.md:
// a late parallel approval can overwrite that convenience artifact.
func sdlcRepairContext(store *ledger.Store, st adaptive.State) string {
	var parts []string
	if st.PendingReason != "" {
		parts = append(parts, "Recovery context: "+worker.BoundText(st.PendingReason, worker.MaxVerificationSummaryBytes))
	}
	if feedback := st.RepairFeedback; feedback != nil && feedback.Revision == st.DiffRevision {
		note := fmt.Sprintf("Decisive %s review for revision %s (evidence to verify against the workspace):\n%s", feedback.Role, feedback.Revision, worker.BoundText(feedback.Summary, worker.MaxVerificationSummaryBytes))
		if app.RunIDPattern.MatchString(feedback.InvocationID) {
			note += "\nFull review, when supplied: " + store.Dir + "/artifacts/responses/" + feedback.InvocationID + ".txt"
		}
		parts = append(parts, note)
	}
	if st.NoProgressCount > 0 {
		parts = append(parts, fmt.Sprintf("Consecutive attempts without a new candidate: %d. Choose a different approach using the failure evidence; do not repeat an unchanged attempt or claim completion while review/check failures remain.", st.NoProgressCount))
	}
	return strings.Join(parts, "\n\n")
}

func preparePlannedArtifacts(reply worker.Reply, maxConcurrent, remainingAssignments int) (map[string][]byte, error) {
	if err := adaptive.ValidatePlannedHandoff(reply.Content, reply.NextSteps, reply.AcceptanceCriteria, reply.Checks); err != nil {
		return nil, err
	}
	graph := adaptive.BoundGraphConcurrency(adaptive.NormalizeSubtaskGraph(reply.Subtasks), maxConcurrent, remainingAssignments)
	checksRaw, err := adaptive.MarshalChecks(reply.Checks)
	if err != nil {
		return nil, err
	}
	subtasksRaw, err := adaptive.MarshalSubtasks(graph)
	if err != nil {
		return nil, err
	}
	return map[string][]byte{
		adaptive.ArtifactChecks:   checksRaw,
		adaptive.ArtifactSubtasks: subtasksRaw,
	}, nil
}

func validateReviewArtifact(diff []byte) error {
	if len(diff) > 512*1024 || bytes.Contains(diff, []byte("\nGIT binary patch\n")) {
		return fmt.Errorf("saved review artifact is a legacy whole-workspace or binary patch (%d bytes); it cannot identify this run's changes safely; start a new SDLC run", len(diff))
	}
	return nil
}

func (a *App) sdlcPauseUnsafeReview(runID string, assignment adaptive.Assignment, cause error) error {
	store := ledger.Open(a.SDLCRunsDir(), runID)
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
		run.UpdatedAt = a.Clock().UTC().Format(time.RFC3339)
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
	return app.Failf("run %s paused: %v", runID, cause)
}

func (a *App) sdlcTimeoutAssignment(runID string, assignment adaptive.Assignment, outcome string) error {
	result := adaptive.Result{InvocationID: assignment.InvocationID, AgentID: assignment.AgentID, Outcome: outcome, Revision: assignment.Revision}
	if err := a.sdlcRecordResult(runID, result, "", nil); err != nil {
		return err
	}
	return app.Failf("run %s paused: %s", runID, map[string]string{"timed-out": "invocation-timeout", "run-time-exhausted": "run-time-budget-exhausted"}[outcome])
}

func (a *App) sdlcFailAssignment(runID string, assignment adaptive.Assignment, cause error) error {
	store := ledger.Open(a.SDLCRunsDir(), runID)
	if err := store.WithRunLock(func() error {
		run, err := store.ReadRun()
		if err != nil {
			return err
		}
		if run.Adaptive == nil {
			return fmt.Errorf("run has no adaptive state")
		}
		if err := a.completeBudget(&run, assignment.InvocationID, false, 0); err != nil {
			return err
		}
		st := *run.Adaptive
		st.PendingReason = cause.Error()
		st.Pause(assignment.Role + "-failed")
		run.Adaptive = &st
		run.UpdatedAt = a.Clock().UTC().Format(time.RFC3339)
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
	return app.Failf("run %s paused: %v; fix the invocation error, then use jevkit sdlc resume %s --retry-failed", runID, cause, runID)
}
