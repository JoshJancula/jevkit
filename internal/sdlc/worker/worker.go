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
	"sync"

	"github.com/JoshJancula/jevkit/internal/agents"
	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/enrollment"
	securityconfig "github.com/JoshJancula/jevkit/internal/security/config"
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
	SDLCRunID      string
	LogDir         string
	// LogTailBytes overrides MaxLogTail for this invocation's saved
	// stdout/stderr/lines.jsonl bound. Zero or negative uses MaxLogTail.
	LogTailBytes     int
	LiveOutput       func(stream, line string)
	SessionID        string
	CaptureSession   bool
	Compact          bool
	JevkitHooks      bool
	JevkitBinary     string
	JevkitCompaction *bool
}

type Reply struct {
	Outcome                 string                 `json:"outcome"`
	ReportedOutcome         string                 `json:"-"`
	Content                 string                 `json:"content"`
	CostUSD                 float64                `json:"costUsd"`
	CostReported            bool                   `json:"-"`
	Focus                   string                 `json:"focus,omitempty"`
	Reason                  string                 `json:"reason,omitempty"`
	NextSteps               []string               `json:"nextSteps,omitempty"`
	AcceptanceCriteria      []string               `json:"acceptanceCriteria,omitempty"`
	Checks                  []adaptive.Check       `json:"checks,omitempty"`
	Subtasks                *adaptive.SubtaskGraph `json:"subtasks,omitempty"`
	SessionID               string                 `json:"sessionId,omitempty"`
	InputTokens             *int64                 `json:"inputTokens,omitempty"`
	OutputTokens            *int64                 `json:"outputTokens,omitempty"`
	ToolCalls               *int64                 `json:"toolCalls,omitempty"`
	CacheReadTokens         *int64                 `json:"cacheReadTokens,omitempty"`
	CacheCreationTokens     *int64                 `json:"cacheCreationTokens,omitempty"`
	UsageProvenance         string                 `json:"usageProvenance,omitempty"`
	StablePrefixBytes       int                    `json:"stablePrefixBytes,omitempty"`
	StablePrefixFingerprint string                 `json:"stablePrefixFingerprint,omitempty"`
	CompactCompleted        bool                   `json:"-"`
	WorkspaceDrift          []string               `json:"-"`
	DriftTruncated          bool                   `json:"-"`
}

// Usage provenance labels identify which runtime event supplied measured counts.
const (
	ProvenanceClaudeResult       = "claude.result"
	ProvenanceCodexTurnCompleted = "codex.turn.completed"
	ProvenanceCursorResult       = "cursor.result"
	ProvenanceOpenCodeStepFinish = "opencode.step_finish"
	ProvenanceAntigravityResult  = "antigravity.result"
)

type Executor interface {
	Execute(context.Context, Request) (Reply, error)
}

type CLIExecutor struct{}

var hookInstallMu sync.Mutex

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
	layout := buildPrompt(req)
	prompt := layout.Prompt
	binary, args, err := command(req)
	if err != nil {
		return Reply{}, &InvocationFailure{Err: err}
	}
	if req.JevkitHooks {
		adapter := agents.Lookup(req.Agent.Runtime)
		if adapter == nil {
			return Reply{}, fmt.Errorf("worker: no Jevkit hook adapter for %s", req.Agent.Runtime)
		}
		hookInstallMu.Lock()
		_, err := agents.InstallAgentComponents(adapter, agents.InstallOptions{WorkDir: req.WorkDir, Scope: "project", Binary: req.JevkitBinary}, agents.Components{Hooks: true})
		hookInstallMu.Unlock()
		if err != nil {
			return Reply{}, fmt.Errorf("worker: install Jevkit hooks: %w", err)
		}
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
	if req.Yolo || req.SecurityPolicy != "" || req.SDLCRunID != "" || req.JevkitCompaction != nil {
		cmd.Env = os.Environ()
		if req.Yolo {
			cmd.Env = append(cmd.Env, "JEVKIT_YOLO=1")
		}
		if req.SecurityPolicy != "" {
			cmd.Env = append(cmd.Env, "JEVKIT_SECURITY_POLICY="+req.SecurityPolicy)
		}
		if req.SDLCRunID != "" {
			cmd.Env = append(cmd.Env, "JEVKIT_SDLC_RUN_ID="+req.SDLCRunID)
		}
		if req.JevkitCompaction != nil {
			value := "0"
			if *req.JevkitCompaction {
				value = "1"
			}
			cmd.Env = append(cmd.Env, "JEVKIT_COMPACT="+value)
			hooks := "0"
			if req.JevkitHooks {
				hooks = "1"
			}
			cmd.Env = append(cmd.Env, "JEVKIT_SDLC_HOOKS="+hooks)
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
	structured := req.Agent.Runtime == "cursor" || req.Agent.Runtime == "antigravity" ||
		(req.Agent.Runtime == "claude" && (req.Compact || req.LogDir != "" || req.LiveOutput != nil)) ||
		((req.Agent.Runtime == "codex" || req.Agent.Runtime == "opencode") && req.CaptureSession)
	toolCalls := newToolCallCounter(req.Agent.Runtime, structured)
	cmd.Stdout, cmd.Stderr = io.MultiWriter(&stdout, toolCalls), &stderr
	if req.LogDir != "" {
		outLog, err := newInvocationLog(req, "stdout")
		if err != nil {
			return Reply{}, err
		}
		errLog, err := newInvocationLog(req, "stderr")
		if err != nil {
			return Reply{}, err
		}
		cmd.Stdout = io.MultiWriter(&stdout, toolCalls, outLog)
		cmd.Stderr = io.MultiWriter(&stderr, errLog)
		defer func() { _ = outLog.Flush() }()
		defer func() { _ = errLog.Flush() }()
		defer func() { _ = outLog.close() }()
		defer func() { _ = errLog.close() }()
	}
	partialUsage := func() Reply {
		partial := withPromptLayout(failedUsage(req.Agent.Runtime, stdout.Bytes()), layout)
		partial.ToolCalls = toolCalls.result()
		return partial
	}
	if err := runRuntimeCommand(cmd); err != nil {
		if ctx.Err() != nil {
			return partialUsage(), ctx.Err()
		}
		msg := stderr.String() + " " + stdout.String()
		partial := partialUsage()
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
			return partialUsage(), retryableIfUnchanged(ctx, snapshot, before, fmt.Errorf("worker: read Codex result: %w", err))
		}
	}
	var cursorSessionID string
	var cursorInput, cursorOutput *int64
	if req.Agent.Runtime == "cursor" {
		var found bool
		raw, cursorSessionID, cursorInput, cursorOutput, found = cursorResult(raw)
		if !found {
			return partialUsage(), retryableIfUnchanged(ctx, snapshot, before, fmt.Errorf("worker: Cursor stream ended without a final result; inspect the saved stdout and stderr logs"))
		}
	}
	compactCompleted := false
	var compactSessionID string
	var compactInput, compactOutput *int64
	if req.Agent.Runtime == "claude" && (req.Compact || req.LogDir != "" || req.LiveOutput != nil) {
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
			return partialUsage(), fmt.Errorf("worker: Claude /compact did not confirm completion: %s", truncate(stderr.String()+" "+stdout.String(), 500))
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
			return partialUsage(), retryableIfUnchanged(ctx, snapshot, before, fmt.Errorf("worker: OpenCode returned no text reply; inspect saved stdout and stderr logs: %s", truncate(stderr.String(), 250)))
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
					return partialUsage(), fmt.Errorf("worker: Antigravity result status %s", event.Result.Status)
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
		return partialUsage(), retryableIfUnchanged(ctx, snapshot, before, err)
	}
	if !allowedOutcome(req.Assignment.Role, reply.Outcome) {
		return partialUsage(), retryableIfUnchanged(ctx, snapshot, before, fmt.Errorf("worker: %s reply outcome %q is not allowed; expected %s", req.Assignment.Role, reply.Outcome, strings.Join(allowedOutcomes(req.Assignment.Role), ", ")))
	}
	if req.Assignment.Role == "planner" {
		if err := validatePlannerReply(reply); err != nil {
			return partialUsage(), retryableIfUnchanged(ctx, snapshot, before, err)
		}
		reply = normalizePlannerReply(reply)
	}
	reply.SessionID, reply.InputTokens, reply.OutputTokens = sessionID, inputTokens, outputTokens
	reply.ToolCalls = toolCalls.result()
	measured := failedUsage(req.Agent.Runtime, stdout.Bytes())
	if req.Agent.Runtime == "opencode" && measured.InputTokens != nil {
		reply.InputTokens, reply.OutputTokens = measured.InputTokens, measured.OutputTokens
	}
	if measured.CostReported && !reply.CostReported {
		reply.CostUSD, reply.CostReported = measured.CostUSD, true
	}
	applyMeasuredCache(&reply, measured)
	reply = withPromptLayout(reply, layout)
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
// them. It is used when the final structured reply is unavailable, and also to
// recover provider cache fields that adapters emit beside input/output.
// Absent fields stay nil; present zeros are preserved as measured values.
//
// Result is kept as RawMessage because Claude/Cursor put the reply text in
// result (a string) while Antigravity puts an object there. A typed struct
// field would reject the whole line and drop usage/cost for those runtimes.
func failedUsage(runtime string, raw []byte) Reply {
	var reply Reply
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		var event struct {
			Type         string          `json:"type"`
			TotalCostUSD *float64        `json:"total_cost_usd"`
			Result       json.RawMessage `json:"result"`
			Part         struct {
				Cost   *float64 `json:"cost"`
				Tokens *struct {
					Input  int64 `json:"input"`
					Output int64 `json:"output"`
					Cache  *struct {
						Read  *int64 `json:"read"`
						Write *int64 `json:"write"`
					} `json:"cache"`
				} `json:"tokens"`
			} `json:"part"`
			Usage *struct {
				Input           *int64 `json:"input_tokens"`
				Output          *int64 `json:"output_tokens"`
				InputCamel      *int64 `json:"inputTokens"`
				OutputCamel     *int64 `json:"outputTokens"`
				CachedInput     *int64 `json:"cached_input_tokens"`
				CacheRead       *int64 `json:"cache_read_input_tokens"`
				CacheCreation   *int64 `json:"cache_creation_input_tokens"`
				CacheReadCamel  *int64 `json:"cacheReadTokens"`
				CacheWriteCamel *int64 `json:"cacheWriteTokens"`
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
			if event.Part.Tokens.Cache != nil {
				if event.Part.Tokens.Cache.Read != nil {
					if reply.CacheReadTokens == nil {
						reply.CacheReadTokens = new(int64)
					}
					*reply.CacheReadTokens += *event.Part.Tokens.Cache.Read
				}
				if event.Part.Tokens.Cache.Write != nil {
					if reply.CacheCreationTokens == nil {
						reply.CacheCreationTokens = new(int64)
					}
					*reply.CacheCreationTokens += *event.Part.Tokens.Cache.Write
				}
			}
			reply.UsageProvenance = ProvenanceOpenCodeStepFinish
		}
		if runtime == "opencode" && event.Type == "step_finish" && event.Part.Cost != nil {
			reply.CostUSD += *event.Part.Cost
			reply.CostReported = true
			if reply.UsageProvenance == "" {
				reply.UsageProvenance = ProvenanceOpenCodeStepFinish
			}
		}
		if event.TotalCostUSD != nil && (runtime == "claude" || runtime == "cursor") {
			reply.CostUSD, reply.CostReported = *event.TotalCostUSD, true
		}
		if (runtime == "codex" || runtime == "claude") && event.Usage != nil && (event.Type == "turn.completed" || event.Type == "result") {
			in, out := derefOrZero(event.Usage.Input), derefOrZero(event.Usage.Output)
			if event.Usage.Input != nil || event.Usage.Output != nil {
				reply.InputTokens, reply.OutputTokens = &in, &out
			}
			if runtime == "claude" {
				applyClaudeCache(&reply, event.Usage.CacheRead, event.Usage.CacheCreation)
				reply.UsageProvenance = ProvenanceClaudeResult
			}
			if runtime == "codex" {
				if event.Usage.CachedInput != nil {
					v := *event.Usage.CachedInput
					reply.CacheReadTokens = &v
				}
				reply.UsageProvenance = ProvenanceCodexTurnCompleted
			}
		}
		if runtime == "cursor" && event.Usage != nil {
			in := firstInt64(event.Usage.Input, event.Usage.InputCamel)
			out := firstInt64(event.Usage.Output, event.Usage.OutputCamel)
			if in != nil || out != nil {
				reply.InputTokens, reply.OutputTokens = in, out
			}
			read := firstInt64(event.Usage.CacheRead, event.Usage.CacheReadCamel)
			write := firstInt64(event.Usage.CacheCreation, event.Usage.CacheWriteCamel)
			if read != nil {
				reply.CacheReadTokens = read
			}
			if write != nil {
				reply.CacheCreationTokens = write
			}
			reply.UsageProvenance = ProvenanceCursorResult
		}
		if runtime == "antigravity" && len(event.Result) > 0 && event.Result[0] == '{' {
			var result struct {
				Usage *struct {
					Input         *int64 `json:"input_tokens"`
					Output        *int64 `json:"output_tokens"`
					CacheRead     *int64 `json:"cache_read_input_tokens"`
					CacheCreation *int64 `json:"cache_creation_input_tokens"`
				} `json:"usage"`
			}
			if json.Unmarshal(event.Result, &result) == nil && result.Usage != nil {
				in, out := derefOrZero(result.Usage.Input), derefOrZero(result.Usage.Output)
				if result.Usage.Input != nil || result.Usage.Output != nil {
					reply.InputTokens, reply.OutputTokens = &in, &out
				}
				if result.Usage.CacheRead != nil || result.Usage.CacheCreation != nil {
					applyClaudeCache(&reply, result.Usage.CacheRead, result.Usage.CacheCreation)
				}
				reply.UsageProvenance = ProvenanceAntigravityResult
			}
		}
	}
	return reply
}

func applyMeasuredCache(dst *Reply, measured Reply) {
	if measured.CacheReadTokens != nil {
		dst.CacheReadTokens = measured.CacheReadTokens
	}
	if measured.CacheCreationTokens != nil {
		dst.CacheCreationTokens = measured.CacheCreationTokens
	}
	if measured.UsageProvenance != "" && dst.UsageProvenance == "" {
		dst.UsageProvenance = measured.UsageProvenance
	}
	// Prefer measured provenance whenever cache counts came from the stream.
	if measured.CacheReadTokens != nil || measured.CacheCreationTokens != nil {
		dst.UsageProvenance = measured.UsageProvenance
	}
}

func applyClaudeCache(reply *Reply, read, creation *int64) {
	if read != nil {
		v := *read
		reply.CacheReadTokens = &v
	}
	if creation != nil {
		v := *creation
		reply.CacheCreationTokens = &v
	}
}

func derefOrZero(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func firstInt64(values ...*int64) *int64 {
	for _, v := range values {
		if v != nil {
			out := *v
			return &out
		}
	}
	return nil
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
					v := value
					inputTokens = &v
					break
				}
			}
			for _, key := range []string{"outputTokens", "output_tokens"} {
				if value, ok := event.Usage[key]; ok {
					v := value
					outputTokens = &v
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
		// The TUI reads the saved log instead of using LiveOutput. Request
		// Claude's stream whenever logging is enabled so tool events reach
		// that log while the invocation is still running.
		if req.Compact || req.LogDir != "" || req.LiveOutput != nil {
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

func withPromptLayout(reply Reply, layout PromptLayout) Reply {
	reply.StablePrefixBytes = layout.StablePrefixBytes
	reply.StablePrefixFingerprint = layout.StablePrefixFingerprint
	return reply
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

func validatePlannerReply(r Reply) error {
	switch r.Outcome {
	case "planned":
		if err := adaptive.ValidatePlannedHandoff(r.Content, r.NextSteps, r.AcceptanceCriteria, r.Checks); err != nil {
			return fmt.Errorf("worker: %w", err)
		}
	case "handoff":
		if strings.TrimSpace(r.Focus) == "" || strings.TrimSpace(r.Reason) == "" {
			return fmt.Errorf("worker: handoff requires focus and reason naming what is needed next")
		}
	}
	return nil
}

func normalizePlannerReply(r Reply) Reply {
	if r.Outcome != "planned" {
		return r
	}
	normalized := adaptive.NormalizeSubtaskGraph(r.Subtasks)
	r.Subtasks = &normalized
	return r
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
