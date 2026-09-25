package main

import (
	"context"
	"fmt"
	"github.com/OWNER/jevkit/internal/registry"
	"github.com/OWNER/jevkit/internal/sdlc/adaptive"
	"github.com/OWNER/jevkit/internal/sdlc/ledger"
	"github.com/OWNER/jevkit/internal/sdlc/route"
	"github.com/OWNER/jevkit/internal/sdlc/worker"
)

func validateSessionStrategy(v string) error {
	switch v {
	case "auto", "fresh", "resume", "compact":
		return nil
	}
	return usagef("--session-strategy must be auto, fresh, resume or compact")
}

func (a *App) setRunSessionStrategy(id, strategy string) error {
	if !sdlcRunIDRE.MatchString(id) {
		return usagef("invalid run ID")
	}
	store := ledger.Open(a.sdlcRunsDir(), id)
	return store.WithRunLock(func() error {
		r, err := store.ReadRun()
		if err != nil {
			return err
		}
		r.SessionStrategy = strategy
		if err := store.WriteRun(r); err != nil {
			return err
		}
		return a.recordDecision(store, ledger.Decision{RunID: id, Kind: "session-strategy", Choice: strategy, Trigger: "resume override", Next: "apply to next invocation"})
	})
}

func sessionKey(assignment adaptive.Assignment) string {
	return assignment.Binding + "/" + assignment.Role
}

func (a *App) chooseSession(ctx context.Context, store *ledger.Store, run ledger.Run, assignment adaptive.Assignment, policyStrategy string) (string, string, error) {
	strategy := run.SessionStrategy
	if strategy == "" {
		strategy = policyStrategy
	}
	if strategy == "" {
		strategy = "auto"
	}
	prior := run.Sessions[sessionKey(assignment)]
	basis := "configured policy"
	if prior == "" {
		strategy, basis = "fresh", "no prior session for this binding and role"
	}
	if strategy == "auto" {
		strategy, basis = "resume", "policy fallback"
		router, err := a.sdlcRouter()
		if err == nil {
			state := fmt.Sprintf("role: %s; stage: %s; plan revision: %s; diff revision: %s; prior session available: true", assignment.Role, run.Adaptive.Stage, run.Adaptive.PlanRevision, run.Adaptive.DiffRevision)
			res, err := router.Decide(ctx, "sdlc.session-strategy", state, route.CriteriaFromRubrics(map[string]string{"fresh": "Discard stale context and start a new session", "resume": "Continue the prior session for this exact binding and role", "compact": "Compact the prior session before continuing"}))
			if err == nil && res.Available && res.Decision.Decision == registry.Act && res.Decision.Chosen != nil {
				strategy, basis = *res.Decision.Chosen, "Jev choice"
			}
		}
	}
	if strategy == "compact" && assignment.Runtime != "codex" && assignment.Runtime != "claude" {
		strategy, basis = "resume", "native compaction unavailable in this adapter; resume explicit session"
	}
	for _, pending := range run.Adaptive.Pending() {
		if pending.InvocationID != assignment.InvocationID && pending.Binding == assignment.Binding && pending.Role == assignment.Role {
			strategy, basis = "fresh", "parallel invocation owns the prior session"
		}
	}
	id := ""
	if strategy == "resume" || strategy == "compact" {
		id = prior
	}
	err := a.recordDecision(store, ledger.Decision{RunID: run.RunID, Kind: "session-strategy", Stage: run.Adaptive.Stage, Invocation: assignment.InvocationID, Runtime: assignment.Runtime, Trigger: sessionKey(assignment), Choice: strategy, Outcome: basis, Next: "invoke agent"})
	return strategy, id, err
}

func (a *App) saveInvocationUsage(store *ledger.Store, assignment adaptive.Assignment, model string, reply worker.Reply) error {
	return store.WithRunLock(func() error {
		r, err := store.ReadRun()
		if err != nil {
			return err
		}
		if r.Sessions == nil {
			r.Sessions = map[string]string{}
		}
		if reply.SessionID != "" {
			r.Sessions[sessionKey(assignment)] = reply.SessionID
		}
		u := ledger.InvocationUsage{Invocation: assignment.InvocationID, Agent: assignment.AgentID, Runtime: assignment.Runtime, Model: model, Role: assignment.Role, SessionID: reply.SessionID, InputTokens: reply.InputTokens, OutputTokens: reply.OutputTokens}
		if reply.CostReported || reply.CostUSD > 0 {
			u.CostUSD = &reply.CostUSD
		}
		found := false
		for i := range r.Usage {
			if r.Usage[i].Invocation == u.Invocation {
				r.Usage[i] = u
				found = true
				break
			}
		}
		if !found {
			r.Usage = append(r.Usage, u)
		}
		return store.WriteRun(r)
	})
}
