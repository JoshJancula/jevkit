package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
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

func (a *App) sdlcDrive(ctx context.Context, runID string) error {
	if !sdlcRunIDRE.MatchString(runID) {
		return usagef("invalid run ID")
	}
	store := ledger.Open(a.sdlcRunsDir(), runID)
	run, err := store.ReadRun()
	if err != nil {
		return failf("read run: %v", err)
	}
	if run.Adaptive == nil {
		return failf("run %s has an unsupported run format", runID)
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
	policy, roster, err := a.sdlcEnrollment()
	if err != nil {
		return failf("%v", err)
	}
	remaining, err := sdlcRunRemaining(run, policy, a.now())
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
	a.outf("  %s: %s", assignment.Role, agent.ID)
	if agent.Via == enrollment.Runtime {
		a.outf(" via %s", agent.Runtime)
	}
	a.outf("\n")
	if agent.Via != enrollment.Runtime && a.SdlcExecutor == nil {
		return a.sdlcFailAssignment(runID, *assignment, fmt.Errorf("native and host-self assignments need a host executor"))
	}
	req := worker.Request{Agent: *agent, Assignment: *assignment, Task: run.Task, WorkDir: a.WorkDir}
	if assignment.Objective != "" {
		req.Task += "\n\nStage objective: " + assignment.Objective
	}
	if assignment.Role == "implementer" || assignment.Role == "assessor" {
		plan, err := store.ReadArtifact("plan.md")
		if err != nil && assignment.Role == "assessor" {
			return a.sdlcFailAssignment(runID, *assignment, fmt.Errorf("read plan: %w", err))
		}
		if err == nil {
			req.Plan = string(plan)
		}
	}
	if assignment.Role == "assessor" {
		diff, err := store.ReadArtifact("patch.diff")
		if err != nil {
			return a.sdlcFailAssignment(runID, *assignment, fmt.Errorf("read diff: %w", err))
		}
		req.Diff = string(diff)
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
		if errors.Is(execErr, context.DeadlineExceeded) {
			return a.sdlcTimeoutAssignment(runID, *assignment, timeoutOutcome)
		}
		return a.sdlcFailAssignment(runID, *assignment, execErr)
	}
	result := adaptive.Result{InvocationID: assignment.InvocationID, AgentID: assignment.AgentID, Outcome: reply.Outcome, CostUSD: reply.CostUSD}
	artifactName := ""
	var artifact []byte
	if reply.Outcome == "planned" || reply.Outcome == "changed" {
		artifact = []byte(reply.Content)
		if len(artifact) == 0 {
			return a.sdlcFailAssignment(runID, *assignment, fmt.Errorf("empty %s artifact", reply.Outcome))
		}
		result.Revision = fmt.Sprintf("%x", sha256.Sum256(artifact))
		if reply.Outcome == "planned" {
			artifactName = "plan.md"
		} else {
			artifactName = "patch.diff"
		}
	} else if assignment.Role == "assessor" {
		result.Revision = assignment.Revision
	}
	if artifactName == "" && reply.Content != "" {
		artifactName = "responses/" + assignment.InvocationID + ".txt"
		artifact = []byte(reply.Content)
	}
	if err := a.sdlcRecordResult(runID, result, artifactName, artifact); err != nil {
		return a.sdlcFailAssignment(runID, *assignment, err)
	}
	return nil
}

func (a *App) sdlcTimeoutAssignment(runID string, assignment adaptive.Assignment, outcome string) error {
	result := adaptive.Result{InvocationID: assignment.InvocationID, AgentID: assignment.AgentID, Outcome: outcome, Revision: assignment.Revision}
	if err := a.sdlcRecordResult(runID, result, "", nil); err != nil {
		return err
	}
	return failf("run %s paused: %s", runID, map[string]string{"timed-out": "invocation-timeout", "run-time-exhausted": "run-time-budget-exhausted"}[outcome])
}

func (a *App) sdlcFailAssignment(runID string, assignment adaptive.Assignment, cause error) error {
	_ = a.sdlcRecordResult(runID, adaptive.Result{InvocationID: assignment.InvocationID, AgentID: assignment.AgentID, Outcome: "failed", Revision: assignment.Revision}, "", nil)
	return failf("sdlc drive: %v", cause)
}
