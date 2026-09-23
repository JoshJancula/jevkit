package agents

// ReplacementScope describes the narrowest model-visible result replacement
// contract supported by a runtime's documented post-tool API. It deliberately
// does not infer replacement from a pre-tool input rewrite.
type ReplacementScope string

const (
	ReplacementNone       ReplacementScope = "none"
	ReplacementMCPOnly    ReplacementScope = "mcp-only"
	ReplacementToolShapes ReplacementScope = "validated-tool-shapes"
)

// RuntimeCapability is the evidence-backed contract used to decide what an
// adapter may do. Evidence dates identify when the vendor documentation was
// reviewed; they are not a claim that an unpinned runtime API is immutable.
type RuntimeCapability struct {
	Name               string
	Evidence           string
	PostToolOutput     bool
	AuthoritativeState string
	Replacement        ReplacementScope
	ContextInjection   bool
	Telemetry          bool
	Installation       string
}

// RuntimeCapabilities is intentionally separate from an adapter's legacy
// Capabilities flags. The latter describe implemented dispatch events; this
// table limits what those events may do as the integrations are revised.
func RuntimeCapabilities(name string) (RuntimeCapability, bool) {
	capability, ok := runtimeCapabilities[name]
	return capability, ok
}

var runtimeCapabilities = map[string]RuntimeCapability{
	ClaudeName: {
		Name:               ClaudeName,
		Evidence:           "Claude Code hooks reference reviewed 2026-09-22",
		PostToolOutput:     true,
		AuthoritativeState: "event: PostToolUse is success; PostToolUseFailure is failure",
		Replacement:        ReplacementToolShapes,
		ContextInjection:   true,
		Telemetry:          true,
		Installation:       "plugin hooks/hooks.json or Claude settings",
	},
	CursorName: {
		Name:               CursorName,
		Evidence:           "Cursor hooks reference reviewed 2026-09-22",
		PostToolOutput:     true,
		AuthoritativeState: "postToolUse success payload or postToolUseFailure failure event",
		Replacement:        ReplacementMCPOnly,
		ContextInjection:   true,
		Telemetry:          true,
		Installation:       "plugin hooks/hooks.json or .cursor/hooks.json",
	},
	CodexName: {
		Name:               CodexName,
		Evidence:           "OpenAI Codex hooks reference reviewed 2026-09-23",
		PostToolOutput:     true,
		AuthoritativeState: "PostToolUse receives Bash output but no documented exit status",
		Replacement:        ReplacementToolShapes,
		ContextInjection:   true,
		Telemetry:          true,
		Installation:       "plugin hooks or .codex/hooks.json",
	},
	OpenCodeName: {
		Name:               OpenCodeName,
		Evidence:           "OpenCode v2 plugin reference reviewed 2026-09-22",
		PostToolOutput:     true,
		AuthoritativeState: "execute.after status completed or error",
		Replacement:        ReplacementToolShapes,
		ContextInjection:   false,
		Telemetry:          true,
		Installation:       "OpenCode plugin",
	},
	AntigravityName: {
		Name:               AntigravityName,
		Evidence:           "Google Antigravity hooks reference reviewed 2026-09-22",
		PostToolOutput:     false,
		AuthoritativeState: "PostToolUse error field only",
		Replacement:        ReplacementNone,
		ContextInjection:   false,
		Telemetry:          true,
		Installation:       ".agents/hooks.json",
	},
}
