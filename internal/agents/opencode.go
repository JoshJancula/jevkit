package agents

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	"github.com/OWNER/jevkit/internal/compact"
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
type OpenCode struct {
	// Binary is the jevkit executable name/path used in rewrites and the
	// staged plugin default. Empty means "jevkit".
	Binary         string
	Getenv         func(string) string
	ThresholdBytes int
	Asker          compact.Asker
	StateDir       string
	Policy         *compact.Policy
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
		PostTool:      true,
		OutputReplace: true,
	}
}

func (o *OpenCode) Passthrough(event Event) []byte {
	return append([]byte(nil), opencodeEmptyPassthrough...)
}

// HandlePreTool deliberately leaves input untouched.
func (o *OpenCode) HandlePreTool(ctx context.Context, req Request) (Response, error) {
	return Response{Body: o.Passthrough(EventPreTool)}, nil
}

func (o *OpenCode) HandlePostTool(ctx context.Context, req Request) (Response, error) {
	pass := Response{Body: o.Passthrough(EventPostTool)}
	if !o.compactEnabled() {
		return pass, nil
	}
	var payload openCodePostPayload
	if json.Unmarshal(req.Raw, &payload) != nil || !isShellTool(payload.Input.Tool) {
		return pass, nil
	}
	pointer, _ := storeRawResult(o.StateDir, OpenCodeName, payload.Output.Output)
	exit, authoritative := 0, false
	if payload.Output.Status == "completed" {
		authoritative = true
	}
	if payload.Output.Status == "error" {
		exit, authoritative = 1, true
	}
	_, result := compact.JevCompact(payload.Input.Args.Command, payload.Output.Output, "", exit, o.Asker, compact.JevOptions{Enabled: true, Shadow: o.shadow(), ThresholdBytes: o.ThresholdBytes, StateDir: o.StateDir, RawPointer: pointer, AuthoritativeExit: authoritative, Policy: o.Policy, Runtime: OpenCodeName})
	if !result.Compacted || result.Stdout == "" || o.shadow() {
		return pass, nil
	}
	bodyText := result.Stdout
	if pointer != "" {
		bodyText = rawResultTrailer(bodyText, pointer)
	}
	body, err := json.Marshal(map[string]string{"output": bodyText})
	if err != nil {
		return pass, nil
	}
	return Response{Body: body}, nil
}

type openCodePostPayload struct {
	Input struct {
		Tool string `json:"tool"`
		Args struct {
			Command string `json:"command"`
		} `json:"args"`
	} `json:"input"`
	Output struct {
		Output string `json:"output"`
		Status string `json:"status"`
	} `json:"output"`
}

func isShellTool(tool string) bool {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "bash", "shell", "command_execution":
		return true
	}
	return false
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

func (o *OpenCode) getenv(key string) string {
	if o != nil && o.Getenv != nil {
		return o.Getenv(key)
	}
	return os.Getenv(key)
}
func (o *OpenCode) compactEnabled() bool {
	return compactEnvEnabled(o.getenv, "JEVKIT_COMPACT")
}
func (o *OpenCode) shadow() bool {
	return compactEnvEnabled(o.getenv, "JEVKIT_COMPACT_SHADOW")
}
