// Package worker invokes enrolled CLI agents and parses their structured reply.
package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/OWNER/jevkit/internal/sdlc/adaptive"
	"github.com/OWNER/jevkit/internal/sdlc/enrollment"
	securityconfig "github.com/OWNER/jevkit/internal/security/config"
)

type Request struct {
	Agent          enrollment.Agent
	Assignment     adaptive.Assignment
	Task           string
	Plan           string
	Diff           string
	DiffPath       string
	WorkDir        string
	Workspace      string
	AllowRead      []string
	Yolo           bool
	SecurityPolicy string
	LogDir         string
	LiveOutput     func(stream, line string)
	SessionID      string
	CaptureSession bool
	Compact        bool
}

type Reply struct {
	Outcome          string   `json:"outcome"`
	ReportedOutcome  string   `json:"-"`
	Content          string   `json:"content"`
	CostUSD          float64  `json:"costUsd"`
	CostReported     bool     `json:"-"`
	Focus            string   `json:"focus,omitempty"`
	Reason           string   `json:"reason,omitempty"`
	SessionID        string   `json:"sessionId,omitempty"`
	InputTokens      *int64   `json:"inputTokens,omitempty"`
	OutputTokens     *int64   `json:"outputTokens,omitempty"`
	CompactCompleted bool     `json:"-"`
	WorkspaceDrift   []string `json:"-"`
	DriftTruncated   bool     `json:"-"`
}

type Executor interface {
	Execute(context.Context, Request) (Reply, error)
}

type CLIExecutor struct{}

// InvocationFailure means the agent could not produce a usable result and its
// invocation left the workspace unchanged, so another binding may be tried.
type InvocationFailure struct{ Err error }

func (e *InvocationFailure) Error() string { return e.Err.Error() }
func (e *InvocationFailure) Unwrap() error { return e.Err }

// Execute uses argv, never a shell. The caller has already checked enrollment
// and reach, and must persist the returned result against the assignment.
func (CLIExecutor) Execute(ctx context.Context, req Request) (Reply, error) {
	if req.Agent.Via != enrollment.Runtime || req.Agent.Runtime != req.Assignment.Runtime {
		return Reply{}, fmt.Errorf("worker: assignment is not a matching CLI runtime")
	}
	if req.Assignment.Isolated || len(req.Assignment.AgentWriteScopes)+len(req.Assignment.ProjectWriteScopes) > 0 {
		return Reply{}, fmt.Errorf("worker: requested isolation or write scopes cannot be enforced by CLI adapter")
	}
	if !req.Yolo && os.Getenv("JEVKIT_YOLO") != "1" {
		workspace := req.Workspace
		if workspace == "" {
			workspace = req.WorkDir
		}
		guard := securityconfig.Guard{Workspace: workspace, AllowRead: req.AllowRead}
		if violation, ok := guard.CheckWorkDir(req.WorkDir); !ok {
			return Reply{}, &InvocationFailure{Err: fmt.Errorf("worker: sandbox: %s", violation)}
		}
		if violation, ok := guard.Check(req.Task); !ok {
			return Reply{}, &InvocationFailure{Err: fmt.Errorf("worker: sandbox: %s", violation)}
		}
	}
	// OpenCode cannot read the state directory in its normal project sandbox.
	// Give it a private, short-lived copy of the review artifact in the project.
	if req.Agent.Runtime == "opencode" && req.DiffPath != "" {
		artifact, err := os.CreateTemp(req.WorkDir, ".jevkit-review-*.diff")
		if err != nil {
			return Reply{}, fmt.Errorf("worker: stage OpenCode review artifact: %w", err)
		}
		defer func() { _ = os.Remove(artifact.Name()) }()
		if _, err := artifact.WriteString(req.Diff); err != nil {
			_ = artifact.Close()
			return Reply{}, fmt.Errorf("worker: write OpenCode review artifact: %w", err)
		}
		if err := artifact.Close(); err != nil {
			return Reply{}, fmt.Errorf("worker: close OpenCode review artifact: %w", err)
		}
		req.DiffPath = artifact.Name()
	}
	prompt := makePrompt(req)
	binary, args, err := command(req)
	if err != nil {
		return Reply{}, &InvocationFailure{Err: err}
	}
	snapshot, err := newWorkspaceSnapshot(ctx, req.WorkDir)
	if err != nil {
		if ctx.Err() != nil {
			return Reply{}, ctx.Err()
		}
		return Reply{}, err
	}
	defer snapshot.close()
	before, err := snapshot.capture(ctx)
	if err != nil {
		return Reply{}, err
	}
	var resultPath string
	if req.Agent.Runtime == "codex" {
		dir, err := os.MkdirTemp("", "jevkit-sdlc-worker-")
		if err != nil {
			return Reply{}, err
		}
		defer func() { _ = os.RemoveAll(dir) }()
		resultPath = filepath.Join(dir, "result.json")
		args = append(args, "--output-last-message", resultPath, "-")
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	prepareRuntimeCommand(cmd)
	cmd.Dir = req.WorkDir
	if req.Yolo || req.SecurityPolicy != "" {
		cmd.Env = os.Environ()
		if req.Yolo {
			cmd.Env = append(cmd.Env, "JEVKIT_YOLO=1")
		}
		if req.SecurityPolicy != "" {
			cmd.Env = append(cmd.Env, "JEVKIT_SECURITY_POLICY="+req.SecurityPolicy)
		}
	}
	if req.Compact {
		first, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]string{"role": "user", "content": "/compact"}})
		second, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]string{"role": "user", "content": prompt}})
		cmd.Stdin = bytes.NewReader(append(append(first, '\n'), append(second, '\n')...))
	} else {
		cmd.Stdin = strings.NewReader(prompt)
	}
	var stdout, stderr captureBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if req.LogDir != "" {
		outLog, err := newInvocationLog(req, "stdout")
		if err != nil {
			return Reply{}, err
		}
		errLog, err := newInvocationLog(req, "stderr")
		if err != nil {
			return Reply{}, err
		}
		cmd.Stdout = io.MultiWriter(&stdout, outLog)
		cmd.Stderr = io.MultiWriter(&stderr, errLog)
		defer func() { _ = outLog.Flush() }()
		defer func() { _ = errLog.Flush() }()
	}
	if err := runRuntimeCommand(cmd); err != nil {
		if ctx.Err() != nil {
			return Reply{}, ctx.Err()
		}
		msg := stderr.String() + " " + stdout.String()
		partial := failedUsage(req.Agent.Runtime, stdout.Bytes())
		if authError(msg) {
			partial.Outcome = "auth-failed"
			return partial, nil
		}
		return partial, retryableIfUnchanged(ctx, snapshot, before, fmt.Errorf("worker: %s exited: %w: %s", req.Agent.Runtime, err, truncate(msg, 500)))
	}
	raw := stdout.Bytes()
	if resultPath != "" {
		raw, err = os.ReadFile(resultPath)
		if err != nil {
			return Reply{}, retryableIfUnchanged(ctx, snapshot, before, fmt.Errorf("worker: read Codex result: %w", err))
		}
	}
	var cursorSessionID string
	var cursorInput, cursorOutput *int64
	if req.Agent.Runtime == "cursor" {
		var found bool
		raw, cursorSessionID, cursorInput, cursorOutput, found = cursorResult(raw)
		if !found {
			return failedUsage(req.Agent.Runtime, stdout.Bytes()), retryableIfUnchanged(ctx, snapshot, before, fmt.Errorf("worker: Cursor stream ended without a final result; inspect the saved stdout and stderr logs"))
		}
	}
	compactCompleted := false
	var compactSessionID string
	var compactInput, compactOutput *int64
	if req.Agent.Runtime == "claude" && (req.Compact || req.LiveOutput != nil) {
		for _, line := range bytes.Split(stdout.Bytes(), []byte{'\n'}) {
			var event struct {
				Type      string `json:"type"`
				Subtype   string `json:"subtype"`
				Result    string `json:"result"`
				SessionID string `json:"session_id"`
				Usage     struct {
					InputTokens  int64 `json:"input_tokens"`
					OutputTokens int64 `json:"output_tokens"`
				} `json:"usage"`
			}
			if json.Unmarshal(line, &event) != nil {
				continue
			}
			if event.Type == "system" && event.Subtype == "compact_boundary" {
				compactCompleted = true
			}
			if event.Type == "result" {
				raw = []byte(event.Result)
				compactSessionID = event.SessionID
				if event.Usage.InputTokens > 0 || event.Usage.OutputTokens > 0 {
					compactInput, compactOutput = &event.Usage.InputTokens, &event.Usage.OutputTokens
				}
			}
		}
		if req.Compact && !compactCompleted {
			return Reply{}, fmt.Errorf("worker: Claude /compact did not confirm completion: %s", truncate(stderr.String()+" "+stdout.String(), 500))
		}
	}
	if req.Agent.Runtime == "opencode" && req.CaptureSession {
		var content strings.Builder
		for _, line := range bytes.Split(raw, []byte{'\n'}) {
			var event struct {
				Type      string `json:"type"`
				SessionID string `json:"sessionID"`
				Part      struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"part"`
			}
			if json.Unmarshal(line, &event) != nil {
				continue
			}
			if event.Part.Type == "text" {
				content.WriteString(event.Part.Text)
			}
		}
		if content.Len() > 0 {
			raw = []byte(content.String())
		} else {
			return failedUsage(req.Agent.Runtime, stdout.Bytes()), retryableIfUnchanged(ctx, snapshot, before, fmt.Errorf("worker: OpenCode returned no text reply; inspect saved stdout and stderr logs: %s", truncate(stderr.String(), 250)))
		}
	}
	var sessionID string
	var inputTokens, outputTokens *int64
	if req.Agent.Runtime == "antigravity" {
		for _, line := range bytes.Split(stdout.Bytes(), []byte{'\n'}) {
			var event struct {
				Event          string `json:"event"`
				ConversationID string `json:"conversation_id"`
				Result         struct {
					ConversationID string `json:"conversation_id"`
					Status         string `json:"status"`
					Response       string `json:"response"`
					Usage          struct {
						InputTokens  int64 `json:"input_tokens"`
						OutputTokens int64 `json:"output_tokens"`
					} `json:"usage"`
				} `json:"result"`
			}
			if json.Unmarshal(line, &event) != nil {
				continue
			}
			if event.ConversationID != "" {
				sessionID = event.ConversationID
			}
			if event.Event == "result" {
				if event.Result.ConversationID != "" {
					sessionID = event.Result.ConversationID
				}
				if event.Result.Status != "SUCCESS" {
					return Reply{}, fmt.Errorf("worker: Antigravity result status %s", event.Result.Status)
				}
				raw = []byte(event.Result.Response)
				inputTokens, outputTokens = &event.Result.Usage.InputTokens, &event.Result.Usage.OutputTokens
			}
		}
	}
	if req.CaptureSession {
		var envelope struct {
			Result    json.RawMessage `json:"result"`
			SessionID string          `json:"session_id"`
			Usage     struct {
				InputTokens  int64 `json:"input_tokens"`
				OutputTokens int64 `json:"output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(stdout.Bytes(), &envelope) == nil {
			sessionID = envelope.SessionID
			if len(envelope.Result) > 0 && req.Agent.Runtime == "claude" {
				var message string
				if json.Unmarshal(envelope.Result, &message) == nil {
					raw = []byte(message)
				}
			}
			if envelope.Usage.InputTokens > 0 || envelope.Usage.OutputTokens > 0 {
				inputTokens, outputTokens = &envelope.Usage.InputTokens, &envelope.Usage.OutputTokens
			}
		}
		if req.Agent.Runtime == "codex" {
			for _, line := range bytes.Split(stdout.Bytes(), []byte{'\n'}) {
				var event struct {
					Type     string `json:"type"`
					ThreadID string `json:"thread_id"`
					Usage    struct {
						InputTokens  int64 `json:"input_tokens"`
						OutputTokens int64 `json:"output_tokens"`
					} `json:"usage"`
				}
				if json.Unmarshal(line, &event) != nil {
					continue
				}
				if event.Type == "thread.started" {
					sessionID = event.ThreadID
				}
				if event.Type == "turn.completed" {
					inputTokens, outputTokens = &event.Usage.InputTokens, &event.Usage.OutputTokens
				}
			}
		}
		if req.Agent.Runtime == "opencode" {
			for _, line := range bytes.Split(stdout.Bytes(), []byte{'\n'}) {
				var event struct {
					SessionID string `json:"sessionID"`
					Part      struct {
						Tokens struct {
							Input  int64 `json:"input"`
							Output int64 `json:"output"`
						} `json:"tokens"`
					} `json:"part"`
				}
				if json.Unmarshal(line, &event) != nil {
					continue
				}
				if event.SessionID != "" {
					sessionID = event.SessionID
				}
				if event.Part.Tokens.Input > 0 || event.Part.Tokens.Output > 0 {
					inputTokens, outputTokens = &event.Part.Tokens.Input, &event.Part.Tokens.Output
				}
			}
		}
	}
	if compactSessionID != "" {
		sessionID = compactSessionID
	}
	if cursorSessionID != "" {
		sessionID = cursorSessionID
	}
	if cursorInput != nil {
		inputTokens, outputTokens = cursorInput, cursorOutput
	}
	if compactInput != nil {
		inputTokens, outputTokens = compactInput, compactOutput
	}
	reply, err := ParseReply(raw)
	if err != nil {
		return failedUsage(req.Agent.Runtime, stdout.Bytes()), retryableIfUnchanged(ctx, snapshot, before, err)
	}
	if !allowedOutcome(req.Assignment.Role, reply.Outcome) {
		return failedUsage(req.Agent.Runtime, stdout.Bytes()), retryableIfUnchanged(ctx, snapshot, before, fmt.Errorf("worker: %s reply outcome %q is not allowed; expected %s", req.Assignment.Role, reply.Outcome, strings.Join(allowedOutcomes(req.Assignment.Role), ", ")))
	}
	reply.SessionID, reply.InputTokens, reply.OutputTokens = sessionID, inputTokens, outputTokens
	measured := failedUsage(req.Agent.Runtime, stdout.Bytes())
	if req.Agent.Runtime == "opencode" && measured.InputTokens != nil {
		reply.InputTokens, reply.OutputTokens = measured.InputTokens, measured.OutputTokens
	}
	if measured.CostReported && !reply.CostReported {
		reply.CostUSD, reply.CostReported = measured.CostUSD, true
	}
	reply.CompactCompleted = compactCompleted
	after, err := snapshot.capture(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return Reply{}, ctx.Err()
		}
		return Reply{}, err
	}
	if req.Assignment.Role != "implementer" && before != after {
		if req.Assignment.Role != "assessor" {
			return Reply{}, fmt.Errorf("worker: non-implementer workspace drift")
		}
		reply.WorkspaceDrift, reply.DriftTruncated, err = snapshot.changedPaths(ctx, before, after)
		if err != nil {
			return Reply{}, err
		}
	}
	if reply.Outcome == "changed" {
		if before == after {
			return Reply{}, fmt.Errorf("worker: changed outcome has no workspace diff")
		}
		reply.Content, err = snapshot.report(ctx, before, after)
		if err != nil {
			return Reply{}, err
		}
	} else if req.Assignment.Role == "implementer" && before != after {
		return Reply{}, fmt.Errorf("worker: workspace changed without a changed outcome")
	}
	if reply.Outcome == "handoff" && before != after {
		return Reply{}, fmt.Errorf("worker: handoff changed the workspace")
	}
	return reply, nil
}

// failedUsage extracts counts only from runtime events that explicitly report
// them. It is used when the final structured reply is unavailable.
func failedUsage(runtime string, raw []byte) Reply {
	var reply Reply
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		var event struct {
			Type         string   `json:"type"`
			TotalCostUSD *float64 `json:"total_cost_usd"`
			Result       struct {
				Usage *struct {
					Input  int64 `json:"input_tokens"`
					Output int64 `json:"output_tokens"`
				} `json:"usage"`
			} `json:"result"`
			Part struct {
				Cost   *float64 `json:"cost"`
				Tokens *struct {
					Input  int64 `json:"input"`
					Output int64 `json:"output"`
				} `json:"tokens"`
			} `json:"part"`
			Usage *struct {
				Input       int64 `json:"input_tokens"`
				Output      int64 `json:"output_tokens"`
				InputCamel  int64 `json:"inputTokens"`
				OutputCamel int64 `json:"outputTokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(line, &event) != nil {
			continue
		}
		if runtime == "opencode" && event.Type == "step_finish" && event.Part.Tokens != nil {
			if reply.InputTokens == nil {
				reply.InputTokens, reply.OutputTokens = new(int64), new(int64)
			}
			*reply.InputTokens += event.Part.Tokens.Input
			*reply.OutputTokens += event.Part.Tokens.Output
		}
		if runtime == "opencode" && event.Type == "step_finish" && event.Part.Cost != nil {
			reply.CostUSD += *event.Part.Cost
			reply.CostReported = true
		}
		if event.TotalCostUSD != nil && (runtime == "claude" || runtime == "cursor") {
			reply.CostUSD, reply.CostReported = *event.TotalCostUSD, true
		}
		if (runtime == "codex" || runtime == "claude") && event.Usage != nil && (event.Type == "turn.completed" || event.Type == "result") {
			in, out := event.Usage.Input, event.Usage.Output
			reply.InputTokens, reply.OutputTokens = &in, &out
		}
		if runtime == "cursor" && event.Usage != nil {
			in, out := event.Usage.Input, event.Usage.Output
			if event.Usage.InputCamel != 0 {
				in = event.Usage.InputCamel
			}
			if event.Usage.OutputCamel != 0 {
				out = event.Usage.OutputCamel
			}
			reply.InputTokens, reply.OutputTokens = &in, &out
		}
		if runtime == "antigravity" && event.Result.Usage != nil {
			in, out := event.Result.Usage.Input, event.Result.Usage.Output
			reply.InputTokens, reply.OutputTokens = &in, &out
		}
	}
	return reply
}

func cursorResult(raw []byte) ([]byte, string, *int64, *int64, bool) {
	var content []byte
	var sessionID string
	var inputTokens, outputTokens *int64
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		var event struct {
			Result    string           `json:"result"`
			SessionID string           `json:"session_id"`
			Usage     map[string]int64 `json:"usage"`
		}
		if json.Unmarshal(line, &event) != nil {
			continue
		}
		if event.SessionID != "" {
			sessionID = event.SessionID
		}
		if event.Result != "" {
			content = []byte(event.Result)
			inputTokens, outputTokens = nil, nil
			for _, key := range []string{"inputTokens", "input_tokens"} {
				if value, ok := event.Usage[key]; ok {
					inputTokens = &value
					break
				}
			}
			for _, key := range []string{"outputTokens", "output_tokens"} {
				if value, ok := event.Usage[key]; ok {
					outputTokens = &value
					break
				}
			}
		}
	}
	return content, sessionID, inputTokens, outputTokens, len(content) > 0
}

func retryableIfUnchanged(ctx context.Context, snapshot *workspaceSnapshot, before string, cause error) error {
	after, err := snapshot.capture(ctx)
	if err != nil {
		return fmt.Errorf("%w; could not verify whether the workspace changed: %v", cause, err)
	}
	if before != after {
		return fmt.Errorf("%w; workspace changed during this invocation; inspect its files and logs before retrying", cause)
	}
	return &InvocationFailure{Err: cause}
}

func command(req Request) (string, []string, error) {
	a := req.Agent
	bin := a.Binary
	if bin == "" {
		switch a.Runtime {
		case "cursor":
			bin = "cursor-agent"
		case "antigravity":
			bin = "agy"
		default:
			bin = a.Runtime
		}
	}
	switch a.Runtime {
	case "codex":
		sandbox := "workspace-write"
		if req.Assignment.ReadOnly || req.Assignment.Role != "implementer" {
			sandbox = "read-only"
		}
		args := []string{"exec"}
		if req.SessionID != "" {
			args = append(args, "resume", req.SessionID, "-c", "sandbox_mode="+sandbox)
			args = append(args, "--model", a.Model)
		} else {
			args = append(args, "--model", a.Model, "--sandbox", sandbox)
		}
		if req.CaptureSession {
			args = append(args, "--json")
		}
		return bin, args, nil
	case "claude":
		args := []string{"-p", "--model", a.Model, "--output-format", "text"}
		if req.CaptureSession {
			args[4] = "json"
		}
		if req.Compact || req.LiveOutput != nil {
			args[4] = "stream-json"
			args = append(args, "--verbose")
		}
		if req.Compact {
			args = append(args, "--input-format", "stream-json")
		}
		if a.RuntimeAgent != "" {
			args = append(args, "--agent", a.RuntimeAgent)
		}
		if req.SessionID != "" {
			args = append(args, "--resume", req.SessionID)
		}
		if req.Assignment.ReadOnly || req.Assignment.Role != "implementer" {
			args = append(args, "--permission-mode", "plan")
		}
		return bin, args, nil
	case "cursor":
		args := []string{"-p", "--output-format", "stream-json", "--model", a.Model}
		if req.SessionID != "" {
			args = append(args, "--resume", req.SessionID)
		}
		if req.Assignment.ReadOnly || req.Assignment.Role != "implementer" {
			args = append(args, "--mode", "ask")
		}
		return bin, append(args, makePrompt(req)), nil
	case "opencode":
		if req.Assignment.ReadOnly {
			return "", nil, fmt.Errorf("worker: OpenCode CLI cannot enforce read-only assignment")
		}
		args := []string{"run", "--model", a.Model}
		if req.CaptureSession {
			args = append(args, "--format", "json")
		}
		if req.SessionID != "" {
			args = append(args, "--session", req.SessionID)
		}
		if a.RuntimeAgent != "" {
			args = append(args, "--agent", a.RuntimeAgent)
		}
		return bin, append(args, makePrompt(req)), nil
	case "antigravity":
		args := []string{"--model", a.Model, "--output-format", "stream-json"}
		if a.RuntimeAgent != "" {
			args = append(args, "--agent", a.RuntimeAgent)
		}
		if req.SessionID != "" {
			args = append(args, "--conversation", req.SessionID)
		}
		if req.Assignment.ReadOnly || req.Assignment.Role != "implementer" {
			args = append(args, "--mode", "plan")
		} else {
			args = append(args, "--mode", "accept-edits", "--dangerously-skip-permissions")
		}
		return bin, append(args, "--print", makePrompt(req)), nil
	default:
		return "", nil, fmt.Errorf("worker: unsupported CLI runtime %q", a.Runtime)
	}
}

func makePrompt(req Request) string {
	diff := req.Diff
	if len(diff) > 64*1024 {
		diff = diff[:64*1024] + "\n[report excerpt ends here; inspect the saved artifact or workspace for the rest]"
	}
	if req.DiffPath != "" {
		diff = fmt.Sprintf("Saved full change artifact: %s\nInspect this file and the workspace as needed. The excerpt below is bounded so the runtime accepts the prompt.\n%s", req.DiffPath, diff)
	}
	allowed := allowedOutcomes(req.Assignment.Role)
	example := "answer"
	if len(allowed) > 0 {
		example = allowed[0]
	}
	return fmt.Sprintf("You are enrolled as agent %q for the %s role. Task: %s\nFocus: %s\nRouting context: %s\nPlan revision: %s\nPlan:\n%s\nChange report revision: %s\nChange report:\n%s\nReturn exactly one JSON object with outcome and content fields. For this role, outcome MUST be exactly one of: %s. Use a bare outcome value, for example {\"outcome\":\"%s\",\"content\":\"...\"}. Never include the role name in the outcome value. Complete the assigned role with available tools when possible. Return handoff with required focus and reason only when you cannot proceed; another agent may not be available. A handoff must leave the workspace unchanged. For planned, content is the complete plan. For changed, content is a concise description; Jevkit computes the change report from the workspace. For other outcomes, content is a concise explanation. Do not include Markdown fences. Reviewers must review the change report and changed files for this revision and must not edit files.", req.Agent.ID, req.Assignment.Role, req.Task, req.Assignment.Objective, req.Assignment.Reason, req.Assignment.Revision, req.Plan, req.Assignment.Revision, diff, strings.Join(allowed, ", "), example)
}

func allowedOutcomes(role string) []string {
	switch role {
	case "planner":
		return []string{"planned", "answer", "no-change", "handoff"}
	case "implementer":
		return []string{"changed", "answer", "no-change", "handoff"}
	case "research":
		return []string{"advice", "handoff"}
	case "assessor", "qa", "security", "code-review":
		return []string{"approved", "changes-required", "handoff"}
	}
	return nil
}

func allowedOutcome(role, outcome string) bool {
	for _, allowed := range allowedOutcomes(role) {
		if outcome == allowed {
			return true
		}
	}
	return false
}

func ParseReply(raw []byte) (Reply, error) {
	s := strings.TrimSpace(string(raw))
	if strings.HasPrefix(s, "```") {
		start := strings.IndexByte(s, '\n')
		end := strings.LastIndex(s, "```")
		if start >= 0 && end > start {
			s = strings.TrimSpace(s[start+1 : end])
		}
	}
	var r Reply
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		// Some CLIs prepend a short status sentence before the requested JSON.
		// Accept one complete object, but never guess an outcome from prose.
		start := strings.IndexByte(s, '{')
		if start < 0 {
			return Reply{}, fmt.Errorf("worker: invalid structured reply: %w", err)
		}
		var object json.RawMessage
		if decodeErr := json.NewDecoder(strings.NewReader(s[start:])).Decode(&object); decodeErr != nil || len(object) == 0 || object[0] != '{' {
			return Reply{}, fmt.Errorf("worker: invalid structured reply: %w", err)
		}
		if decodeErr := json.Unmarshal(object, &r); decodeErr != nil {
			return Reply{}, fmt.Errorf("worker: invalid structured reply: %w", decodeErr)
		}
		s = string(object)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(s), &fields) == nil {
		_, r.CostReported = fields["costUsd"]
	}
	r.ReportedOutcome = r.Outcome
	r.Outcome = canonicalOutcome(r.Outcome)
	if r.Outcome == "" || r.CostUSD < 0 {
		return Reply{}, fmt.Errorf("worker: missing outcome or negative cost")
	}
	if r.Outcome == "handoff" && (strings.TrimSpace(r.Focus) == "" || strings.TrimSpace(r.Reason) == "") {
		return Reply{}, fmt.Errorf("worker: handoff requires focus and reason")
	}
	if (r.Outcome == "planned" || r.Outcome == "changed") && strings.TrimSpace(r.Content) == "" {
		return Reply{}, fmt.Errorf("worker: %s requires content", r.Outcome)
	}
	return r, nil
}

func canonicalOutcome(raw string) string {
	outcome := strings.ToLower(strings.TrimSpace(raw))
	parts := strings.SplitN(outcome, ":", 2)
	if len(parts) == 2 {
		role, value := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if allowedOutcome(role, value) {
			return value
		}
	}
	return outcome
}

func authError(s string) bool {
	s = strings.ToLower(s)
	for _, phrase := range []string{"unauthorized", "authentication failed", "not authenticated", "invalid api key", "login required", "please log in"} {
		if strings.Contains(s, phrase) {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
