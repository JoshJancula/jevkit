package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/OWNER/jevkit/internal/usage"
)

// Options configures a single hook dispatch.
type Options struct {
	// Timeout is the hard upper bound; zero means DefaultTimeout.
	Timeout time.Duration
	// StateDir is the usage/telemetry state home (see usage.Path). Empty
	// disables telemetry unless AppendHook is set.
	StateDir string
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// AppendHook records one invocation; nil uses usage.AppendHook when
	// StateDir is set. Tests inject a recorder here.
	AppendHook func(stateDir string, rec usage.HookInvocation) error
}

// Outcome codes recorded in hook telemetry.
const (
	OutcomeOK              = "ok"
	OutcomeDeny            = "deny"
	OutcomePassthrough     = "passthrough"
	OutcomePanic           = "panic"
	OutcomeTimeout         = "timeout"
	OutcomeVersionMismatch = "version_mismatch"
	OutcomeGarbage         = "garbage"
	OutcomeUnknownAgent    = "unknown_agent"
)

// emptyPassthrough is the framework fallback when no agent is available.
var emptyPassthrough = []byte("{}")

// Run reads a JSON hook payload from in, dispatches to agent for event, writes
// the response JSON to out, and returns the process exit code. It always fails
// open: panics, timeouts, garbage stdin, version mismatches, and unknown
// agents/events yield a safe passthrough body and exit 0.
func Run(ctx context.Context, agent Agent, event Event, in io.Reader, out io.Writer, opts Options) int {
	start := opts.now()
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	name := ""
	if agent != nil {
		name = agent.Name()
	}
	passthrough := safePassthrough(agent, event)

	raw, err := io.ReadAll(in)
	if err != nil {
		return failOpen(out, passthrough, opts, name, event, OutcomeGarbage, start, "")
	}
	raw = bytes.TrimSpace(raw)

	if agent == nil {
		return failOpen(out, passthrough, opts, name, event, OutcomeUnknownAgent, start, "")
	}
	if len(raw) == 0 || !json.Valid(raw) || raw[0] != '{' {
		return failOpen(out, passthrough, opts, name, event, OutcomeGarbage, start, "")
	}

	version, tool := inspectPayload(raw)
	if version != 0 && version != ProtocolVersion {
		return failOpen(out, passthrough, opts, name, event, OutcomeVersionMismatch, start, tool)
	}
	if version == 0 {
		version = ProtocolVersion
	}

	req := Request{
		Raw:             json.RawMessage(append([]byte(nil), raw...)),
		Event:           event,
		ProtocolVersion: version,
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type result struct {
		resp Response
		err  error
		boom any
	}
	ch := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- result{boom: r}
			}
		}()
		resp, err := dispatch(runCtx, agent, req)
		ch <- result{resp: resp, err: err}
	}()

	var res result
	select {
	case res = <-ch:
	case <-runCtx.Done():
		outcome := OutcomePassthrough
		if runCtx.Err() == context.DeadlineExceeded {
			outcome = OutcomeTimeout
		}
		return failOpen(out, passthrough, opts, name, event, outcome, start, tool)
	}

	if res.boom != nil {
		return failOpen(out, passthrough, opts, name, event, OutcomePanic, start, tool)
	}
	if res.err != nil {
		return failOpen(out, passthrough, opts, name, event, OutcomePassthrough, start, tool)
	}

	body := res.resp.Body
	if len(body) == 0 || !json.Valid(body) {
		body = passthrough
	}
	outcome := OutcomeOK
	code := 0
	if res.resp.Deny {
		outcome = OutcomeDeny
		code = res.resp.ExitCode
	}
	return writeOutcome(out, body, code, opts, usage.HookInvocation{
		Agent: name, Event: string(event), Outcome: outcome, Tool: tool,
		DurationMs: elapsedMs(start, opts.now()),
	})
}

func dispatch(ctx context.Context, agent Agent, req Request) (Response, error) {
	caps := agent.Capabilities()
	switch req.Event {
	case EventPreTool:
		if !caps.PreTool {
			return Response{Body: agent.Passthrough(req.Event)}, nil
		}
		return agent.HandlePreTool(ctx, req)
	case EventPostTool:
		if !caps.PostTool {
			return Response{Body: agent.Passthrough(req.Event)}, nil
		}
		return agent.HandlePostTool(ctx, req)
	case EventStop:
		if !caps.Stop {
			return Response{Body: agent.Passthrough(req.Event)}, nil
		}
		return agent.HandleStop(ctx, req)
	default:
		return Response{Body: agent.Passthrough(req.Event)}, nil
	}
}

func failOpen(out io.Writer, body []byte, opts Options, agent string, event Event, outcome string, start time.Time, tool string) int {
	return writeOutcome(out, body, 0, opts, usage.HookInvocation{
		Agent: agent, Event: string(event), Outcome: outcome, Tool: tool,
		DurationMs: elapsedMs(start, opts.now()),
	})
}

func (opts Options) now() time.Time {
	if opts.Now != nil {
		return opts.Now()
	}
	return time.Now()
}

func elapsedMs(start, end time.Time) int64 {
	if start.IsZero() || end.IsZero() {
		return 0
	}
	d := end.Sub(start).Milliseconds()
	if d < 0 {
		return 0
	}
	return d
}

func safePassthrough(agent Agent, event Event) []byte {
	if agent == nil {
		return emptyPassthrough
	}
	body := agent.Passthrough(event)
	if len(body) == 0 || !json.Valid(body) {
		return emptyPassthrough
	}
	return body
}

func writeOutcome(out io.Writer, body []byte, code int, opts Options, rec usage.HookInvocation) int {
	if out == nil {
		out = io.Discard
	}
	if len(body) == 0 {
		body = emptyPassthrough
	}
	_, _ = out.Write(body)
	if body[len(body)-1] != '\n' {
		_, _ = out.Write([]byte("\n"))
	}
	recordHook(opts, rec)
	return code
}

func recordHook(opts Options, rec usage.HookInvocation) {
	if opts.StateDir == "" && opts.AppendHook == nil {
		return
	}
	appendFn := opts.AppendHook
	if appendFn == nil {
		appendFn = usage.AppendHook
	}
	_ = appendFn(opts.StateDir, rec)
}

// inspectPayload extracts an optional protocol_version and a best-effort tool
// name from a JSON object. version 0 means "not declared".
func inspectPayload(raw []byte) (version int, tool string) {
	var meta struct {
		ProtocolVersion       int    `json:"protocol_version"`
		JevkitProtocolVersion int    `json:"jevkit_protocol_version"`
		ToolName              string `json:"tool_name"`
		ToolCall              struct {
			Name string `json:"name"`
		} `json:"toolCall"`
		Input struct {
			Tool string `json:"tool"`
		} `json:"input"`
	}
	if json.Unmarshal(raw, &meta) != nil {
		return 0, ""
	}
	switch {
	case meta.JevkitProtocolVersion != 0:
		version = meta.JevkitProtocolVersion
	case meta.ProtocolVersion != 0:
		version = meta.ProtocolVersion
	}
	switch {
	case meta.ToolName != "":
		tool = meta.ToolName
	case meta.ToolCall.Name != "":
		tool = meta.ToolCall.Name
	case meta.Input.Tool != "":
		tool = meta.Input.Tool
	}
	return version, tool
}
