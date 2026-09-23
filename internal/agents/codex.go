package agents

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	"github.com/OWNER/jevkit/internal/compact"
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
// PostToolUse can replace Codex's model-visible result with hook feedback via
// continue:false. It cannot mutate Bash output in place, so this adapter emits
// an already-compacted local result as its stop reason. Any untrusted decision
// leaves the original result alone.
type Codex struct {
	// Binary is the jevkit executable name/path used in rewrites and install
	// commands. Empty means "jevkit".
	Binary string
	// Getenv reads process env; nil means os.Getenv. JEVKIT_COMPACT enables
	// result replacement, while JEVKIT_COMPACT_SHADOW preserves original output.
	Getenv func(string) string
	// ThresholdBytes overrides the compaction threshold; zero uses the default.
	ThresholdBytes int
	// Asker is the optional Jev ranked-line tier. A nil or unavailable client
	// fails open and preserves Codex's original tool result.
	Asker    compact.Asker
	StateDir string
	Policy   *compact.Policy
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
		PostTool:      true,
		OutputReplace: true,
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

// HandlePostTool replaces only eligible, high-confidence Bash output. Codex's
// PostToolUse payload does not provide a documented exit-status field, so Jev
// classifies the observed outcome rather than treating the local zero value as
// authoritative.
func (c *Codex) HandlePostTool(ctx context.Context, req Request) (Response, error) {
	passthrough := Response{Body: c.Passthrough(EventPostTool)}
	if !c.compactEnabled() || c.shadow() {
		return passthrough, nil
	}
	var payload codexPostPayload
	if err := json.Unmarshal(req.Raw, &payload); err != nil {
		return passthrough, nil
	}
	if !strings.EqualFold(payload.HookEventName, "PostToolUse") ||
		(payload.ToolName != codexBashMatcher && payload.ToolName != codexCommandMatcher) ||
		payload.ToolResponse == nil {
		return passthrough, nil
	}
	result, compacted := compact.JevCompact(
		payload.ToolInput.Command,
		payload.ToolResponse.Stdout,
		payload.ToolResponse.Stderr,
		0,
		c.Asker,
		compact.JevOptions{
			Enabled:        true,
			ThresholdBytes: c.ThresholdBytes,
			StateDir:       c.StateDir,
			CanReplace:     true,
			Policy:         c.Policy,
		},
	)
	if !compacted.Compacted {
		return passthrough, nil
	}
	feedback := result.Body
	if feedback == "" {
		feedback = compacted.Stdout
	}
	if feedback == "" {
		return passthrough, nil
	}
	return Response{Body: codexReplacement(feedback)}, nil
}

type codexPostPayload struct {
	HookEventName string `json:"hook_event_name"`
	ToolName      string `json:"tool_name"`
	ToolInput     struct {
		Command string `json:"command"`
	} `json:"tool_input"`
	ToolResponse *struct {
		Stdout string `json:"stdout"`
		Stderr string `json:"stderr"`
	} `json:"tool_response"`
}

func codexReplacement(feedback string) []byte {
	body, err := json.Marshal(struct {
		Continue   bool   `json:"continue"`
		StopReason string `json:"stopReason"`
	}{Continue: false, StopReason: feedback})
	if err != nil {
		return codexPostPassthrough
	}
	return body
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

func (c *Codex) getenv(key string) string {
	if c != nil && c.Getenv != nil {
		return c.Getenv(key)
	}
	return os.Getenv(key)
}

func (c *Codex) compactEnabled() bool {
	v := c.getenv("JEVKIT_COMPACT")
	return v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes") || strings.EqualFold(v, "on")
}

func (c *Codex) shadow() bool {
	v := c.getenv("JEVKIT_COMPACT_SHADOW")
	return v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes") || strings.EqualFold(v, "on")
}
