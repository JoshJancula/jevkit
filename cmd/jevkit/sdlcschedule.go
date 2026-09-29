package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/enrollment"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/sdlc/worker"
	"github.com/JoshJancula/jevkit/internal/security/review"
)

type fanoutReservation struct {
	id         string
	assignment adaptive.Assignment
	workspace  string
	mode       string
	agent      enrollment.Agent
}

// worktreeCreator returns the injected creator or the real Git implementation.
func (a *App) worktreeCreator() worker.WorktreeCreator {
	if a.WorktreeCreator != nil {
		return a.WorktreeCreator
	}
	return worker.GitWorktreeCreator{}
}

// approvedFanoutGraph loads the approved subtasks.json when digests still match.
func (a *App) approvedFanoutGraph(run ledger.Run, store *ledger.Store) (adaptive.SubtaskGraph, string, bool, error) {
	if run.Adaptive == nil || run.Adaptive.Stage != adaptive.Implementing {
		return adaptive.SubtaskGraph{}, "", false, nil
	}
	if run.RequirePlanApproval {
		if run.ApprovedSubtasksRevision == "" || run.Adaptive.SubtasksRevision == "" {
			return adaptive.SubtaskGraph{}, "", false, nil
		}
		if run.ApprovedSubtasksRevision != run.Adaptive.SubtasksRevision {
			return adaptive.SubtaskGraph{}, "", false, nil
		}
	}
	raw, err := store.ReadArtifact(adaptive.ArtifactSubtasks)
	if err != nil {
		if os.IsNotExist(err) {
			return adaptive.SubtaskGraph{}, "", false, nil
		}
		return adaptive.SubtaskGraph{}, "", false, err
	}
	digest := adaptive.DigestHex(raw)
	if run.RequirePlanApproval && run.ApprovedSubtasksRevision != digest {
		return adaptive.SubtaskGraph{}, "", false, fmt.Errorf("subtasks.json digest no longer matches approval")
	}
	var g adaptive.SubtaskGraph
	if err := json.Unmarshal(raw, &g); err != nil {
		return adaptive.SubtaskGraph{}, "", false, fmt.Errorf("parse subtasks.json: %w", err)
	}
	if g.Mode != "fan-out" || len(g.Subtasks) == 0 {
		return g, digest, false, nil
	}
	return g, digest, true, nil
}

func (a *App) ensureFanoutSchedule(store *ledger.Store, run *ledger.Run, graph adaptive.SubtaskGraph, digest string, now time.Time) error {
	if run.Fanout != nil && run.Fanout.GraphRevision == digest && len(run.Fanout.Subtasks) > 0 {
		return nil
	}
	source := ""
	if run.Fanout != nil {
		source = run.Fanout.SourceRevision
	}
	if source == "" {
		if rev, err := worker.ResolveSourceRevision(context.Background(), a.WorkDir); err == nil {
			source = rev
		} else {
			source = "unrecorded"
		}
	}
	sched := adaptive.NewSchedule(graph, digest, source)
	return store.WithRunLock(func() error {
		r, err := store.ReadRun()
		if err != nil {
			return err
		}
		if r.Fanout != nil && r.Fanout.GraphRevision == digest && len(r.Fanout.Subtasks) > 0 {
			*run = r
			return nil
		}
		r.Fanout = &sched
		r.UpdatedAt = now.UTC().Format(time.RFC3339)
		if err := store.WriteRun(r); err != nil {
			return err
		}
		*run = r
		return nil
	})
}

func (a *App) reconcileFanout(store *ledger.Store, run *ledger.Run, now time.Time) error {
	if run.Fanout == nil {
		return nil
	}
	var live []adaptive.LiveLease
	for id, st := range run.Fanout.Subtasks {
		if st.Status != adaptive.SubtaskReserved && st.Status != adaptive.SubtaskRunning {
			continue
		}
		alive := false
		if st.PID > 0 {
			alive = processAlive(st.PID)
		}
		live = append(live, adaptive.LiveLease{SubtaskID: id, LeaseOwner: st.LeaseOwner, PID: st.PID, Alive: alive})
	}
	return store.UpdateFanout(now, func(r *ledger.Run, s *adaptive.Schedule) error {
		_ = s.Reconcile(live, now)
		*run = *r
		run.Fanout = s
		return nil
	})
}

func (a *App) pauseFanout(store *ledger.Store, runID, reason string) error {
	return store.WithRunLock(func() error {
		run, err := store.ReadRun()
		if err != nil {
			return err
		}
		if run.Adaptive == nil {
			return fmt.Errorf("run has no adaptive state")
		}
		st := *run.Adaptive
		st.Pause(reason)
		run.Adaptive = &st
		if run.Fanout != nil && run.Fanout.PauseReason == "" {
			run.Fanout.Pause(reason)
		}
		run.UpdatedAt = a.now().UTC().Format(time.RFC3339)
		if err := store.WriteRun(run); err != nil {
			return err
		}
		a.outf("run %s paused: %s\n", runID, reason)
		return nil
	})
}

// sdlcDriveFanout runs the durable supervisor scheduler for an approved graph.
func (a *App) sdlcDriveFanout(ctx context.Context, runID string, store *ledger.Store, run ledger.Run) (handled bool, err error) {
	graph, digest, ok, err := a.approvedFanoutGraph(run, store)
	if err != nil || !ok {
		return false, err
	}
	policy, _, err := a.sdlcEnrollment()
	if err != nil {
		return true, failf("%v", err)
	}
	now := a.now()
	if err := a.ensureFanoutSchedule(store, &run, graph, digest, now); err != nil {
		return true, failf("%v", err)
	}
	if err := a.reconcileFanout(store, &run, now); err != nil {
		return true, failf("%v", err)
	}
	run, err = store.ReadRun()
	if err != nil {
		return true, failf("read run: %v", err)
	}
	if run.Fanout == nil {
		return true, failf("fan-out schedule missing after init")
	}
	if run.Fanout.PauseReason != "" {
		a.outf("run %s: fan-out paused (%s)\n", runID, run.Fanout.PauseReason)
		return true, nil
	}
	if run.Fanout.IntegrationPending || (run.Fanout.AllTerminal() && run.Fanout.HasFailures() && (run.Integration == nil || run.Integration.Status == "")) {
		a.outf("run %s: fan-out complete; running supervisor integration\n", runID)
		return true, a.sdlcIntegrateFanout(ctx, runID, store, run)
	}
	if run.Fanout.AllTerminal() && run.Fanout.HasFailures() {
		if run.Integration != nil && (run.Integration.Status == adaptive.IntegrationStatusRepair || run.Integration.Status == adaptive.IntegrationStatusPaused) {
			a.outf("run %s: integration %s — %s\n", runID, run.Integration.Status, run.Integration.Summary)
			return true, nil
		}
		return true, a.pauseFanout(store, runID, "fanout-subtask-failed")
	}

	remaining, err := a.treeRemaining(run, policy)
	if err != nil {
		return true, failf("%v", err)
	}
	root, err := a.rootRun(run)
	if err != nil {
		return true, failf("%v", err)
	}
	rootUsage := root.TreeUsage
	if rootUsage == nil {
		rootUsage = &ledger.TreeUsage{}
	}
	// Route candidates as implementers even when adaptive Role() is implementer.
	implState := *run.Adaptive
	candidates, err := a.adaptiveCandidates(implState, a.cliReach())
	if err != nil {
		return true, failf("%v", err)
	}
	bindings := enrollment.DistinctBindings(candidates)
	costOK := policy.MaxEstimatedCostUSD <= 0 || rootUsage.EstimatedCostUSD < policy.MaxEstimatedCostUSD
	remainAssign := policy.MaxAssignments - rootUsage.Assignments
	if remainAssign < 0 {
		remainAssign = 0
	}
	if run.Adaptive.MaxAssignments > 0 {
		left := run.Adaptive.MaxAssignments - run.Adaptive.AssignmentCount
		if left < remainAssign {
			remainAssign = left
		}
	}
	remainChild := sdlcMaxChildRuns - rootUsage.ChildRuns
	budgets := adaptive.AdmitBudgets{
		PolicyMax:            policy.MaxConcurrent,
		RunTreeLimit:         run.Fanout.EffectiveConcurrency,
		EligibleBindings:     bindings,
		RemainingAssignments: remainAssign,
		RemainingChildRuns:   remainChild,
		TimeRemaining:        remaining,
		CostRemainingOK:      costOK,
	}
	capN := adaptive.AdmitCap(budgets, run.Fanout.EffectiveConcurrency, run.Fanout.InFlight())
	if capN <= 0 {
		if run.Fanout.InFlight() > 0 {
			a.outf("run %s: fan-out has %d in-flight subtask(s)\n", runID, run.Fanout.InFlight())
			return true, nil
		}
		reason := "fanout-budget-exhausted"
		if remaining <= 0 {
			reason = "run-time-exhausted"
		} else if !costOK {
			reason = "cost-budget-exhausted"
		}
		return true, a.pauseFanout(store, runID, reason)
	}

	creator := a.worktreeCreator()
	worktreeOK := creator.Available(ctx, a.WorkDir)
	reach := a.cliReach()
	ready := run.Fanout.ReadyIDs()
	if len(ready) == 0 {
		if run.Fanout.InFlight() > 0 {
			a.outf("run %s: waiting on in-flight fan-out subtasks\n", runID)
			return true, nil
		}
		return true, a.pauseFanout(store, runID, "fanout-blocked")
	}

	var reserved []fanoutReservation
	for _, id := range ready {
		if len(reserved) >= capN {
			break
		}
		st := run.Fanout.Subtasks[id]
		used := map[string]bool{}
		for _, r := range reserved {
			used[r.assignment.Binding] = true
			used[r.assignment.AgentID] = true
		}
		var choice enrollment.Candidate
		found := false
		for _, c := range candidates {
			if used[c.Binding] || used[c.Agent.ID] {
				continue
			}
			choice, found = c, true
			break
		}
		if !found {
			break
		}
		rule := policy.Roles["implementer"]
		assignment := adaptive.Assignment{
			InvocationID:       a.newRunID(now),
			AgentID:            choice.Agent.ID,
			Binding:            choice.Binding,
			Via:                choice.Agent.Via,
			Runtime:            choice.Agent.Runtime,
			Role:               "implementer",
			Revision:           run.Adaptive.PlanRevision,
			ReadOnly:           !rule.Write || rule.ReadOnly || choice.Agent.ReadOnly,
			Isolated:           rule.Isolated || choice.Agent.Isolated,
			ProjectWriteScopes: append([]string(nil), rule.WriteScopes...),
			AgentWriteScopes:   append([]string(nil), choice.Agent.WriteScopes...),
			Objective:          st.Objective,
			Reason:             "fan-out subtask " + id,
		}
		writable := !assignment.ReadOnly
		readOnlyEnforced := false
		if capRuntime, ok := reach.Runtimes[choice.Agent.Runtime]; ok {
			readOnlyEnforced = assignment.ReadOnly && capRuntime.ReadOnly
		}
		wantParallel := run.Fanout.Parallelize && run.Fanout.EffectiveConcurrency > 1 && (run.Fanout.InFlight()+len(reserved) > 0 || len(ready) > 1)
		decision := adaptive.DecideIsolation(wantParallel, writable, readOnlyEnforced, worktreeOK, run.Fanout.WritableInFlight() || fanoutHasWritable(reserved))
		if decision.Mode == adaptive.WorkspaceUnavailable {
			if len(reserved) == 0 && run.Fanout.InFlight() == 0 {
				_ = store.UpdateFanout(now, func(r *ledger.Run, s *adaptive.Schedule) error {
					s.Pause(decision.Reason)
					return nil
				})
				return true, a.pauseFanout(store, runID, "fanout-isolation-unavailable")
			}
			break
		}
		if decision.Mode == adaptive.WorkspaceSequential {
			if run.Fanout.WritableInFlight() || fanoutHasWritable(reserved) {
				break
			}
			capN = 1
		}
		workspace := a.WorkDir
		if decision.Mode == adaptive.WorkspaceWorktree {
			dest := filepath.Join(store.Dir, "workspaces", id+"-"+assignment.InvocationID)
			ws, werr := creator.Create(ctx, a.WorkDir, run.Fanout.SourceRevision, dest)
			if werr != nil {
				_ = store.UpdateFanout(now, func(r *ledger.Run, s *adaptive.Schedule) error {
					s.Pause("worktree-create-failed: " + werr.Error())
					return nil
				})
				return true, a.pauseFanout(store, runID, "fanout-isolation-unavailable")
			}
			workspace = ws.Path
		}
		leaseOwner := assignment.InvocationID
		subID := id
		assignCopy := assignment
		wsPath := workspace
		mode := decision.Mode
		err := store.ReserveFanoutSlot(now, func(r *ledger.Run, s *adaptive.Schedule) error {
			if err := chargeTreeInPlace(r, policy, "assignment"); err != nil {
				return err
			}
			if err := chargeTreeInPlace(r, policy, "child"); err != nil {
				return err
			}
			if r.Adaptive != nil {
				r.Adaptive.AssignmentCount++
			}
			return s.Reserve(subID, assignCopy, wsPath, mode, leaseOwner, now)
		})
		if err != nil {
			if decision.Mode == adaptive.WorkspaceWorktree && workspace != a.WorkDir {
				_ = creator.Remove(ctx, worker.IsolatedWorkspace{Path: workspace, RepoRoot: a.WorkDir, Worktree: true})
			}
			if strings.Contains(err.Error(), "budget exhausted") {
				return true, a.pauseFanout(store, runID, "fanout-budget-exhausted")
			}
			if strings.Contains(err.Error(), "is ") {
				run, _ = store.ReadRun()
				continue
			}
			return true, failf("reserve %s: %v", id, err)
		}
		reserved = append(reserved, fanoutReservation{id: id, assignment: assignment, workspace: workspace, mode: decision.Mode, agent: choice.Agent})
		a.outf("  fan-out %s: %s via %s (%s)\n", id, choice.Agent.ID, choice.Agent.Runtime, decision.Mode)
		run, _ = store.ReadRun()
	}

	if len(reserved) == 0 {
		return true, nil
	}

	var wg sync.WaitGroup
	errCh := make(chan error, len(reserved))
	for _, r := range reserved {
		r := r
		wg.Add(1)
		go func() {
			defer wg.Done()
			errCh <- a.runFanoutSubtask(ctx, runID, store, r.id, r.assignment, r.agent, r.workspace, r.mode)
		}()
	}
	wg.Wait()
	close(errCh)
	var first error
	for e := range errCh {
		if e != nil && first == nil {
			first = e
		}
	}
	if first != nil {
		return true, first
	}
	run, err = store.ReadRun()
	if err != nil {
		return true, failf("read run: %v", err)
	}
	if run.Fanout != nil && (run.Fanout.IntegrationPending || (run.Fanout.AllTerminal() && run.Fanout.HasFailures() && (run.Integration == nil || run.Integration.Status == ""))) {
		a.outf("run %s: fan-out complete; running supervisor integration\n", runID)
		return true, a.sdlcIntegrateFanout(ctx, runID, store, run)
	}
	if run.Fanout != nil && run.Fanout.AllTerminal() && run.Fanout.HasFailures() {
		if run.Integration != nil && (run.Integration.Status == adaptive.IntegrationStatusRepair || run.Integration.Status == adaptive.IntegrationStatusPaused) {
			return true, nil
		}
		return true, a.pauseFanout(store, runID, "fanout-subtask-failed")
	}
	return true, nil
}

func fanoutHasWritable(reserved []fanoutReservation) bool {
	for _, r := range reserved {
		if !r.assignment.ReadOnly {
			return true
		}
	}
	return false
}

func chargeTreeInPlace(r *ledger.Run, p enrollment.Policy, kind string) error {
	if r.TreeUsage == nil {
		r.TreeUsage = &ledger.TreeUsage{}
		if r.Adaptive != nil {
			r.TreeUsage.Assignments = r.Adaptive.AssignmentCount
			r.TreeUsage.Revisions = r.Adaptive.RevisionCount
			r.TreeUsage.EstimatedCostUSD = r.Adaptive.EstimatedCostUSD
		}
	}
	u := r.TreeUsage
	if p.MaxEstimatedCostUSD > 0 && u.EstimatedCostUSD >= p.MaxEstimatedCostUSD {
		return fmt.Errorf("root cost budget exhausted")
	}
	switch kind {
	case "assignment":
		if u.Assignments >= p.MaxAssignments {
			return fmt.Errorf("root assignment budget exhausted")
		}
		u.Assignments++
	case "child":
		if u.ChildRuns >= sdlcMaxChildRuns {
			return fmt.Errorf("root child-run budget exhausted")
		}
		u.ChildRuns++
	default:
		return fmt.Errorf("unknown charge kind %q", kind)
	}
	return nil
}

func (a *App) runFanoutSubtask(ctx context.Context, runID string, store *ledger.Store, subtaskID string, assignment adaptive.Assignment, agent enrollment.Agent, workspace, mode string) error {
	now := a.now()
	_ = store.UpdateFanout(now, func(r *ledger.Run, s *adaptive.Schedule) error {
		return s.MarkRunning(subtaskID, os.Getpid(), now)
	})
	req := worker.Request{
		Agent: agent, Assignment: assignment, Task: "", WorkDir: workspace, Workspace: workspace,
		AllowRead: nil, Yolo: a.Yolo || truthy(a.getenv("JEVKIT_YOLO")), SecurityPolicy: a.SecurityPolicy,
		SDLCRunID: runID,
		LogDir:    store.Dir + "/logs", LogTailBytes: a.sdlcLogTailBytes(),
	}
	run, err := store.ReadRun()
	if err != nil {
		return failf("read run: %v", err)
	}
	a.applySDLCRuntimeIntegration(&req, run)
	req.Task = run.Task
	if assignment.Objective != "" {
		req.Task += "\n\nSubtask " + subtaskID + " objective: " + assignment.Objective
	}
	if plan, err := store.ReadArtifact(adaptive.ArtifactPlan); err == nil {
		req.Plan = string(plan)
	}
	executor := a.SdlcExecutor
	if executor == nil {
		executor = worker.CLIExecutor{}
	}
	invokeCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	type replyOrErr struct {
		reply worker.Reply
		err   error
	}
	done := make(chan replyOrErr, 1)
	go func() {
		reply, err := executor.Execute(invokeCtx, req)
		done <- replyOrErr{reply, err}
	}()

	var reply worker.Reply
	select {
	case <-ctx.Done():
		cancel()
		stopped := <-done
		if stopped.reply.InputTokens != nil || stopped.reply.OutputTokens != nil || stopped.reply.ToolCalls != nil || stopped.reply.CostReported {
			if err := a.saveInvocationUsage(store, assignment, agent.Model, stopped.reply); err != nil {
				return err
			}
		}
		_ = a.completeFanout(store, subtaskID, assignment, adaptive.SubtaskCancelled, adaptive.Result{
			InvocationID: assignment.InvocationID, AgentID: assignment.AgentID, Outcome: "failed", Reason: "cancelled",
		}, nil)
		return ctx.Err()
	case res := <-done:
		if rec, ok := review.PendingRun(a.stateHome(), runID); ok {
			return a.sdlcPauseInjectionReview(runID, rec.ID)
		}
		if res.err != nil {
			if res.reply.InputTokens != nil || res.reply.OutputTokens != nil || res.reply.ToolCalls != nil || res.reply.CostReported {
				if err := a.saveInvocationUsage(store, assignment, agent.Model, res.reply); err != nil {
					return err
				}
			}
			status := adaptive.SubtaskFailed
			outcome := "failed"
			if invokeCtx.Err() != nil {
				status = adaptive.SubtaskTimedOut
				outcome = "timed-out"
			}
			_ = a.completeFanout(store, subtaskID, assignment, status, adaptive.Result{
				InvocationID: assignment.InvocationID, AgentID: assignment.AgentID, Outcome: outcome, Reason: res.err.Error(),
			}, nil)
			return nil
		}
		reply = res.reply
	}

	status := adaptive.SubtaskSucceeded
	if reply.Outcome != "changed" && reply.Outcome != "no-change" && reply.Outcome != "answer" {
		status = adaptive.SubtaskFailed
	}
	result := adaptive.Result{
		InvocationID: assignment.InvocationID, AgentID: assignment.AgentID,
		Outcome: reply.Outcome, CostUSD: reply.CostUSD, Reason: reply.Reason, Focus: reply.Focus,
	}
	if reply.Content != "" {
		_ = store.WriteArtifact("fanout/"+subtaskID+"-"+assignment.InvocationID+".txt", []byte(reply.Content))
	}
	var usage *adaptive.SubtaskUsage
	if reply.CostUSD > 0 || reply.InputTokens != nil || reply.OutputTokens != nil || reply.ToolCalls != nil {
		cost := reply.CostUSD
		usage = &adaptive.SubtaskUsage{InputTokens: reply.InputTokens, OutputTokens: reply.OutputTokens, ToolCalls: reply.ToolCalls, CostUSD: &cost}
		_ = store.WithRunLock(func() error {
			latest, err := store.ReadRun()
			if err != nil {
				return err
			}
			return a.chargeTreeCost(&latest, mustPolicy(a), reply.CostUSD)
		})
	}
	if err := a.saveInvocationUsage(store, assignment, agent.Model, reply); err != nil {
		return err
	}
	_ = a.completeFanout(store, subtaskID, assignment, status, result, usage)
	_ = mode
	return nil
}

func mustPolicy(a *App) enrollment.Policy {
	p, _, err := a.sdlcEnrollment()
	if err != nil {
		return enrollment.DefaultPolicy()
	}
	return p
}

func (a *App) completeFanout(store *ledger.Store, subtaskID string, assignment adaptive.Assignment, status string, result adaptive.Result, usage *adaptive.SubtaskUsage) error {
	now := a.now()
	return store.UpdateFanout(now, func(r *ledger.Run, s *adaptive.Schedule) error {
		return s.Complete(subtaskID, status, result, usage, now)
	})
}
