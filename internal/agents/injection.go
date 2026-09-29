package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/JoshJancula/jevkit/internal/registry"
	"github.com/JoshJancula/jevkit/internal/security"
	"github.com/JoshJancula/jevkit/internal/security/config"
	"github.com/JoshJancula/jevkit/internal/security/review"
)

func hookSession(raw []byte, runtime string) (key, workspace string) {
	var p struct {
		SessionID      string   `json:"session_id"`
		ConversationID string   `json:"conversation_id"`
		CWD            string   `json:"cwd"`
		WorkspaceRoots []string `json:"workspace_roots"`
		WorkspacePaths []string `json:"workspacePaths"`
	}
	_ = json.Unmarshal(raw, &p)
	workspace = p.CWD
	if len(p.WorkspaceRoots) > 0 && p.WorkspaceRoots[0] != "" {
		workspace = p.WorkspaceRoots[0]
	}
	if len(p.WorkspacePaths) > 0 && p.WorkspacePaths[0] != "" {
		workspace = p.WorkspacePaths[0]
	}
	return review.SessionKey(runtime, p.SessionID, p.ConversationID, workspace), workspace
}

func latched(stateDir, runtime string, raw []byte) (Response, bool) {
	key, _ := hookSession(raw, runtime)
	rec, ok := review.Pending(stateDir, key)
	if !ok {
		return Response{}, false
	}
	message := reviewNotice(rec.ID)
	return Response{Body: denyBody(runtime, message), Deny: true, Outcome: OutcomeLatched}, true
}

func reviewNotice(id string) string {
	return "jevkit: tool output held for prompt-injection review " + id + ". Run: jevkit security review " + id
}

func scanToolOutput(ctx context.Context, cfg *config.Config, decider *registry.Decider, runtime, tool, input, body, workspace, key, pointer string) (review.Record, bool) {
	if cfg == nil || cfg.Injection.Mode == "off" {
		return review.Record{}, false
	}
	v := security.CheckInjection(ctx, cfg, security.InjectionRequest{Body: body, Runtime: runtime, Tool: tool, ToolInput: input, Workspace: workspace, SessionKey: key, RawPointer: pointer, SDLCRunID: os.Getenv("JEVKIT_SDLC_RUN_ID")}, decider)
	if !v.Halt {
		return review.Record{}, false
	}
	rec, err := review.Create(cfg.StateDir, review.Record{Runtime: runtime, SessionKey: key, Workspace: workspace, Tool: tool, ToolInput: short(input, 200), Score: v.Score, Confidence: v.Confidence, Reason: v.Reason, HeuristicHits: v.Hits, Excerpt: v.Excerpt, RawPointer: pointer, ContentSHA256: v.Hash, SDLCRunID: os.Getenv("JEVKIT_SDLC_RUN_ID")})
	if err != nil {
		return review.Record{}, false
	}
	if decider != nil && decider.StateDir != "" {
		_ = registry.AppendDecision(decider.StateDir, registry.Decision{Timestamp: rec.Created.Format("2006-01-02T15:04:05Z07:00"), Decision: "halt", QuestionSetID: security.InjectionQuestionSetID, Surface: "security", Runtime: runtime, Confidence: v.Confidence, Reason: v.Reason, ReviewID: rec.ID})
	}
	return rec, true
}
func short(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
func stopBody(id string, extra map[string]any) []byte {
	v := map[string]any{"continue": false, "stopReason": reviewNotice(id)}
	for k, x := range extra {
		v[k] = x
	}
	b, _ := json.Marshal(v)
	return b
}
func reviewError(id string) error { return fmt.Errorf("injection review %s pending", id) }
