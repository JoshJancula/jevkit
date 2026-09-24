// Package worker invokes enrolled CLI agents and parses their structured reply.
package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/OWNER/jevkit/internal/sdlc/adaptive"
	"github.com/OWNER/jevkit/internal/sdlc/enrollment"
)

type Request struct {
	Agent      enrollment.Agent
	Assignment adaptive.Assignment
	Task       string
	Plan       string
	Diff       string
	WorkDir    string
}

type Reply struct {
	Outcome string  `json:"outcome"`
	Content string  `json:"content"`
	CostUSD float64 `json:"costUsd"`
}

type Executor interface {
	Execute(context.Context, Request) (Reply, error)
}

type CLIExecutor struct{}

// Execute uses argv, never a shell. The caller has already checked enrollment
// and reach, and must persist the returned result against the assignment.
func (CLIExecutor) Execute(ctx context.Context, req Request) (Reply, error) {
	if req.Agent.Via != enrollment.Runtime || req.Agent.Runtime != req.Assignment.Runtime {
		return Reply{}, fmt.Errorf("worker: assignment is not a matching CLI runtime")
	}
	if req.Assignment.Isolated || len(req.Assignment.AgentWriteScopes)+len(req.Assignment.ProjectWriteScopes) > 0 {
		return Reply{}, fmt.Errorf("worker: requested isolation or write scopes cannot be enforced by CLI adapter")
	}
	prompt := makePrompt(req)
	binary, args, err := command(req)
	if err != nil {
		return Reply{}, err
	}
	before, err := workspaceDiff(ctx, req.WorkDir)
	if err != nil {
		if ctx.Err() != nil {
			return Reply{}, ctx.Err()
		}
		return Reply{}, err
	}
	var resultPath string
	if req.Agent.Runtime == "codex" {
		dir, err := os.MkdirTemp("", "jevkit-sdlc-worker-")
		if err != nil {
			return Reply{}, err
		}
		defer os.RemoveAll(dir)
		resultPath = filepath.Join(dir, "result.json")
		args = append(args, "--output-last-message", resultPath, "-")
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = req.WorkDir
	cmd.Stdin = strings.NewReader(prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return Reply{}, ctx.Err()
		}
		msg := stderr.String() + " " + stdout.String()
		if authError(msg) {
			return Reply{Outcome: "auth-failed"}, nil
		}
		return Reply{Outcome: "failed"}, fmt.Errorf("worker: %s exited: %w: %s", req.Agent.Runtime, err, truncate(msg, 500))
	}
	raw := stdout.Bytes()
	if resultPath != "" {
		raw, err = os.ReadFile(resultPath)
		if err != nil {
			return Reply{}, fmt.Errorf("worker: read Codex result: %w", err)
		}
	}
	if req.Agent.Runtime == "cursor" {
		var envelope struct {
			Result string `json:"result"`
		}
		if json.Unmarshal(raw, &envelope) == nil && envelope.Result != "" {
			raw = []byte(envelope.Result)
		}
	}
	reply, err := ParseReply(raw)
	if err != nil {
		return Reply{}, err
	}
	after, err := workspaceDiff(ctx, req.WorkDir)
	if err != nil {
		if ctx.Err() != nil {
			return Reply{}, ctx.Err()
		}
		return Reply{}, err
	}
	if req.Assignment.Role != "implementer" && !bytes.Equal(before, after) {
		return Reply{}, fmt.Errorf("worker: non-implementer changed the workspace")
	}
	if reply.Outcome == "changed" {
		if bytes.Equal(before, after) {
			return Reply{}, fmt.Errorf("worker: changed outcome has no workspace diff")
		}
		reply.Content = string(after)
	} else if req.Assignment.Role == "implementer" && !bytes.Equal(before, after) {
		return Reply{}, fmt.Errorf("worker: workspace changed without a changed outcome")
	}
	return reply, nil
}

// workspaceDiff includes tracked modifications and untracked files so the
// assessment revision names the bytes in the workspace after execution.
func workspaceDiff(ctx context.Context, dir string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", "--binary", "HEAD", "--")
	cmd.Dir = dir
	tracked, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("worker: capture git diff: %w", err)
	}
	cmd = exec.CommandContext(ctx, "git", "ls-files", "--others", "--exclude-standard", "-z")
	cmd.Dir = dir
	paths, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("worker: list untracked files: %w", err)
	}
	var out bytes.Buffer
	out.Write(tracked)
	for _, path := range bytes.Split(paths, []byte{0}) {
		if len(path) == 0 {
			continue
		}
		cmd = exec.CommandContext(ctx, "git", "diff", "--no-index", "--binary", "--", os.DevNull, string(path))
		cmd.Dir = dir
		patch, err := cmd.Output()
		if err != nil {
			if e, ok := err.(*exec.ExitError); !ok || e.ExitCode() != 1 {
				return nil, fmt.Errorf("worker: capture untracked diff: %w", err)
			}
		}
		out.Write(patch)
	}
	return out.Bytes(), nil
}

func command(req Request) (string, []string, error) {
	a := req.Agent
	bin := a.Binary
	if bin == "" {
		switch a.Runtime {
		case "cursor":
			bin = "cursor-agent"
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
		return bin, []string{"exec", "--model", a.Model, "--sandbox", sandbox}, nil
	case "claude":
		args := []string{"-p", "--model", a.Model, "--output-format", "text"}
		if req.Assignment.ReadOnly || req.Assignment.Role != "implementer" {
			args = append(args, "--permission-mode", "plan")
		}
		return bin, args, nil
	case "cursor":
		args := []string{"-p", "--output-format", "json", "--model", a.Model}
		if req.Assignment.ReadOnly || req.Assignment.Role != "implementer" {
			args = append(args, "--mode", "ask")
		}
		return bin, append(args, makePrompt(req)), nil
	case "opencode":
		if req.Assignment.ReadOnly {
			return "", nil, fmt.Errorf("worker: OpenCode CLI cannot enforce read-only assignment")
		}
		args := []string{"run", "--model", a.Model}
		if a.RuntimeAgent != "" {
			args = append(args, "--agent", a.RuntimeAgent)
		}
		return bin, append(args, makePrompt(req)), nil
	default:
		return "", nil, fmt.Errorf("worker: unsupported CLI runtime %q", a.Runtime)
	}
}

func makePrompt(req Request) string {
	return fmt.Sprintf("You are enrolled as agent %q for the %s role. Task: %s\nPlan revision: %s\nPlan:\n%s\nDiff revision: %s\nDiff:\n%s\nReturn exactly one JSON object with outcome and content fields. Valid outcomes: planner: planned, answer, no-change; implementer: changed, answer, no-change; assessor: approved, changes-required. For planned, content is the complete plan. For changed, content is a unified diff of the changes you made. For other outcomes, content is a concise explanation. Do not include Markdown fences. An assessor must review the exact diff revision and must not edit files.", req.Agent.ID, req.Assignment.Role, req.Task, req.Assignment.Revision, req.Plan, req.Assignment.Revision, req.Diff)
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
		return Reply{}, fmt.Errorf("worker: invalid structured reply: %w", err)
	}
	if r.Outcome == "" || r.CostUSD < 0 {
		return Reply{}, fmt.Errorf("worker: missing outcome or negative cost")
	}
	if (r.Outcome == "planned" || r.Outcome == "changed") && strings.TrimSpace(r.Content) == "" {
		return Reply{}, fmt.Errorf("worker: %s requires content", r.Outcome)
	}
	return r, nil
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
