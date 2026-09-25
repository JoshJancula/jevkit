package agents

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	"github.com/OWNER/jevkit/internal/compact"
	"github.com/OWNER/jevkit/internal/registry"
	"github.com/OWNER/jevkit/internal/security/config"
)

// Cursor adapter name used by installed runtime integrations.
const CursorName = "cursor"

// CursorHookMarker is the idempotency substring for managed entries in
// .cursor/hooks.json. Installer matching is substring-based so a binary-path
// change still replaces the prior entry instead of duplicating.
const CursorHookMarker = "_runtime dispatch --protocol 1 cursor"
const legacyCursorHookMarker = "hook cursor"

// CursorPreToolMarker / CursorPostToolMarker are the full event tokens.
const (
	CursorPreToolMarker  = "_runtime dispatch --protocol 1 cursor pre-tool"
	CursorPostToolMarker = "_runtime dispatch --protocol 1 cursor post-tool"
)

// Matcher strings mirrored from ralph bundle/.cursor/hooks.json.
const (
	cursorShellMatcher  = "Shell"
	cursorNativeMatcher = "Read|read|readToolCall|Grep|grep|grepToolCall|Glob|glob|globToolCall|SemanticSearch|semanticSearch"
	cursorMCPMatcher    = "MCP:*"
)

var (
	cursorPrePassthrough  = []byte(`{"permission":"allow"}`)
	cursorPostPassthrough = []byte("{}")
)

// Cursor is the Cursor IDE / cursor-agent adapter.
//
// Spike (docs/AGENT-CAPABILITIES.md): preToolUse Shell rewrite via
// updated_input.command is agent-visible. Shell postToolUse updated_tool_output
// is NOT agent-visible (telemetry only). Native Read/Grep/Glob and MCP:*
// results may be replaced via updated_tool_output / updated_mcp_tool_output.
type Cursor struct {
	// Binary is the jevkit executable name/path used in rewrites and install
	// commands. Empty means "jevkit".
	Binary string
	// Getenv reads process env; nil means os.Getenv. Gates:
	// JEVKIT_COMPACT=1 enables post-tool compaction; JEVKIT_COMPACT_SHADOW=1
	// passes through post-tool replacements (pre-tool rewrite still applies).
	Getenv func(string) string
	// ThresholdBytes overrides the compact threshold; zero uses the default.
	ThresholdBytes int
	// Asker is the optional jev ranked-line tier; nil is deterministic-only.
	Asker compact.Asker
	// StateDir is forwarded to compact shadow logging.
	StateDir        string
	Policy          *compact.Policy
	Security        *config.Config
	SecurityDecider *registry.Decider
}

func init() {
	Register(NewCursor())
}

// NewCursor returns a Cursor adapter wired to the process environment.
func NewCursor() *Cursor {
	return &Cursor{}
}

func (c *Cursor) Name() string { return CursorName }

func (c *Cursor) Capabilities() Capabilities {
	return Capabilities{
		PreTool:        true,
		PreToolRewrite: true,
		PostTool:       true,
		OutputReplace:  true, // native + MCP; Shell post is telemetry-only
	}
}

func (c *Cursor) Passthrough(event Event) []byte {
	switch event {
	case EventPreTool:
		return append([]byte(nil), cursorPrePassthrough...)
	default:
		return append([]byte(nil), cursorPostPassthrough...)
	}
}

func (c *Cursor) HandlePreTool(ctx context.Context, req Request) (Response, error) {
	var payload struct {
		HookEventName string `json:"hook_event_name"`
		ToolName      string `json:"tool_name"`
		ToolInput     struct {
			Command string `json:"command"`
		} `json:"tool_input"`
		WorkspaceRoots []string `json:"workspace_roots"`
		CWD            string   `json:"cwd"`
	}
	if err := json.Unmarshal(req.Raw, &payload); err != nil ||
		!strings.EqualFold(payload.HookEventName, "preToolUse") || payload.ToolName != "Shell" ||
		strings.TrimSpace(payload.ToolInput.Command) == "" || IsShellWrapperCommand(payload.ToolInput.Command) {
		return Response{Body: c.Passthrough(EventPreTool)}, nil
	}
	workspace := payload.CWD
	if len(payload.WorkspaceRoots) > 0 && strings.TrimSpace(payload.WorkspaceRoots[0]) != "" {
		workspace = payload.WorkspaceRoots[0]
	}
	if strings.TrimSpace(workspace) == "" {
		return Response{Body: c.Passthrough(EventPreTool)}, nil
	}
	if response, deny := securityDecision(ctx, c.Security, c.SecurityDecider, payload.ToolInput.Command, payload.CWD, workspace, CursorName); deny {
		return response, nil
	}
	body, err := json.Marshal(map[string]any{
		"permission":    "allow",
		"updated_input": map[string]string{"command": buildSecurityShellWrapperCommand(c.binary(), workspace, payload.ToolInput.Command, CursorName, c.Security != nil && c.Security.Yolo, securityPolicyName(c.Security))},
	})
	if err != nil {
		return Response{Body: c.Passthrough(EventPreTool)}, nil
	}
	return Response{Body: body}, nil
}

// HandlePostTool handles Shell and native tools as telemetry only. Cursor's
// documented replacement field is updated_mcp_tool_output, so only MCP tool
// results may be compacted here.
//
// Everything else fails open with `{}`.
func (c *Cursor) HandlePostTool(ctx context.Context, req Request) (Response, error) {
	passthrough := Response{Body: c.Passthrough(EventPostTool)}

	var payload cursorPostPayload
	if err := json.Unmarshal(req.Raw, &payload); err != nil {
		return passthrough, nil
	}
	event := payload.HookEventName
	if event == "" {
		event = payload.HookEventNameCamel
	}
	// afterShellExecution is registered as a telemetry hook calling post-tool;
	// it never carries replaceable tool output.
	if strings.EqualFold(event, "afterShellExecution") {
		return passthrough, nil
	}
	if event != "" && !strings.EqualFold(event, "postToolUse") && event != "PostToolUse" {
		return passthrough, nil
	}

	tool := payload.ToolName
	if tool == "Shell" {
		// Observability only: Shell updated_tool_output is not agent-visible.
		return passthrough, nil
	}

	if !c.compactEnabled() {
		return passthrough, nil
	}

	switch {
	case isCursorMCPTool(tool):
		return c.compactMCPResult(passthrough, payload)
	default:
		return passthrough, nil
	}
}

func (c *Cursor) HandleStop(ctx context.Context, req Request) (Response, error) {
	return Response{Body: c.Passthrough(EventStop)}, nil
}

func (c *Cursor) compactMCPResult(passthrough Response, payload cursorPostPayload) (Response, error) {
	text, output, ok := extractCursorToolOutput(payload.ToolOutput)
	if !ok {
		return passthrough, nil
	}
	compacted, ok := c.compactText(payload.ToolName, text)
	if !ok {
		return passthrough, nil
	}
	updated, err := replaceCursorToolOutputText(output, compacted)
	if err != nil {
		return passthrough, nil
	}
	body, err := json.Marshal(map[string]any{"updated_mcp_tool_output": updated})
	if err != nil {
		return passthrough, nil
	}
	return Response{Body: body}, nil
}

func (c *Cursor) compactText(toolName, text string) (string, bool) {
	pointer, err := storeRawResult(c.StateDir, CursorName, text)
	if err != nil {
		return "", false
	}
	opts := compact.JevOptions{
		Enabled:        true,
		Shadow:         c.shadow(),
		ThresholdBytes: c.ThresholdBytes,
		StateDir:       c.StateDir,
		RawPointer:     pointer,
		Runtime:        CursorName,
		Policy:         c.Policy,
	}
	_, result := compact.JevCompact(toolName, text, "", 0, c.Asker, opts)
	if !result.Compacted || c.shadow() {
		return "", false
	}
	out := result.Stdout
	if out == "" {
		out = result.Stderr
	}
	if out == "" || out == text {
		return "", false
	}
	return rawResultTrailer(out, pointer), true
}

func (c *Cursor) binary() string {
	if c != nil && strings.TrimSpace(c.Binary) != "" {
		return strings.TrimSpace(c.Binary)
	}
	return "jevkit"
}

func (c *Cursor) getenv(key string) string {
	if c != nil && c.Getenv != nil {
		return c.Getenv(key)
	}
	return os.Getenv(key)
}

func (c *Cursor) compactEnabled() bool {
	return compactEnvEnabled(c.getenv, "JEVKIT_COMPACT")
}

func (c *Cursor) shadow() bool {
	return compactEnvEnabled(c.getenv, "JEVKIT_COMPACT_SHADOW")
}

type cursorPostPayload struct {
	HookEventName      string          `json:"hook_event_name"`
	HookEventNameCamel string          `json:"hookEventName"`
	ToolName           string          `json:"tool_name"`
	ToolInput          json.RawMessage `json:"tool_input"`
	ToolOutput         json.RawMessage `json:"tool_output"`
}

func isCursorMCPTool(name string) bool {
	n := strings.TrimSpace(name)
	return strings.HasPrefix(n, "MCP:") ||
		strings.HasPrefix(n, "mcp__") ||
		strings.HasPrefix(strings.ToLower(n), "mcp:")
}

// extractCursorToolOutput pulls the model-visible text from a Cursor
// tool_output value. tool_output may be a JSON object, a JSON string that
// itself encodes an object, or a bare string.
func extractCursorToolOutput(raw json.RawMessage) (text string, output json.RawMessage, ok bool) {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil, false
	}

	// Double-encoded JSON string.
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		asString = strings.TrimSpace(asString)
		if asString == "" {
			return "", nil, false
		}
		if json.Valid([]byte(asString)) && (asString[0] == '{' || asString[0] == '[') {
			return extractCursorToolOutput(json.RawMessage(asString))
		}
		return asString, json.RawMessage(mustJSONString(asString)), true
	}

	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "", nil, false
	}
	text, ok = cursorOutputText(obj)
	if !ok {
		return "", nil, false
	}
	return text, raw, true
}

func cursorOutputText(obj map[string]any) (string, bool) {
	if s, ok := obj["content"].(string); ok && s != "" {
		return s, true
	}
	if arr, ok := obj["content"].([]any); ok && len(arr) > 0 {
		if m, ok := arr[0].(map[string]any); ok {
			if s, ok := m["text"].(string); ok && s != "" {
				return s, true
			}
		}
	}
	if file, ok := obj["file"].(map[string]any); ok {
		if s, ok := file["content"].(string); ok && s != "" {
			return s, true
		}
	}
	if success, ok := obj["success"].(map[string]any); ok {
		if s, ok := success["content"].(string); ok && s != "" {
			return s, true
		}
	}
	if result, ok := obj["result"].(map[string]any); ok {
		if success, ok := result["success"].(map[string]any); ok {
			if s, ok := success["content"].(string); ok && s != "" {
				return s, true
			}
		}
	}
	if s, ok := obj["text"].(string); ok && s != "" {
		return s, true
	}
	if s, ok := obj["output"].(string); ok && s != "" {
		return s, true
	}
	if s, ok := obj["stdout"].(string); ok && s != "" {
		return s, true
	}
	return "", false
}

func replaceCursorToolOutputText(original json.RawMessage, text string) (any, error) {
	var obj map[string]any
	if err := json.Unmarshal(original, &obj); err != nil {
		// Bare string output: wrap as content object.
		return map[string]any{"content": text}, nil
	}
	switch {
	case hasStringField(obj, "content"):
		obj["content"] = text
	case hasContentArray(obj):
		arr, _ := obj["content"].([]any)
		if len(arr) == 0 {
			obj["content"] = []any{map[string]any{"type": "text", "text": text}}
		} else if m, ok := arr[0].(map[string]any); ok {
			m["text"] = text
			arr[0] = m
			obj["content"] = arr
		} else {
			obj["content"] = []any{map[string]any{"type": "text", "text": text}}
		}
	case hasNestedString(obj, "file", "content"):
		file, _ := obj["file"].(map[string]any)
		file["content"] = text
		obj["file"] = file
	case hasNestedString(obj, "success", "content"):
		success, _ := obj["success"].(map[string]any)
		success["content"] = text
		obj["success"] = success
	case obj["stdout"] != nil:
		obj["stdout"] = text
	default:
		obj["content"] = text
	}
	return obj, nil
}

func hasStringField(obj map[string]any, key string) bool {
	_, ok := obj[key].(string)
	return ok
}

func hasContentArray(obj map[string]any) bool {
	_, ok := obj["content"].([]any)
	return ok
}

func hasNestedString(obj map[string]any, a, b string) bool {
	inner, ok := obj[a].(map[string]any)
	if !ok {
		return false
	}
	_, ok = inner[b].(string)
	return ok
}

func mustJSONString(s string) []byte {
	b, err := json.Marshal(s)
	if err != nil {
		return []byte(`""`)
	}
	return b
}
