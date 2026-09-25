package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OWNER/jevkit/internal/sdlc/adaptive"
	"github.com/OWNER/jevkit/internal/sdlc/ledger"
	"github.com/OWNER/jevkit/internal/sdlc/worker"
)

func (a *App) saveReviewRecovery(store *ledger.Store, assignment adaptive.Assignment, model string, reply worker.Reply) error {
	return store.WithRunLock(func() error {
		run, err := store.ReadRun()
		if err != nil {
			return err
		}
		run.ReviewRecovery = &ledger.ReviewRecovery{
			Invocation: assignment.InvocationID, Agent: assignment.AgentID, Binding: assignment.Binding,
			Runtime: assignment.Runtime, Model: model, Revision: assignment.Revision, Outcome: reply.Outcome,
			Content: reply.Content, Reason: reply.Reason, Paths: reply.WorkspaceDrift,
			Truncated: reply.DriftTruncated, SessionID: reply.SessionID,
			InputTokens: reply.InputTokens, OutputTokens: reply.OutputTokens,
		}
		if reply.CostReported || reply.CostUSD > 0 {
			run.ReviewRecovery.CostUSD = &reply.CostUSD
		}
		if err := store.WriteRun(run); err != nil {
			return err
		}
		detail := fmt.Sprintf("%d observed paths; editor unknown", len(reply.WorkspaceDrift))
		if len(reply.WorkspaceDrift) == 0 {
			detail = "historical drift: changed paths unavailable; editor unknown"
		}
		_ = a.recordDecision(store, ledger.Decision{RunID: run.RunID, Kind: "review-workspace-drift",
			Stage: adaptive.Assessing, Invocation: assignment.InvocationID, Runtime: assignment.Runtime,
			Trigger: reply.Outcome, Choice: "recorded", Detail: detail,
			Next: "use saved assessment or reassess changed workspace"})
		return nil
	})
}

func (a *App) finishReviewRecovery(runID string, assignment adaptive.Assignment, reply worker.Reply) error {
	store := ledger.Open(a.sdlcRunsDir(), runID)
	if reply.Outcome == "approved" && len(reply.WorkspaceDrift) > 0 {
		cause := "workspace changed during assessment; assess the changed workspace again"
		return store.WithRunLock(func() error {
			run, err := store.ReadRun()
			if err != nil {
				return err
			}
			run.Adaptive.PendingReason = cause
			run.Adaptive.Pause("review-workspace-drift")
			run.UpdatedAt = a.now().UTC().Format(time.RFC3339)
			if err := store.WriteRun(run); err != nil {
				return err
			}
			_ = a.recordDecision(store, ledger.Decision{RunID: runID, Kind: "review-recovery", Stage: adaptive.Assessing,
				Invocation: assignment.InvocationID, Runtime: assignment.Runtime, Trigger: reply.Outcome,
				Choice: adaptive.Paused, Detail: cause, Next: "jevkit sdlc resume " + runID + " --retry-failed"})
			return nil
		})
	}
	result := adaptive.Result{InvocationID: assignment.InvocationID, AgentID: assignment.AgentID,
		Outcome: reply.Outcome, Revision: assignment.Revision, CostUSD: reply.CostUSD,
		Focus: reply.Focus, Reason: reply.Reason}
	artifact := ""
	if reply.Content != "" {
		artifact = "responses/" + assignment.InvocationID + ".txt"
	}
	if err := a.sdlcRecordResult(runID, result, artifact, []byte(reply.Content)); err != nil {
		return err
	}
	return store.WithRunLock(func() error {
		run, err := store.ReadRun()
		if err != nil {
			return err
		}
		if run.ReviewRecovery != nil && run.ReviewRecovery.Invocation == assignment.InvocationID {
			run.ReviewRecovery.Applied = true
			return store.WriteRun(run)
		}
		return nil
	})
}

// recoverReview first uses durable replies written by current versions. The
// legacy path is restricted to the historical drift failure with a saved final
// stream, a matching invocation and a verified patch digest.
func (a *App) recoverReview(ctx context.Context, runID string) error {
	store := ledger.Open(a.sdlcRunsDir(), runID)
	run, err := store.ReadRun()
	if err != nil {
		return err
	}
	if run.Adaptive == nil {
		return nil
	}
	if run.Adaptive.Stage != adaptive.Assessing {
		if run.ReviewRecovery != nil && !run.ReviewRecovery.Applied {
			for _, assessment := range run.Adaptive.Assessments {
				if assessment.InvocationID == run.ReviewRecovery.Invocation {
					return store.WithRunLock(func() error {
						fresh, err := store.ReadRun()
						if err != nil {
							return err
						}
						if fresh.ReviewRecovery != nil && fresh.ReviewRecovery.Invocation == assessment.InvocationID {
							fresh.ReviewRecovery.Applied = true
							return store.WriteRun(fresh)
						}
						return nil
					})
				}
			}
		}
		return nil
	}
	if run.ReviewRecovery == nil {
		if err := a.recoverLegacyReview(ctx, store, run); err != nil {
			_ = a.pauseReviewRecovery(store, run, err)
			return failf("saved review cannot be reused: %v; inspect the run and resume with --retry-failed to reassess", err)
		}
		run, err = store.ReadRun()
		if err != nil {
			return err
		}
	}
	recovery := run.ReviewRecovery
	if recovery == nil || recovery.Applied {
		return nil
	}
	if err := verifyReviewRecovery(store, run, recovery); err != nil {
		_ = a.pauseReviewRecovery(store, run, err)
		return failf("saved review cannot be reused: %v; inspect the run and resume with --retry-failed to reassess", err)
	}
	assignment := adaptive.Assignment{InvocationID: recovery.Invocation, AgentID: recovery.Agent,
		Binding: recovery.Binding, Runtime: recovery.Runtime, Role: "assessor", Revision: recovery.Revision}
	if _, ok := run.Adaptive.Assignments[assignment.InvocationID]; !ok {
		if err := store.WithRunLock(func() error {
			fresh, err := store.ReadRun()
			if err != nil {
				return err
			}
			if fresh.Adaptive.Assignments == nil {
				fresh.Adaptive.Assignments = map[string]adaptive.Assignment{}
			}
			fresh.Adaptive.Assignments[assignment.InvocationID] = assignment
			return store.WriteRun(fresh)
		}); err != nil {
			return err
		}
	}
	reply := worker.Reply{Outcome: recovery.Outcome, Content: recovery.Content, Reason: recovery.Reason,
		WorkspaceDrift: recovery.Paths, DriftTruncated: recovery.Truncated, SessionID: recovery.SessionID,
		InputTokens: recovery.InputTokens, OutputTokens: recovery.OutputTokens}
	if recovery.CostUSD != nil {
		reply.CostUSD, reply.CostReported = *recovery.CostUSD, true
	}
	if err := a.saveInvocationUsage(store, assignment, recovery.Model, reply); err != nil {
		return err
	}
	return a.finishReviewRecovery(runID, assignment, reply)
}

func verifyReviewRecovery(store *ledger.Store, run ledger.Run, recovery *ledger.ReviewRecovery) error {
	patch, err := store.ReadArtifact("patch.diff")
	if err != nil {
		return fmt.Errorf("read patch.diff: %w", err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(patch)) != recovery.Revision || run.Adaptive.DiffRevision != recovery.Revision {
		return fmt.Errorf("patch.diff digest no longer matches the reviewer assignment")
	}
	if recovery.Invocation == "" || recovery.Agent == "" || recovery.Binding == "" ||
		(recovery.Outcome != "approved" && recovery.Outcome != "changes-required") {
		return fmt.Errorf("saved review has incomplete invocation or outcome binding")
	}
	for _, assessment := range run.Adaptive.Assessments {
		if assessment.InvocationID == recovery.Invocation {
			return fmt.Errorf("review invocation was already applied")
		}
	}
	return nil
}

func (a *App) pauseReviewRecovery(store *ledger.Store, run ledger.Run, cause error) error {
	return store.WithRunLock(func() error {
		fresh, err := store.ReadRun()
		if err != nil {
			return err
		}
		fresh.Adaptive.PendingReason = cause.Error()
		fresh.Adaptive.Pause("review-recovery-invalid")
		return store.WriteRun(fresh)
	})
}

func (a *App) recoverLegacyReview(ctx context.Context, store *ledger.Store, run ledger.Run) error {
	if len(run.Adaptive.Assignments) != 0 || len(run.Adaptive.Assessments) != 0 {
		return nil
	}
	decisions, err := store.ReadDecisions()
	if err != nil {
		return err
	}
	var invocation, agentID, runtime string
	for i := len(decisions) - 1; i >= 0; i-- {
		d := decisions[i]
		if d.Kind == "invocation-outcome" && d.Stage == adaptive.Assessing && d.Choice == "failed" &&
			strings.Contains(d.Detail, "non-implementer changed the workspace") {
			invocation, agentID, runtime = d.Invocation, d.Trigger, d.Runtime
			break
		}
		if d.Kind == "invocation-outcome" && d.Stage == adaptive.Assessing {
			return nil
		}
	}
	if invocation == "" || !sdlcRunIDRE.MatchString(invocation) {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var meta worker.LogMeta
	raw, err := os.ReadFile(filepath.Join(store.Dir, "logs", invocation+".json"))
	if err != nil || json.Unmarshal(raw, &meta) != nil || meta.Invocation != invocation || meta.Agent != agentID || meta.Runtime != runtime {
		return failf("saved review invocation metadata does not match; reassess with --retry-failed")
	}
	_, roster, err := a.sdlcEnrollment()
	if err != nil {
		return err
	}
	binding := ""
	model := ""
	for _, agent := range roster.Agents {
		allowed := false
		for _, role := range agent.Roles {
			if role == "assessor" || role == "all" {
				allowed = true
			}
		}
		if agent.ID == agentID && agent.Runtime == runtime && allowed {
			binding = "runtime:" + agent.Runtime + ":" + agent.Model + ":" + agent.RuntimeAgent + ":" + agent.Binary
			model = agent.Model
			break
		}
	}
	if binding == "" {
		return failf("saved reviewer binding is no longer enrolled; reassess with --retry-failed")
	}
	bound := false
	for _, decision := range decisions {
		if decision.Kind == "session-strategy" && decision.Invocation == invocation && decision.Trigger == binding+"/assessor" {
			bound = true
		}
	}
	if !bound {
		return failf("saved reviewer binding does not match the invocation; reassess with --retry-failed")
	}
	file, err := os.Open(filepath.Join(store.Dir, "logs", invocation+".stdout"))
	if err != nil {
		return err
	}
	defer file.Close()
	scan := bufio.NewScanner(file)
	scan.Buffer(make([]byte, 64*1024), 16<<20)
	var reply worker.Reply
	var finished bool
	var tokensIn, tokensOut int64
	var tokensSeen bool
	var cost float64
	var costSeen bool
	for scan.Scan() {
		var event struct {
			Type string `json:"type"`
			Part struct {
				Text   string   `json:"text"`
				Cost   *float64 `json:"cost"`
				Tokens *struct {
					Input  int64 `json:"input"`
					Output int64 `json:"output"`
				} `json:"tokens"`
			} `json:"part"`
		}
		if json.Unmarshal(scan.Bytes(), &event) != nil {
			continue
		}
		if event.Type == "text" && event.Part.Text != "" {
			if parsed, err := worker.ParseReply([]byte(event.Part.Text)); err == nil {
				reply = parsed
			}
		}
		if event.Type == "step_finish" {
			finished = true
			if event.Part.Tokens != nil {
				tokensIn += event.Part.Tokens.Input
				tokensOut += event.Part.Tokens.Output
				tokensSeen = true
			}
			if event.Part.Cost != nil {
				cost += *event.Part.Cost
				costSeen = true
			}
		}
	}
	if err := scan.Err(); err != nil {
		return err
	}
	if !finished || reply.Outcome != "changes-required" {
		return failf("saved assessment has no complete changes-required reply; reassess with --retry-failed")
	}
	if tokensSeen {
		reply.InputTokens, reply.OutputTokens = &tokensIn, &tokensOut
	}
	if costSeen {
		reply.CostUSD, reply.CostReported = cost, true
	}
	assignment := adaptive.Assignment{InvocationID: invocation, AgentID: agentID, Binding: binding, Runtime: runtime,
		Role: "assessor", Revision: run.Adaptive.DiffRevision}
	if err := verifyReviewRecovery(store, run, &ledger.ReviewRecovery{Invocation: invocation, Agent: agentID, Binding: binding,
		Runtime: runtime, Revision: assignment.Revision, Outcome: reply.Outcome}); err != nil {
		return err
	}
	// Historical streams have no invocation baseline, so their drift paths are
	// explicitly unknown. Do not guess from the current working tree.
	reply.Reason = strings.TrimSpace(reply.Reason + " Workspace drift was observed; historical changed paths are unavailable.")
	if err := a.saveReviewRecovery(store, assignment, model, reply); err != nil {
		return err
	}
	return a.saveInvocationUsage(store, assignment, model, reply)
}
