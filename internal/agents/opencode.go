package agents

import (
	"context"
	"strings"
)

// OpenCode adapter name used by the installed plugin.
const OpenCodeName = "opencode"

// OpenCodePluginFile is the staged plugin basename under .opencode/plugins/.
const OpenCodePluginFile = "jevkit-runtime-hooks.ts"

// OpenCodePluginMarker is the idempotency / ownership token embedded in the
// staged TypeScript plugin. Installer matching is substring-based.
const OpenCodePluginMarker = "JEVKIT_OPENCODE_PLUGIN"

// OpenCodeHookMarker identifies the plugin's private runtime dispatcher call.
const OpenCodeHookMarker = "_runtime dispatch --protocol 1 opencode"

var opencodeEmptyPassthrough = []byte(`{}`)

// OpenCode is the OpenCode adapter.
//
// Not a settings-file stdin hook: Install stages a TypeScript plugin under
// .opencode/plugins/ that shells out to `jevkit hook opencode ...`. The Go
// handlers below are what that plugin invokes.
//
// Spike (docs/AGENT-CAPABILITIES.md, ralph SPIKE-output-mutation.md):
// tool.execute.before can mutate output.args.command (rewrite path).
// tool.execute.after output mutation is unproven on OpenCode 1.14.35 — do not
// rely on it; compaction comes from the `jevkit exec --` wrapper rewrite.
type OpenCode struct {
	// Binary is the jevkit executable name/path used in rewrites and the
	// staged plugin default. Empty means "jevkit".
	Binary string
}

func init() {
	Register(NewOpenCode())
}

// NewOpenCode returns an OpenCode adapter.
func NewOpenCode() *OpenCode {
	return &OpenCode{}
}

func (o *OpenCode) Name() string { return OpenCodeName }

func (o *OpenCode) Capabilities() Capabilities {
	return Capabilities{
		// PostTool is true so the plugin can shell out for telemetry; the
		// framework AppendHook records the invocation. OutputReplace is false:
		// model-visible after-hook mutation is unproven (SPIKE).
		PostTool:      true,
		OutputReplace: false,
	}
}

func (o *OpenCode) Passthrough(event Event) []byte {
	return append([]byte(nil), opencodeEmptyPassthrough...)
}

// HandlePreTool deliberately leaves input untouched.
func (o *OpenCode) HandlePreTool(ctx context.Context, req Request) (Response, error) {
	return Response{Body: o.Passthrough(EventPreTool)}, nil
}

// HandlePostTool is telemetry-only. The framework records the invocation when
// the plugin shells out; we never mutate output (SPIKE: unproven).
func (o *OpenCode) HandlePostTool(ctx context.Context, req Request) (Response, error) {
	return Response{Body: o.Passthrough(EventPostTool)}, nil
}

func (o *OpenCode) HandleStop(ctx context.Context, req Request) (Response, error) {
	return Response{Body: o.Passthrough(EventStop)}, nil
}

func (o *OpenCode) binary() string {
	if o != nil && strings.TrimSpace(o.Binary) != "" {
		return strings.TrimSpace(o.Binary)
	}
	return "jevkit"
}
