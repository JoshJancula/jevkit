package agents

import (
	"context"
	"encoding/json"
	"strings"
)

// Antigravity adapter name as used on the CLI: `jevkit hook antigravity ...`.
const AntigravityName = "antigravity"

// AntigravityHookMarker is the idempotency substring for managed entries in
// .agents/hooks.json. Installer matching is substring-based so a binary-path
// change still replaces the prior entry instead of duplicating.
const AntigravityHookMarker = "hook antigravity"

// AntigravityPreToolMarker is the full pre-tool event token.
const AntigravityPreToolMarker = "hook antigravity pre-tool"

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
	Binary string
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
		// PostTool is false: agy PostToolUse has no command/output to observe;
		// framework telemetry is recorded on the PreToolUse rewrite invocation.
		PostTool:      false,
		OutputReplace: false,
	}
}

func (a *Antigravity) Passthrough(event Event) []byte {
	// agy requires a JSON decision on stdout; empty output is not allow.
	return append([]byte(nil), antigravityAllowPassthrough...)
}

// HandlePreTool rewrites run_command CommandLine to `jevkit exec -- <cmd>`
// so the wrapper sees the real exit code and can compact. Non-run_command
// tools and already-rewritten commands fail open with decision allow.
func (a *Antigravity) HandlePreTool(ctx context.Context, req Request) (Response, error) {
	passthrough := Response{Body: a.Passthrough(EventPreTool)}

	var payload antigravityPrePayload
	if err := json.Unmarshal(req.Raw, &payload); err != nil {
		return passthrough, nil
	}
	if payload.ToolCall.Name != "" && payload.ToolCall.Name != antigravityRunCommandMatcher {
		return passthrough, nil
	}
	command := strings.TrimSpace(payload.ToolCall.Args.CommandLine)
	if command == "" {
		return passthrough, nil
	}

	rewritten := rewriteAntigravityCommand(a.binary(), command)
	body, err := json.Marshal(antigravityPreResponse{
		Decision: "allow",
		Overwrite: antigravityOverwrite{
			CommandLine: rewritten,
		},
	})
	if err != nil {
		return passthrough, nil
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

type antigravityPrePayload struct {
	ToolCall struct {
		Name string `json:"name"`
		Args struct {
			CommandLine string `json:"CommandLine"`
		} `json:"args"`
	} `json:"toolCall"`
	WorkspacePaths []string `json:"workspacePaths"`
}

type antigravityPreResponse struct {
	Decision  string               `json:"decision"`
	Overwrite antigravityOverwrite `json:"overwrite"`
}

type antigravityOverwrite struct {
	CommandLine string `json:"CommandLine"`
}

func rewriteAntigravityCommand(binary, command string) string {
	if alreadyAntigravityJevkitExec(command, binary) {
		return command
	}
	return binary + " exec -- " + command
}

func alreadyAntigravityJevkitExec(command, binary string) bool {
	if strings.Contains(command, binary+" exec -- ") {
		return true
	}
	return strings.Contains(command, "jevkit exec -- ") ||
		strings.Contains(command, "jevkit.exe exec -- ")
}
