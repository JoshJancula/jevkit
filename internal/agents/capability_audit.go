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
	PreToolDecision    bool
	Evidence           string
	PostToolOutput     bool
	AuthoritativeState string
	Replacement        ReplacementScope
	ContextInjection   bool
	Telemetry          bool
	Installation       string
	// ShellPolicyCoverage is true when the installed pre-tool hook actually
	// inspects and can rewrite an eligible shell call before it runs (see
	// IsShellWrapperCommand / buildSecurityShellWrapperCommand call sites).
	// OpenCode's plugin never receives a pre-tool event (HandlePreTool is a
	// documented no-op; see opencode.go and docs/AGENT-REFERENCE.md), so its
	// shell calls are never policy-checked in this version. This must stay in
	// sync with PreToolDecision for every runtime that claims shell coverage;
	// see TestRuntimeCapabilityAuditMatchesAdapterClaims.
	ShellPolicyCoverage bool
	InjectionGuard      bool
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
		Name:                ClaudeName,
		ShellPolicyCoverage: true,
		InjectionGuard:      true,
		PreToolDecision:     true,
		Evidence:            "Claude Code hooks reference reviewed 2026-09-22",
		PostToolOutput:      true,
		AuthoritativeState:  "event: PostToolUse is success; PostToolUseFailure is failure",
		Replacement:         ReplacementToolShapes,
		ContextInjection:    true,
		Telemetry:           true,
		Installation:        "plugin hooks/hooks.json or Claude settings",
	},
	CursorName: {
		Name:                CursorName,
		ShellPolicyCoverage: true,
		InjectionGuard:      true,
		PreToolDecision:     true,
		Evidence:            "Cursor hooks reference reviewed 2026-09-22",
		PostToolOutput:      true,
		AuthoritativeState:  "postToolUse success payload or postToolUseFailure failure event",
		Replacement:         ReplacementMCPOnly,
		ContextInjection:    true,
		Telemetry:           true,
		Installation:        "plugin hooks/hooks.json or .cursor/hooks.json",
	},
	CodexName: {
		Name:                CodexName,
		ShellPolicyCoverage: true,
		InjectionGuard:      true,
		PreToolDecision:     true,
		Evidence:            "OpenAI Codex hooks reference reviewed 2026-09-23",
		PostToolOutput:      true,
		AuthoritativeState:  "PostToolUse receives Bash output but no documented exit status",
		Replacement:         ReplacementToolShapes,
		ContextInjection:    true,
		Telemetry:           true,
		Installation:        "plugin hooks or .codex/hooks.json",
	},
	OpenCodeName: {
		Name:                OpenCodeName,
		ShellPolicyCoverage: false,
		Evidence:            "OpenCode v2 plugin reference reviewed 2026-09-22",
		PostToolOutput:      true,
		AuthoritativeState:  "execute.after status completed or error",
		Replacement:         ReplacementToolShapes,
		ContextInjection:    false,
		Telemetry:           true,
		Installation:        "OpenCode plugin",
	},
	AntigravityName: {
		Name:                AntigravityName,
		ShellPolicyCoverage: true,
		InjectionGuard:      true,
		PreToolDecision:     true,
		Evidence:            "Google Antigravity hooks reference reviewed 2026-09-22",
		PostToolOutput:      false,
		AuthoritativeState:  "PostToolUse error field only",
		Replacement:         ReplacementNone,
		ContextInjection:    false,
		Telemetry:           false,
		Installation:        ".agents/hooks.json",
	},
}
