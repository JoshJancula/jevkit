package security

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/JoshJancula/jevkit/internal/jev"
	"github.com/JoshJancula/jevkit/internal/redact"
	"github.com/JoshJancula/jevkit/internal/registry"
	"github.com/JoshJancula/jevkit/internal/security/config"
	"github.com/JoshJancula/jevkit/internal/security/review"
)

const InjectionQuestionSetID = "security.tool-output-injection"

type InjectionRequest struct{ Body, Runtime, Tool, Workspace, SessionKey, ToolInput, RawPointer, SDLCRunID string }
type InjectionVerdict struct {
	Halt, Shadow          bool
	Score, Confidence     float64
	Reason, Excerpt, Hash string
	Hits                  []string
}

// CheckInjection fails open on Jev and redaction errors. The caller stores a
// review before replacing output; a failed review write must not hide content.
func CheckInjection(ctx context.Context, cfg *config.Config, req InjectionRequest, decider *registry.Decider) InjectionVerdict {
	v := InjectionVerdict{Hash: review.Hash(req.Body)}
	if cfg == nil || cfg.Injection.Mode == "off" || cfg.Injection.Mode == "" || req.Body == "" || review.Allowed(cfg.StateDir, v.Hash) {
		return v
	}
	if len(cfg.Injection.Tools) > 0 {
		found := false
		for _, t := range cfg.Injection.Tools {
			if t == req.Tool {
				found = true
				break
			}
		}
		if !found {
			return v
		}
	}
	var strong bool
	v.Hits, strong = injectionHits(req.Body)
	if cfg.Injection.Scan != "all" && len(v.Hits) == 0 {
		return v
	}
	r, err := redact.New(redact.Options{})
	if err != nil {
		return v
	}
	redacted, err := r.Apply(req.Body)
	if err != nil {
		return v
	}
	budget := cfg.Injection.MaxBytes
	if budget <= 0 {
		budget = 16384
	}
	state := redacted.Text
	if len(state) > budget {
		state = state[:budget]
	}
	v.Excerpt = state
	if len(v.Excerpt) > 500 {
		v.Excerpt = v.Excerpt[:500]
	}
	if strong && cfg.Injection.HeuristicHalt {
		v.Halt = true
		v.Reason = "strong prompt-injection marker"
	}
	if cfg.Asker != nil && decider != nil {
		reg := decider.Registry
		if reg == nil {
			reg, _ = registry.Load()
			decider.Registry = reg
		}
		if reg != nil {
			if set, ok := reg.Set(InjectionQuestionSetID); ok {
				if questions, e := set.JevQuestions(nil); e == nil {
					scoreCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
					resp, e := cfg.Asker.Ask(scoreCtx, jev.Request{QuestionSetID: InjectionQuestionSetID, State: state, Questions: questions})
					cancel()
					if e == nil && resp != nil {
						if answer, ok := resp.Answers["injection"].(jev.ScoreAnswer); ok {
							if decision, e := decider.Decide(InjectionQuestionSetID, resp.Answers); e == nil {
								v.Score = answer.Score
								v.Confidence = decision.Confidence
								haltOn := cfg.Injection.HaltOn
								if haltOn == "" {
									haltOn = "escalate"
								}
								if answer.Score >= 3 && !decision.Shadow && (decision.Decision == registry.Act || (haltOn == "escalate" && decision.Decision == registry.Gather)) {
									v.Halt = true
									v.Reason = fmt.Sprintf("Jev prompt-injection score %.2f", answer.Score)
								}
							}
						}
					}
				}
			}
		}
	}
	if v.Halt && cfg.Injection.Mode == "shadow" {
		v.Shadow = true
		v.Halt = false
		recordShadow(cfg.StateDir, "would-halt: "+v.Reason, req.Runtime)
	}
	v.Reason = strings.TrimSpace(v.Reason)
	return v
}
