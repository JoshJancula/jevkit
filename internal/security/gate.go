package security

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/OWNER/jevkit/internal/jev"
	"github.com/OWNER/jevkit/internal/redact"
	"github.com/OWNER/jevkit/internal/registry"
	"github.com/OWNER/jevkit/internal/security/config"
)

const QuestionSetID = "security.command-risk"

type Request struct {
	Command, Cwd, Workspace, Runtime string
}

type Decision struct {
	Deny   bool
	Reason string
	Score  *registry.Decision
	Shadow bool
}

// Evaluate checks deterministic rules before making an optional bounded Jev
// request. Jev errors have no effect on the deterministic decision.
func Evaluate(ctx context.Context, cfg *config.Config, req Request, decider *registry.Decider) (Decision, error) {
	if cfg == nil {
		return Decision{}, fmt.Errorf("security config is nil")
	}
	if pattern, ok := cfg.Killswitch.Match(req.Command); ok {
		return verdict(cfg, "killswitch: "+pattern, req.Runtime), nil
	}
	if !cfg.Yolo && req.Workspace != "" {
		guard := config.Guard{Workspace: req.Workspace, AllowRead: cfg.AllowRead}
		if req.Cwd != "" {
			if violation, ok := guard.CheckWorkDir(req.Cwd); !ok {
				return verdict(cfg, "sandbox: "+violation, req.Runtime), nil
			}
		}
		if violation, ok := guard.Check(req.Command); !ok {
			return verdict(cfg, "sandbox: "+violation, req.Runtime), nil
		}
	}
	if !cfg.JevScoring || cfg.Asker == nil || decider == nil {
		return Decision{}, nil
	}
	reg := decider.Registry
	if reg == nil {
		var err error
		reg, err = registry.Load()
		if err != nil {
			return Decision{}, nil
		}
		decider.Registry = reg
	}
	set, ok := reg.Set(QuestionSetID)
	if !ok {
		return Decision{}, nil
	}
	questions, err := set.JevQuestions(nil)
	if err != nil {
		return Decision{}, nil
	}
	redactor, err := redact.New(redact.Options{})
	if err != nil {
		return Decision{}, nil
	}
	redacted, err := redactor.Apply(req.Command)
	if err != nil {
		return Decision{}, nil
	}
	scoreCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	resp, err := cfg.Asker.Ask(scoreCtx, jev.Request{QuestionSetID: QuestionSetID, State: redacted.Text, Questions: questions})
	if err != nil || resp == nil {
		return Decision{}, nil
	}
	risk, ok := resp.Answers["risk"].(jev.ScoreAnswer)
	if !ok {
		return Decision{}, nil
	}
	score, err := decider.Decide(QuestionSetID, resp.Answers)
	if err != nil {
		return Decision{}, nil
	}
	score.Runtime = req.Runtime
	result := Decision{Score: &score}
	if risk.Score >= 3 && score.Decision == registry.Act && !score.Shadow {
		result = verdict(cfg, fmt.Sprintf("Jev risk score %.2f", risk.Score), req.Runtime)
		result.Score = &score
	}
	return result, nil
}

// CheckLocal is used at execution points where a second Jev request would
// duplicate a verdict already made by the pre-tool hook.
func CheckLocal(cfg *config.Config, req Request) Decision {
	if cfg == nil {
		return Decision{}
	}
	if pattern, ok := cfg.Killswitch.Match(req.Command); ok {
		return verdict(cfg, "killswitch: "+pattern, req.Runtime)
	}
	if cfg.Yolo || strings.TrimSpace(req.Workspace) == "" {
		return Decision{}
	}
	guard := config.Guard{Workspace: req.Workspace, AllowRead: cfg.AllowRead}
	if req.Cwd != "" {
		if violation, ok := guard.CheckWorkDir(req.Cwd); !ok {
			return verdict(cfg, "sandbox: "+violation, req.Runtime)
		}
	}
	if violation, ok := guard.Check(req.Command); !ok {
		return verdict(cfg, "sandbox: "+violation, req.Runtime)
	}
	return Decision{}
}

func verdict(cfg *config.Config, reason, runtime string) Decision {
	if cfg.Mode == "shadow" {
		recordShadow(cfg.StateDir, reason, runtime)
		return Decision{Reason: reason, Shadow: true}
	}
	return Decision{Deny: true, Reason: reason}
}
