package agents

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/OWNER/jevkit/internal/registry"
	"github.com/OWNER/jevkit/internal/security/config"
)

// Antigravity adapter name used by installed runtime integrations.
const AntigravityName = "antigravity"

// AntigravityHookMarker is the idempotency substring for managed entries in
// .agents/hooks.json. Installer matching is substring-based so a binary-path
// change still replaces the prior entry instead of duplicating.
const AntigravityHookMarker = "_runtime dispatch --protocol 1 antigravity"
const legacyAntigravityHookMarker = "hook antigravity"

const AntigravityPreToolMarker = "_runtime dispatch --protocol 1 antigravity pre-tool"

// AntigravityPostToolMarker is retained for removal of old telemetry hooks.
const AntigravityPostToolMarker = "_runtime dispatch --protocol 1 antigravity post-tool"

// AntigravityHooksGroup is the top-level group key written into .agents/hooks.json
// (ralph uses "ralph-native"; jevkit owns its own group).
const AntigravityHooksGroup = "jevkit"

// Matcher mirrored from ralph bundle/.agents/hooks.json.
const antigravityRunCommandMatcher = "run_command"

// Default PreToolUse timeout seconds (ralph bundle/.agents/hooks.json).
const antigravityPreToolTimeout = 30

var antigravityAllowPassthrough = []byte(`{"decision":"allow"}`)

// Antigravity is the Antigravity (agy) adapter.
//
// Spike (docs/AGENT-CAPABILITIES.md): PreToolUse run_command rewrite via
// overwrite.CommandLine is the compaction path. PostToolUse carries only
// stepIdx/error — no output replacement. A decision must always be printed.
type Antigravity struct {
	// Binary is the jevkit executable name/path used in rewrites and install
	// commands. Empty means "jevkit".
	Binary          string
	Security        *config.Config
	SecurityDecider *registry.Decider
}

func init() {
	Register(NewAntigravity())
}

// NewAntigravity returns an Antigravity adapter.
func NewAntigravity() *Antigravity {
	return &Antigravity{}
}

func (a *Antigravity) Name() string { return AntigravityName }

func (a *Antigravity) Capabilities() Capabilities {
	return Capabilities{
		PreTool:        true,
		PreToolRewrite: true,
		// Antigravity's PostToolUse payload has no command or output, so the
		// installed integration uses no post-tool hook at all.
		PostTool:      false,
		OutputReplace: false,
	}
}

func (a *Antigravity) Passthrough(event Event) []byte {
	// agy requires a JSON decision on stdout; empty output is not allow.
	return append([]byte(nil), antigravityAllowPassthrough...)
}

func (a *Antigravity) HandlePreTool(ctx context.Context, req Request) (Response, error) {
	var payload struct {
		ToolCall struct {
			Name string `json:"name"`
			Args struct {
				CommandLine string `json:"CommandLine"`
			} `json:"args"`
		} `json:"toolCall"`
		WorkspacePaths []string `json:"workspacePaths"`
	}
	if err := json.Unmarshal(req.Raw, &payload); err != nil || payload.ToolCall.Name != antigravityRunCommandMatcher ||
		strings.TrimSpace(payload.ToolCall.Args.CommandLine) == "" || IsShellWrapperCommand(payload.ToolCall.Args.CommandLine) ||
		len(payload.WorkspacePaths) == 0 || strings.TrimSpace(payload.WorkspacePaths[0]) == "" {
		return Response{Body: a.Passthrough(EventPreTool)}, nil
	}
	if response, deny := securityDecision(ctx, a.Security, a.SecurityDecider, payload.ToolCall.Args.CommandLine, payload.WorkspacePaths[0], payload.WorkspacePaths[0], AntigravityName); deny {
		return response, nil
	}
	body, err := json.Marshal(map[string]any{
		"decision":  "allow",
		"overwrite": map[string]string{"CommandLine": buildSecurityShellWrapperCommand(a.binary(), payload.WorkspacePaths[0], payload.ToolCall.Args.CommandLine, AntigravityName, a.Security != nil && a.Security.Yolo, securityPolicyName(a.Security))},
	})
	if err != nil {
		return Response{Body: a.Passthrough(EventPreTool)}, nil
	}
	return Response{Body: body}, nil
}

func (a *Antigravity) HandlePostTool(ctx context.Context, req Request) (Response, error) {
	return Response{Body: a.Passthrough(EventPostTool)}, nil
}

func (a *Antigravity) HandleStop(ctx context.Context, req Request) (Response, error) {
	return Response{Body: a.Passthrough(EventStop)}, nil
}

func (a *Antigravity) binary() string {
	if a != nil && strings.TrimSpace(a.Binary) != "" {
		return strings.TrimSpace(a.Binary)
	}
	return "jevkit"
}
