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

// Claude adapter name used by installed runtime integrations.
const ClaudeName = "claude"

// Managed hook command token used as an idempotency marker inside
// .claude/settings.json. Installer matching is substring-based on this token
// so a binary-path change still replaces the prior entry instead of duplicating.
// Full command is `<binary> _runtime dispatch --protocol 1 claude post-tool`.
const ClaudeHookMarker = "_runtime dispatch --protocol 1 claude post-tool"
const ClaudePreHookMarker = "_runtime dispatch --protocol 1 claude pre-tool"
const legacyClaudeHookMarker = "hook claude post-tool"

// claudePostPassthrough is the fail-open PostToolUse body (do nothing).
var claudePostPassthrough = []byte("{}")

// Claude is the Claude Code adapter.
//
// Spike (docs/AGENT-CAPABILITIES.md, testdata/hooks/claude/): PostToolUse:Bash
// payloads carry tool_response.{stdout,stderr,...} but no real exit code.
// Non-zero exits fire PostToolUseFailure without tool_response, so this
// adapter only replaces successful Bash output. Compaction assumes exit 0.
type Claude struct {
	// Getenv reads process env; nil means os.Getenv. Gates:
	// JEVKIT_COMPACT=1 enables compaction; JEVKIT_COMPACT_SHADOW=1 passes through.
	Getenv func(string) string
	// ThresholdBytes overrides the compact threshold; zero uses the default.
	ThresholdBytes int
	// Asker is the optional jev ranked-line tier; nil is deterministic-only.
	Asker compact.Asker
	// StateDir is forwarded to compact shadow logging (unused when shadow
	// passthrough skips compaction).
	StateDir        string
	Policy          *compact.Policy
	Security        *config.Config
	SecurityDecider *registry.Decider
}

func init() {
	Register(NewClaude())
}

// NewClaude returns a Claude adapter wired to the process environment.
func NewClaude() *Claude {
	return &Claude{}
}

func (c *Claude) Name() string { return ClaudeName }

func (c *Claude) Capabilities() Capabilities {
	return Capabilities{
		PreTool:       true,
		PostTool:      true,
		OutputReplace: true,
	}
}

func (c *Claude) Passthrough(event Event) []byte {
	switch event {
	case EventPreTool:
		return []byte(`{"decision":"allow"}`)
	default:
		return append([]byte(nil), claudePostPassthrough...)
	}
}

func (c *Claude) HandlePreTool(ctx context.Context, req Request) (Response, error) {
	var payload struct {
		HookEventName string `json:"hook_event_name"`
		ToolName      string `json:"tool_name"`
		CWD           string `json:"cwd"`
		ToolInput     struct {
			Command string `json:"command"`
		} `json:"tool_input"`
	}
	if json.Unmarshal(req.Raw, &payload) == nil && strings.EqualFold(payload.HookEventName, "PreToolUse") &&
		payload.ToolName == "Bash" && strings.TrimSpace(payload.ToolInput.Command) != "" {
		if response, deny := securityDecision(ctx, c.Security, c.SecurityDecider, payload.ToolInput.Command, payload.CWD, payload.CWD, ClaudeName); deny {
			return response, nil
		}
	}
	return Response{Body: c.Passthrough(EventPreTool)}, nil
}

func (c *Claude) HandleStop(ctx context.Context, req Request) (Response, error) {
	return Response{Body: c.Passthrough(EventStop)}, nil
}

// HandlePostTool compacts PostToolUse:Bash tool_response when jev compaction
// is enabled, the combined output is above the size threshold, and shadow mode
// is off. Everything else fails open with `{}`.
func (c *Claude) HandlePostTool(ctx context.Context, req Request) (Response, error) {
	passthrough := Response{Body: c.Passthrough(EventPostTool)}
	if !c.compactEnabled() {
		return passthrough, nil
	}

	var payload claudePostPayload
	if err := json.Unmarshal(req.Raw, &payload); err != nil {
		return passthrough, nil
	}
	if !strings.EqualFold(payload.HookEventName, "PostToolUse") || payload.ToolName != "Bash" {
		return passthrough, nil
	}
	if payload.ToolResponse == nil {
		return passthrough, nil
	}

	command := payload.ToolInput.Command
	stdout := payload.ToolResponse.Stdout
	stderr := payload.ToolResponse.Stderr
	pointer, err := storeRawResult(c.StateDir, ClaudeName, joinToolStreams(stdout, stderr))
	if err != nil {
		return passthrough, nil
	}
	// Spike confirmed PostToolUse has no exit code; success-only path uses 0.
	const exitStatus = 0

	opts := compact.JevOptions{
		Enabled:           true,
		Shadow:            c.shadow(),
		ThresholdBytes:    c.ThresholdBytes,
		StateDir:          c.StateDir,
		RawPointer:        pointer,
		Runtime:           ClaudeName,
		AuthoritativeExit: false,
		Policy:            c.Policy,
	}
	_, result := compact.JevCompact(command, stdout, stderr, exitStatus, c.Asker, opts)
	if !result.Compacted || c.shadow() {
		return passthrough, nil
	}
	body, err := marshalClaudeUpdatedOutput(rawResultTrailer(result.Stdout, pointer), result.Stderr, payload.ToolResponse)
	if err != nil {
		return passthrough, nil
	}
	return Response{Body: body}, nil
}

type claudePostPayload struct {
	HookEventName string `json:"hook_event_name"`
	ToolName      string `json:"tool_name"`
	ToolInput     struct {
		Command string `json:"command"`
	} `json:"tool_input"`
	ToolResponse *claudeToolResponse `json:"tool_response"`
}

type claudeToolResponse struct {
	Stdout           string `json:"stdout"`
	Stderr           string `json:"stderr"`
	Interrupted      bool   `json:"interrupted"`
	IsImage          bool   `json:"isImage"`
	NoOutputExpected bool   `json:"noOutputExpected"`
}

func marshalClaudeUpdatedOutput(stdout, stderr string, orig *claudeToolResponse) ([]byte, error) {
	out := claudeToolResponse{
		Stdout: stdout,
		Stderr: stderr,
	}
	if orig != nil {
		out.Interrupted = orig.Interrupted
		out.IsImage = orig.IsImage
		out.NoOutputExpected = orig.NoOutputExpected
	}
	resp := struct {
		HookSpecificOutput struct {
			HookEventName     string             `json:"hookEventName"`
			UpdatedToolOutput claudeToolResponse `json:"updatedToolOutput"`
		} `json:"hookSpecificOutput"`
	}{}
	resp.HookSpecificOutput.HookEventName = "PostToolUse"
	resp.HookSpecificOutput.UpdatedToolOutput = out
	return json.Marshal(resp)
}

func (c *Claude) getenv(key string) string {
	if c != nil && c.Getenv != nil {
		return c.Getenv(key)
	}
	return os.Getenv(key)
}

func (c *Claude) compactEnabled() bool {
	return compactEnvEnabled(c.getenv, "JEVKIT_COMPACT")
}

func (c *Claude) shadow() bool {
	return compactEnvEnabled(c.getenv, "JEVKIT_COMPACT_SHADOW")
}
