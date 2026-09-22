package agents

import (
	"context"
	"encoding/json"
	"time"
)

// ProtocolVersion is the stdin JSON protocol this build speaks. A payload that
// declares a different version fails open (passthrough, exit 0).
const ProtocolVersion = 1

// DefaultTimeout is the hard upper bound for one hook dispatch. Hooks fire on
// every tool call, so adapters must stay well under this.
const DefaultTimeout = 5 * time.Second

// Event is a normalized runtime event used by installed integrations.
type Event string

const (
	EventPreTool  Event = "pre-tool"
	EventPostTool Event = "post-tool"
	EventStop     Event = "stop"
)

// Capabilities describes what an adapter supports.
type Capabilities struct {
	// PreTool is true when HandlePreTool may rewrite or decide on a tool call.
	PreTool bool
	// PostTool is true when HandlePostTool may replace or observe tool output.
	PostTool bool
	// Stop is true when HandleStop may block or continue a stop event.
	Stop bool
	// OutputReplace is true when PostTool can replace model-visible output.
	OutputReplace bool
	// PreToolRewrite is true when PreTool can rewrite the shell command into
	// `jevkit exec -- ...`.
	PreToolRewrite bool
}

// Request is one stdin payload delivered to an adapter.
type Request struct {
	// Raw is the original stdin bytes (valid JSON when the runner reached the
	// adapter; garbage never reaches adapters).
	Raw json.RawMessage
	// Event is the normalized CLI event.
	Event Event
	// ProtocolVersion is the version declared by the payload, or
	// ProtocolVersion when the field is absent.
	ProtocolVersion int
}

// Response is the adapter's answer. Body is written to stdout as JSON.
// ExitCode is used only for a deliberate deny; fail-open paths always force 0.
type Response struct {
	Body []byte
	// Deny marks a deliberate policy denial (as opposed to a fail-open
	// passthrough). When true and ExitCode is 0, the process still exits 0
	// and the body carries the deny for agents that encode it in JSON.
	Deny bool
	// ExitCode overrides the process exit code for a deliberate deny. Zero
	// keeps the fail-open default of exit 0.
	ExitCode int
}

// FilePreview is one planned or applied file edit for dry-run diffs and
// install reporting.
type FilePreview struct {
	Path   string
	Before []byte
	After  []byte
}

// InstallOptions configures Install/Uninstall of an agent's hook config.
type InstallOptions struct {
	// WorkDir is the project root (project-scoped config).
	WorkDir string
	// ConfigDir is the user config home (user-scoped config).
	ConfigDir string
	// Scope is "project" or "user"; empty means project when WorkDir is set.
	Scope string
	// Binary is the absolute path to the jevkit binary to register.
	Binary string
	// DryRun reports the planned edit without writing.
	DryRun bool
	// Preview receives each planned file edit (hooks and, when used by the
	// installer, MCP configs). Called on dry-run and on write.
	Preview func(FilePreview)
}

// Components selects independently installable integration pieces. Plugin is
// a user-facing bundle alias that resolves to hooks plus MCP; it is not passed
// to individual adapters.
type Components struct {
	Hooks bool
	MCP   bool
}

func DefaultComponents() Components { return Components{Hooks: true, MCP: true} }

// Agent is one coding-agent adapter.
type Agent interface {
	Name() string
	Capabilities() Capabilities
	HandlePreTool(ctx context.Context, req Request) (Response, error)
	HandlePostTool(ctx context.Context, req Request) (Response, error)
	HandleStop(ctx context.Context, req Request) (Response, error)
	// Passthrough is the fail-open JSON body for event (empty/allow). It must
	// always be valid JSON the agent accepts as "do nothing".
	Passthrough(event Event) []byte
	Install(opts InstallOptions) error
	Uninstall(opts InstallOptions) error
}
