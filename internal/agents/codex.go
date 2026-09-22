package agents

import (
	"context"
	"encoding/json"
	"strings"
)

// Codex adapter name as used on the CLI: `jevkit hook codex ...`.
const CodexName = "codex"

// CodexHookMarker is the idempotency substring for managed entries in
// .codex/hooks.json. Installer matching is substring-based so a binary-path
// change still replaces the prior entry instead of duplicating.
const CodexHookMarker = "hook codex"

// CodexPreToolMarker / CodexPostToolMarker are the full event tokens.
const (
	CodexPreToolMarker  = "hook codex pre-tool"
	CodexPostToolMarker = "hook codex post-tool"
)

// Matcher strings mirrored from ralph bundle/.codex/hooks.json.
const (
	codexBashMatcher    = "Bash"
	codexCommandMatcher = "command_execution"
)

// Default PreToolUse / PostToolUse timeout seconds (ralph bundle/.codex/hooks.json).
const codexHookTimeout = 30

var (
	codexPrePassthrough = []byte(`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}`)
	codexPostPassthrough = []byte(`{}`)
)

// Codex is the OpenAI Codex CLI adapter.
//
// Spike (docs/AGENT-CAPABILITIES.md, ralph bundle/.codex/hooks/): PreToolUse
// Bash / command_execution rewrite via updatedInput.command is proven
// (Codex CLI 0.136.0). PostToolUse output replacement is unproven — post-tool
// is telemetry-only; compaction comes from the `jevkit exec --` wrapper.
type Codex struct {
	// Binary is the jevkit executable name/path used in rewrites and install
	// commands. Empty means "jevkit".
	Binary string
}

func init() {
	Register(NewCodex())
}

// NewCodex returns a Codex adapter.
func NewCodex() *Codex {
	return &Codex{}
}

func (c *Codex) Name() string { return CodexName }

func (c *Codex) Capabilities() Capabilities {
	return Capabilities{
		PreTool:        true,
		PreToolRewrite: true,
		// PostTool is true so Install can register a telemetry hook; the
		// framework AppendHook records the invocation. OutputReplace is false:
		// model-visible PostToolUse mutation is unproven.
		PostTool:      true,
		OutputReplace: false,
	}
}

func (c *Codex) Passthrough(event Event) []byte {
	switch event {
	case EventPreTool:
		return append([]byte(nil), codexPrePassthrough...)
	default:
		return append([]byte(nil), codexPostPassthrough...)
	}
}

// HandlePreTool rewrites Bash / command_execution commands to
// `jevkit exec -- <cmd>` so the wrapper sees the real exit code and can
// compact. Non-shell tools and already-rewritten commands fail open with
// permissionDecision allow.
func (c *Codex) HandlePreTool(ctx context.Context, req Request) (Response, error) {
	passthrough := Response{Body: c.Passthrough(EventPreTool)}

	var payload codexPrePayload
	if err := json.Unmarshal(req.Raw, &payload); err != nil {
		return passthrough, nil
	}
	if payload.HookEventName != "" && payload.HookEventName != "PreToolUse" {
		return passthrough, nil
	}
	if !isCodexShellTool(payload.ToolName) {
		return passthrough, nil
	}
	command := strings.TrimSpace(payload.ToolInput.Command)
	if command == "" {
		return passthrough, nil
	}

	rewritten := rewriteCodexCommand(c.binary(), command)
	body, err := marshalCodexPreRewrite(rewritten)
	if err != nil {
		return passthrough, nil
	}
	return Response{Body: body}, nil
}

// HandlePostTool is telemetry-only. The framework records the invocation when
// the hook fires; we never mutate output (SPIKE: unproven).
func (c *Codex) HandlePostTool(ctx context.Context, req Request) (Response, error) {
	return Response{Body: c.Passthrough(EventPostTool)}, nil
}

func (c *Codex) HandleStop(ctx context.Context, req Request) (Response, error) {
	return Response{Body: c.Passthrough(EventStop)}, nil
}

func (c *Codex) binary() string {
	if c != nil && strings.TrimSpace(c.Binary) != "" {
		return strings.TrimSpace(c.Binary)
	}
	return "jevkit"
}

type codexPrePayload struct {
	HookEventName string `json:"hook_event_name"`
	ToolName      string `json:"tool_name"`
	ToolInput     struct {
		Command string `json:"command"`
	} `json:"tool_input"`
}

func marshalCodexPreRewrite(command string) ([]byte, error) {
	resp := struct {
		HookSpecificOutput struct {
			HookEventName      string `json:"hookEventName"`
			PermissionDecision string `json:"permissionDecision"`
			UpdatedInput       struct {
				Command string `json:"command"`
			} `json:"updatedInput"`
		} `json:"hookSpecificOutput"`
	}{}
	resp.HookSpecificOutput.HookEventName = "PreToolUse"
	resp.HookSpecificOutput.PermissionDecision = "allow"
	resp.HookSpecificOutput.UpdatedInput.Command = command
	return json.Marshal(resp)
}

func isCodexShellTool(tool string) bool {
	switch strings.TrimSpace(tool) {
	case codexBashMatcher, codexCommandMatcher:
		return true
	default:
		return false
	}
}

func rewriteCodexCommand(binary, command string) string {
	if alreadyCodexJevkitExec(command, binary) {
		return command
	}
	return binary + " exec -- " + command
}

func alreadyCodexJevkitExec(command, binary string) bool {
	if strings.Contains(command, binary+" exec -- ") {
		return true
	}
	return strings.Contains(command, "jevkit exec -- ") ||
		strings.Contains(command, "jevkit.exe exec -- ")
}
