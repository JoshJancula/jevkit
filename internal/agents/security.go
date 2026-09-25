package agents

import (
	"context"
	"encoding/json"

	"github.com/OWNER/jevkit/internal/registry"
	"github.com/OWNER/jevkit/internal/security"
	"github.com/OWNER/jevkit/internal/security/config"
)

func securityDecision(ctx context.Context, cfg *config.Config, decider *registry.Decider, command, cwd, workspace, runtime string) (Response, bool) {
	if cfg == nil {
		return Response{}, false
	}
	decision, _ := security.Evaluate(ctx, cfg, security.Request{Command: command, Cwd: cwd, Workspace: workspace, Runtime: runtime}, decider)
	if !decision.Deny {
		return Response{}, false
	}
	return Response{Body: denyBody(runtime, decision.Reason), Deny: true}, true
}

func denyBody(runtime, reason string) []byte {
	var value any
	switch runtime {
	case ClaudeName, CodexName:
		value = map[string]any{"hookSpecificOutput": map[string]any{
			"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": reason,
		}}
	case CursorName:
		value = map[string]any{"permission": "deny", "user_message": reason}
	case AntigravityName:
		value = map[string]any{"decision": "deny", "reason": reason}
	default:
		value = map[string]any{"decision": "deny", "reason": reason}
	}
	body, _ := json.Marshal(value)
	return body
}
