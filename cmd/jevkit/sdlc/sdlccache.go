package sdlc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/jev"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/sdlc/worker"
)

// sdlcPromptCacheDecision controls only Claude Code's invocation-wide cache.
// Jev sees counts and a fingerprint, never prompt or tool-result content.
func (a *App) sdlcPromptCacheDecision(ctx context.Context, store *ledger.Store, run ledger.Run, req *worker.Request) {
	if req.Agent.Via != "runtime" {
		return
	}
	choice, reason := "unmanaged", "runtime has no supported provider cache control"
	if req.Agent.Runtime == "claude" {
		enabled := false
		req.PromptCache = &enabled
		choice, reason = "disabled", "Jev unavailable or reuse uncertain"
		if a.claudeCacheDisabledByUser(req.Agent.Model) {
			reason = "disabled by user environment"
		} else if score, err := a.askPromptCache(ctx, run, *req); err == nil {
			if score >= 0.90 {
				enabled, choice, reason = true, "enabled", fmt.Sprintf("Jev reuse score %.2f", score)
			} else {
				reason = fmt.Sprintf("Jev reuse score %.2f below 0.90", score)
			}
			*req.PromptCache = enabled
		}
	}
	_ = a.recordDecision(store, ledger.Decision{
		RunID: run.RunID, Kind: "prompt-cache", Stage: run.Adaptive.Stage,
		Invocation: req.Assignment.InvocationID, Runtime: req.Agent.Runtime,
		Choice: choice, Detail: reason, Next: "invoke agent",
	})
}

func (a *App) claudeCacheDisabledByUser(model string) bool {
	if app.Truthy(a.Getenv("DISABLE_PROMPT_CACHING")) {
		return true
	}
	model = strings.ToLower(model)
	for _, name := range []string{"opus", "sonnet", "haiku"} {
		if strings.Contains(model, name) && app.Truthy(a.Getenv("DISABLE_PROMPT_CACHING_"+strings.ToUpper(name))) {
			return true
		}
	}
	return false
}

func (a *App) askPromptCache(ctx context.Context, run ledger.Run, req worker.Request) (float64, error) {
	cfg, err := a.JevConfig()
	if err != nil {
		return 0, err
	}
	key := ""
	if cfg.Transport != jev.TransportFixture {
		key, _, err = a.Store().Resolve(ctx)
		if err != nil || key == "" {
			return 0, fmt.Errorf("Jev key unavailable")
		}
	}
	bytes, fingerprint := worker.StablePrefixMetadata(req.Assignment.Role)
	prior, reads, writes := 0, int64(0), int64(0)
	for _, u := range run.Usage {
		if u.Runtime != req.Agent.Runtime || u.Model != req.Agent.Model || u.StablePrefixFingerprint != fingerprint {
			continue
		}
		prior++
		if u.CacheReadTokens != nil {
			reads += *u.CacheReadTokens
		}
		if u.CacheCreationTokens != nil {
			writes += *u.CacheCreationTokens
		}
	}
	state, _ := json.Marshal(struct {
		Role                     string `json:"role"`
		Model                    string `json:"model"`
		StablePrefixBytes        int    `json:"stablePrefixBytes"`
		StablePrefixFingerprint  string `json:"stablePrefixFingerprint"`
		PriorMatchingInvocations int    `json:"priorMatchingInvocations"`
		PriorCacheReadTokens     int64  `json:"priorCacheReadTokens"`
		PriorCacheWriteTokens    int64  `json:"priorCacheWriteTokens"`
		AssignmentsRemaining     int    `json:"assignmentsRemaining"`
		ResumingSession          bool   `json:"resumingSession"`
	}{req.Assignment.Role, req.Agent.Model, bytes, fingerprint, prior, reads, writes,
		max(0, run.Adaptive.MaxAssignments-run.Adaptive.AssignmentCount), req.SessionID != ""})
	query := jev.Request{QuestionSetID: "sdlc.prompt-cache", State: string(state), Questions: map[string]jev.Question{
		"cache": jev.NoulQuestion{Instructions: "Return the probability that enabling provider prompt caching for this entire Claude Code invocation will save cost through cache reads within the cache lifetime. A write costs 1.25 times ordinary input. Require concrete evidence of likely repeated identical prefixes; score low when reuse is speculative. Consider that this is an invocation-wide control, not a per-tool decision."},
	}}
	askCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	askCtx = app.WithUsageOrigin(app.WithUsageRun(askCtx, run.RunID), "sdlc-cache", req.Agent.ID)
	client := a.JevClient(cfg, func() (string, error) { return key, nil })
	resp, err := client.Ask(askCtx, query)
	if err != nil || resp == nil {
		return 0, fmt.Errorf("Jev cache decision unavailable")
	}
	answer, ok := resp.Answers["cache"].(jev.NoulAnswer)
	if !ok {
		return 0, fmt.Errorf("Jev cache decision missing")
	}
	return answer.Noul, nil
}
