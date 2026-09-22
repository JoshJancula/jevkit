package agents

import (
	"context"
	"strings"
)

// Antigravity adapter name used by installed runtime integrations.
const AntigravityName = "antigravity"

// AntigravityHookMarker is the idempotency substring for managed entries in
// .agents/hooks.json. Installer matching is substring-based so a binary-path
// change still replaces the prior entry instead of duplicating.
const AntigravityHookMarker = "_runtime dispatch --protocol 1 antigravity"
const legacyAntigravityHookMarker = "hook antigravity"

// AntigravityPreToolMarker is retained only to recognize legacy config.
const AntigravityPreToolMarker = "hook antigravity pre-tool"

// AntigravityPostToolMarker is the full post-tool event token.
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
		// PostToolUse has no replaceable result, but it does include the error
		// field needed for privacy-safe telemetry.
		PostTool:      true,
		OutputReplace: false,
	}
}

func (a *Antigravity) Passthrough(event Event) []byte {
	// agy requires a JSON decision on stdout; empty output is not allow.
	return append([]byte(nil), antigravityAllowPassthrough...)
}

// HandlePreTool deliberately leaves input untouched.
func (a *Antigravity) HandlePreTool(ctx context.Context, req Request) (Response, error) {
	return Response{Body: a.Passthrough(EventPreTool)}, nil
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
