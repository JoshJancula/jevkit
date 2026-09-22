package agents

import (
	"context"
	"strings"
)

// Codex adapter name used by installed runtime integrations.
const CodexName = "codex"

// CodexHookMarker is the idempotency substring for managed entries in
// .codex/hooks.json. Installer matching is substring-based so a binary-path
// change still replaces the prior entry instead of duplicating.
const CodexHookMarker = "_runtime dispatch --protocol 1 codex"
const legacyCodexHookMarker = "hook codex"

// CodexPreToolMarker / CodexPostToolMarker are the full event tokens.
const (
	// CodexPreToolMarker remains only for recognizing legacy config during an
	// upgrade; new installations never emit it.
	CodexPreToolMarker  = "hook codex pre-tool"
	CodexPostToolMarker = "_runtime dispatch --protocol 1 codex post-tool"
)

// Matcher strings mirrored from ralph bundle/.codex/hooks.json.
const (
	codexBashMatcher    = "Bash"
	codexCommandMatcher = "command_execution"
)

// Default PreToolUse / PostToolUse timeout seconds (ralph bundle/.codex/hooks.json).
const codexHookTimeout = 30

var (
	codexPrePassthrough  = []byte(`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}`)
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

// HandlePreTool deliberately leaves input untouched.
func (c *Codex) HandlePreTool(ctx context.Context, req Request) (Response, error) {
	return Response{Body: c.Passthrough(EventPreTool)}, nil
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
