package sdlc

import (
	"context"
	"fmt"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/registry"
	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/sdlc/route"
	"github.com/JoshJancula/jevkit/internal/sdlc/worker"
)

func validateSessionStrategy(v string) error {
	switch v {
	case "auto", "fresh", "resume", "compact":
		return nil
	}
	return app.Usagef("--session-strategy must be auto, fresh, resume or compact")
}

func (a *App) setRunSessionStrategy(id, strategy string) error {
	if !app.RunIDPattern.MatchString(id) {
		return app.Usagef("invalid run ID")
	}
	store := ledger.Open(a.SDLCRunsDir(), id)
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
	key := assignment.Binding + "/" + assignment.Role
	if assignment.ToolPolicyFingerprint != "" {
		key += "/tools/" + assignment.ToolPolicyFingerprint
	}
	if assignment.RuntimeArgsFingerprint != "" {
		key += "/args/" + assignment.RuntimeArgsFingerprint
	}
	return key
}

func (a *App) chooseSession(ctx context.Context, store *ledger.Store, run ledger.Run, assignment adaptive.Assignment, policyStrategy string) (string, string, error) {
	strategy := run.SessionStrategy
	if strategy == "" {
		strategy = policyStrategy
	}
	if strategy == "" {
		strategy = "auto"
	}
	key := sessionKey(assignment)
	prior := run.Sessions[key]
	basis := "configured policy"
	if prior == "" {
		strategy, basis = "fresh", "no prior session for this binding and role"
	}
	if strategy == "auto" && prior != "" {
		strategy, basis = "resume", "policy fallback"
		router, err := a.sdlcRouter()
		if err == nil {
			state := fmt.Sprintf("role: %s; stage: %s; plan revision: %s; diff revision: %s; workdir: %s; prior session available: true", assignment.Role, run.Adaptive.Stage, run.Adaptive.PlanRevision, run.Adaptive.DiffRevision, run.WorkDir)
			res, err := router.Decide(ctx, "sdlc.session-strategy", state, route.CriteriaFromRubrics(map[string]string{
				"fresh":   "Discard stale context and start a new session",
				"resume":  "Continue the prior session for this exact binding, role, project, and revision",
				"compact": "Compact the prior session before continuing",
			}))
			if err == nil && res.Available && res.Decision.Decision == registry.Act && res.Decision.Chosen != nil {
				strategy, basis = *res.Decision.Chosen, "Jev choice"
			}
		}
	}
	if strategy == "compact" && !sessionCompactAvailable(assignment.Runtime) {
		strategy, basis = "resume", "native compaction unavailable in this adapter; resume explicit session"
	}
	if prior != "" && (strategy == "resume" || strategy == "compact") {
		if stale, why := sessionStale(run, key); stale {
			if sessionCompactAvailable(assignment.Runtime) && strategy != "fresh" {
				strategy, basis = "compact", why
			} else {
				strategy, basis = "fresh", why
			}
			// Workdir/project change always starts fresh: prior transcripts are for another tree.
			if why == "project/workdir changed since prior session" {
				strategy, basis = "fresh", why
			}
		}
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
	err := a.recordDecision(store, ledger.Decision{RunID: run.RunID, Kind: "session-strategy", Stage: run.Adaptive.Stage, Invocation: assignment.InvocationID, Runtime: assignment.Runtime, Trigger: key, Choice: strategy, Outcome: basis, Next: "invoke agent"})
	return strategy, id, err
}

func sessionCompactAvailable(runtime string) bool {
	return runtime == "codex" || runtime == "claude"
}

// sessionStale detects project/workdir or plan/diff revision drift against the
// last saved SessionContext for this binding/role. Missing context (legacy
// run.json) is not treated as stale.
func sessionStale(run ledger.Run, key string) (bool, string) {
	ctx, ok := run.SessionContexts[key]
	if !ok {
		return false, ""
	}
	if ctx.WorkDir != "" && run.WorkDir != "" && ctx.WorkDir != run.WorkDir {
		return true, "project/workdir changed since prior session"
	}
	planRev, diffRev := "", ""
	if run.Adaptive != nil {
		planRev, diffRev = run.Adaptive.PlanRevision, run.Adaptive.DiffRevision
	}
	if ctx.PlanRevision != "" && planRev != "" && ctx.PlanRevision != planRev {
		return true, "plan revision changed since prior session"
	}
	if ctx.DiffRevision != "" && diffRev != "" && ctx.DiffRevision != diffRev {
		return true, "change-report revision changed since prior session"
	}
	return false, ""
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
		if r.SessionContexts == nil {
			r.SessionContexts = map[string]ledger.SessionContext{}
		}
		key := sessionKey(assignment)
		if reply.SessionID != "" {
			r.Sessions[key] = reply.SessionID
			planRev, diffRev := "", ""
			if r.Adaptive != nil {
				planRev, diffRev = r.Adaptive.PlanRevision, r.Adaptive.DiffRevision
			}
			r.SessionContexts[key] = ledger.SessionContext{
				WorkDir:      r.WorkDir,
				PlanRevision: planRev,
				DiffRevision: diffRev,
			}
		}
		u := ledger.InvocationUsage{
			Invocation: assignment.InvocationID, Agent: assignment.AgentID, Runtime: assignment.Runtime, Model: model, Role: assignment.Role,
			SessionID: reply.SessionID, InputTokens: reply.InputTokens, OutputTokens: reply.OutputTokens, ToolCalls: reply.ToolCalls,
			CacheReadTokens: reply.CacheReadTokens, CacheCreationTokens: reply.CacheCreationTokens,
			UsageProvenance:   reply.UsageProvenance,
			StablePrefixBytes: reply.StablePrefixBytes, StablePrefixFingerprint: reply.StablePrefixFingerprint,
		}
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
