package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/OWNER/jevkit/internal/sdlc/adaptive"
	"github.com/OWNER/jevkit/internal/sdlc/enrollment"
)

func requireUnixShellFixture(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fixture executes a Unix shell script")
	}
}

func TestCommandUsesEnrolledRuntimeAndReadOnlyMode(t *testing.T) {
	req := Request{Agent: enrollment.Agent{Via: enrollment.Runtime, Runtime: "codex", Model: "selected-model"}, Assignment: adaptive.Assignment{Role: "assessor"}}
	bin, args, err := command(req)
	if err != nil || bin != "codex" || strings.Join(args, " ") != "exec --model selected-model --sandbox read-only" {
		t.Fatalf("codex command: %s %v %v", bin, args, err)
	}
	req.Agent.Runtime = "cursor"
	bin, args, err = command(req)
	if err != nil || bin != "cursor-agent" || !strings.Contains(strings.Join(args, " "), "--mode ask") || !strings.Contains(strings.Join(args, " "), "--output-format stream-json") {
		t.Fatalf("cursor command: %s %v %v", bin, args, err)
	}
	req.Agent.Runtime = "opencode"
	req.Assignment.ReadOnly = true
	if _, _, err := command(req); err == nil {
		t.Fatal("OpenCode read-only assignment was accepted")
	}
}

func TestCursorStreamResultKeepsSessionAndMeasuredUsage(t *testing.T) {
	raw := []byte("{\"type\":\"system\",\"session_id\":\"cursor-session\"}\n" +
		"{\"type\":\"tool_call\",\"subtype\":\"started\",\"tool_call\":{\"readToolCall\":{}}}\n" +
		"{\"type\":\"result\",\"result\":\"{\\\"outcome\\\":\\\"planned\\\",\\\"content\\\":\\\"Plan\\\"}\",\"usage\":{\"inputTokens\":12,\"outputTokens\":3}}\n")
	result, session, input, output, ok := cursorResult(raw)
	if !ok || session != "cursor-session" || input == nil || *input != 12 || output == nil || *output != 3 {
		t.Fatalf("cursor result metadata: %v %s %v %v", ok, session, input, output)
	}
	reply, err := ParseReply(result)
	if err != nil || reply.Outcome != "planned" {
		t.Fatalf("cursor result: %+v %v", reply, err)
	}
}

func TestLargeReviewDiffUsesSavedPatchWithoutOversizedPrompt(t *testing.T) {
	req := Request{Agent: enrollment.Agent{ID: "reviewer"}, Assignment: adaptive.Assignment{Role: "assessor", Revision: "sha256"}, Diff: strings.Repeat("x", 12*1024*1024), DiffPath: "/saved/patch.diff"}
	prompt := makePrompt(req)
	if len(prompt) > 70*1024 || !strings.Contains(prompt, "/saved/patch.diff") || !strings.Contains(prompt, "sha256") {
		t.Fatalf("review prompt is too large or lost patch reference: %d bytes", len(prompt))
	}
}

func TestPlannerPromptUsesBareOutcomeExample(t *testing.T) {
	req := Request{Agent: enrollment.Agent{ID: "planner"}, Assignment: adaptive.Assignment{Role: "planner"}, Task: "Write a plan"}
	prompt := makePrompt(req)
	if !strings.Contains(prompt, `{"outcome":"planned","content":"..."}`) || !strings.Contains(prompt, "Never include the role name in the outcome value") || strings.Contains(prompt, "Valid outcomes: planner: planned") {
		t.Fatalf("ambiguous planner prompt: %q", prompt)
	}
}

func TestClaudeNamedAgentIsPassedToCLI(t *testing.T) {
	req := Request{Agent: enrollment.Agent{Via: enrollment.Runtime, Runtime: "claude", Model: "opus", RuntimeAgent: "security-reviewer"}, Assignment: adaptive.Assignment{Role: "assessor"}}
	_, args, err := command(req)
	if err != nil || !strings.Contains(strings.Join(args, " "), "--agent security-reviewer") {
		t.Fatalf("Claude named agent arguments: %v %v", args, err)
	}
}

func TestWorkerSandboxChecksWorkDirAndTaskOnly(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	file := filepath.Join(outside, "task.md")
	req := Request{
		Agent:      enrollment.Agent{Via: enrollment.Runtime, Runtime: "claude"},
		Assignment: adaptive.Assignment{Runtime: "claude"},
		WorkDir:    outside, Workspace: workspace, Task: "build feature",
	}
	_, err := (CLIExecutor{}).Execute(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "sandbox") {
		t.Fatalf("outside WorkDir: %v", err)
	}
	req.WorkDir = workspace
	req.Task = "read " + file
	req.Plan = "plan references /etc/passwd"
	req.Diff = "diff references /etc/passwd"
	_, err = (CLIExecutor{}).Execute(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "sandbox") {
		t.Fatalf("outside task path: %v", err)
	}
	req.AllowRead = []string{file}
	_, err = (CLIExecutor{}).Execute(context.Background(), req)
	if err != nil && strings.Contains(err.Error(), "sandbox") {
		t.Fatalf("allowlisted task or generated plan/diff rejected: %v", err)
	}
	req.AllowRead = nil
	req.Yolo = true
	_, err = (CLIExecutor{}).Execute(context.Background(), req)
	if err != nil && strings.Contains(err.Error(), "sandbox") {
		t.Fatalf("yolo did not lift guard: %v", err)
	}
}

func TestCommandUsesExplicitSession(t *testing.T) {
	req := Request{Agent: enrollment.Agent{Via: enrollment.Runtime, Runtime: "codex", Model: "m"}, Assignment: adaptive.Assignment{Role: "assessor", ReadOnly: true}, SessionID: "session-1", CaptureSession: true}
	_, args, err := command(req)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"exec resume session-1", "sandbox_mode=read-only", "--json"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %q", want, joined)
		}
	}
	req.Agent.Runtime = "claude"
	_, args, err = command(req)
	if err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(args, " ")
	for _, want := range []string{"--output-format json", "--resume session-1"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %q", want, joined)
		}
	}
}

func TestClaudeEnvelopeCapturesSessionAndUsage(t *testing.T) {
	requireUnixShellFixture(t)
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "initial"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	bin := filepath.Join(dir, "fake-claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho '{\"result\":\"{\\\"outcome\\\":\\\"planned\\\",\\\"content\\\":\\\"Plan\\\"}\",\"session_id\":\"explicit-session\",\"usage\":{\"input_tokens\":12,\"output_tokens\":5}}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	req := Request{Agent: enrollment.Agent{ID: "claude-builder", Via: enrollment.Runtime, Runtime: "claude", Model: "model", Binary: bin}, Assignment: adaptive.Assignment{InvocationID: "inv", AgentID: "claude-builder", Runtime: "claude", Role: "planner"}, WorkDir: dir, CaptureSession: true}
	reply, err := (CLIExecutor{}).Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Outcome != "planned" || reply.SessionID != "explicit-session" || reply.InputTokens == nil || *reply.InputTokens != 12 || reply.OutputTokens == nil || *reply.OutputTokens != 5 {
		t.Fatalf("reply: %+v", reply)
	}
}

func TestInvalidRoleOutcomeCanRerouteWithoutPausing(t *testing.T) {
	requireUnixShellFixture(t)
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	bin := filepath.Join(dir, "fake-claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho '{\"outcome\":\"approved\",\"content\":\"Looks fine\"}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	req := Request{Agent: enrollment.Agent{ID: "planner", Via: enrollment.Runtime, Runtime: "claude", Model: "model", Binary: bin}, Assignment: adaptive.Assignment{InvocationID: "inv", AgentID: "planner", Runtime: "claude", Role: "planner"}, WorkDir: dir}
	_, err := (CLIExecutor{}).Execute(context.Background(), req)
	var invocation *InvocationFailure
	if !errors.As(err, &invocation) || !strings.Contains(err.Error(), `planner reply outcome "approved" is not allowed`) {
		t.Fatalf("invalid role outcome was not retryable: %v", err)
	}
}

func TestAssessorReplySurvivesWorkspaceDrift(t *testing.T) {
	requireUnixShellFixture(t)
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	bin := filepath.Join(dir, "fake-claude")
	script := "#!/bin/sh\nprintf changed > drift.txt\necho '{\"outcome\":\"changes-required\",\"content\":\"Review findings\"}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	req := Request{Agent: enrollment.Agent{ID: "reviewer", Via: enrollment.Runtime, Runtime: "claude", Model: "model", Binary: bin},
		Assignment: adaptive.Assignment{InvocationID: "inv", AgentID: "reviewer", Runtime: "claude", Role: "assessor"}, WorkDir: dir, Yolo: true}
	reply, err := (CLIExecutor{}).Execute(context.Background(), req)
	if err != nil || reply.Outcome != "changes-required" || reply.Content != "Review findings" || len(reply.WorkspaceDrift) != 1 || reply.WorkspaceDrift[0] != "drift.txt" {
		t.Fatalf("reply=%+v err=%v", reply, err)
	}
}

func TestFailedInvocationKeepsReportedUsage(t *testing.T) {
	raw := []byte("{\"type\":\"step_finish\",\"part\":{\"tokens\":{\"input\":30,\"output\":4},\"cost\":0.02}}\n" +
		"{\"type\":\"step_finish\",\"part\":{\"tokens\":{\"input\":1,\"output\":1},\"cost\":0.005}}\n")
	reply := failedUsage("opencode", raw)
	if reply.InputTokens == nil || *reply.InputTokens != 31 || reply.OutputTokens == nil || *reply.OutputTokens != 5 || !reply.CostReported || reply.CostUSD != 0.025 {
		t.Fatalf("failed usage: %+v", reply)
	}
	unknown := failedUsage("opencode", []byte("{\"type\":\"error\"}\n"))
	if unknown.InputTokens != nil || unknown.OutputTokens != nil || unknown.CostReported {
		t.Fatalf("unknown counts should stay unknown: %+v", unknown)
	}
}

func TestOpenCodeReadsReviewArtifactInsideWorkspace(t *testing.T) {
	requireUnixShellFixture(t)
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	bin := filepath.Join(dir, "fake-opencode")
	script := `#!/bin/sh
for arg in "$@"; do prompt="$arg"; done
path=$(printf '%s\n' "$prompt" | sed -n 's/^Saved full change artifact: //p')
if [ ! -f "$path" ] || ! grep -q '+new' "$path"; then
  echo 'review artifact unavailable' >&2
  exit 1
fi
echo '{"type":"text","sessionID":"session-1","part":{"type":"text","text":"{\"outcome\":\"approved\",\"content\":\"Reviewed\"}"}}'
echo '{"type":"step_finish","part":{"tokens":{"input":30,"output":4},"cost":0.02}}'
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	req := Request{Agent: enrollment.Agent{ID: "reviewer", Via: enrollment.Runtime, Runtime: "opencode", Model: "provider/model", Binary: bin},
		Assignment: adaptive.Assignment{InvocationID: "inv", AgentID: "reviewer", Runtime: "opencode", Role: "assessor"}, WorkDir: dir, CaptureSession: true,
		Diff: "diff --git a/a b/a\n+new\n", DiffPath: "/outside/artifacts/patch.diff"}
	reply, err := (CLIExecutor{}).Execute(context.Background(), req)
	if err != nil || reply.Outcome != "approved" || reply.InputTokens == nil || *reply.InputTokens != 30 || reply.CostUSD != 0.02 {
		t.Fatalf("OpenCode reply=%+v err=%v", reply, err)
	}
	if matches, err := filepath.Glob(filepath.Join(dir, ".jevkit-review-*.diff")); err != nil || len(matches) != 0 {
		t.Fatalf("temporary review artifact left behind: %v %v", matches, err)
	}
}

func TestOpenCodeNoTextReportsCauseAndUsage(t *testing.T) {
	requireUnixShellFixture(t)
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	bin := filepath.Join(dir, "fake-opencode")
	script := `#!/bin/sh
echo 'permission requested: external_directory; auto-rejecting' >&2
echo '{"type":"step_finish","part":{"tokens":{"input":30,"output":4},"cost":0.02}}'
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	req := Request{Agent: enrollment.Agent{ID: "reviewer", Via: enrollment.Runtime, Runtime: "opencode", Model: "provider/model", Binary: bin},
		Assignment: adaptive.Assignment{InvocationID: "inv", AgentID: "reviewer", Runtime: "opencode", Role: "assessor"}, WorkDir: dir, CaptureSession: true}
	reply, err := (CLIExecutor{}).Execute(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "returned no text reply") || !strings.Contains(err.Error(), "auto-rejecting") {
		t.Fatalf("OpenCode error=%v", err)
	}
	if reply.InputTokens == nil || *reply.InputTokens != 30 || reply.CostUSD != 0.02 {
		t.Fatalf("lost usage on failed reply: %+v", reply)
	}
}

func TestCursorScopedPlannerOutcomeIsAccepted(t *testing.T) {
	requireUnixShellFixture(t)
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	bin := filepath.Join(dir, "fake-cursor")
	script := `#!/bin/sh
echo '{"type":"system","session_id":"session-1"}'
echo '{"type":"result","result":"{\"outcome\":\"planner: planned\",\"content\":\"Plan\"}"}'
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	req := Request{Agent: enrollment.Agent{ID: "planner", Via: enrollment.Runtime, Runtime: "cursor", Model: "model", Binary: bin}, Assignment: adaptive.Assignment{InvocationID: "inv", AgentID: "planner", Runtime: "cursor", Role: "planner"}, WorkDir: dir, CaptureSession: true}
	reply, err := (CLIExecutor{}).Execute(context.Background(), req)
	if err != nil || reply.Outcome != "planned" || reply.ReportedOutcome != "planner: planned" || reply.SessionID != "session-1" {
		t.Fatalf("scoped Cursor outcome: %+v %v", reply, err)
	}
}

func TestClaudeStreamFeedsLiveActivityAndParsesResult(t *testing.T) {
	requireUnixShellFixture(t)
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "initial"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	bin := filepath.Join(dir, "fake-claude")
	script := `#!/bin/sh
echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read"}]}}'
echo '{"type":"result","result":"{\"outcome\":\"planned\",\"content\":\"Plan\"}","session_id":"session-1","usage":{"input_tokens":4,"output_tokens":5}}'
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	var lines []string
	req := Request{Agent: enrollment.Agent{ID: "planner", Via: enrollment.Runtime, Runtime: "claude", Model: "opus", Binary: bin}, Assignment: adaptive.Assignment{InvocationID: "inv", AgentID: "planner", Runtime: "claude", Role: "planner"}, WorkDir: dir, LogDir: t.TempDir(), CaptureSession: true, LiveOutput: func(_, line string) { lines = append(lines, line) }}
	reply, err := (CLIExecutor{}).Execute(context.Background(), req)
	if err != nil || reply.Outcome != "planned" || reply.SessionID != "session-1" || len(lines) != 2 {
		t.Fatalf("stream reply %+v, lines %d, err %v", reply, len(lines), err)
	}
}

func TestAntigravityCapturesConversationAndUsage(t *testing.T) {
	requireUnixShellFixture(t)
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "initial"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	bin := filepath.Join(dir, "fake-agy")
	script := `#!/bin/sh
echo '{"event":"init","conversation_id":"agy-123"}'
echo '{"event":"result","result":{"conversation_id":"agy-123","status":"SUCCESS","response":"{\"outcome\":\"planned\",\"content\":\"Plan\"}","usage":{"input_tokens":30,"output_tokens":7}}}'
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	req := Request{Agent: enrollment.Agent{ID: "agy-planner", Via: enrollment.Runtime, Runtime: "antigravity", Model: "Gemini Exact Display", Binary: bin}, Assignment: adaptive.Assignment{InvocationID: "inv", AgentID: "agy-planner", Runtime: "antigravity", Role: "planner", ReadOnly: true}, WorkDir: dir, CaptureSession: true}
	reply, err := (CLIExecutor{}).Execute(context.Background(), req)
	if err != nil || reply.Outcome != "planned" || reply.SessionID != "agy-123" || reply.InputTokens == nil || *reply.InputTokens != 30 {
		t.Fatalf("reply: %+v %v", reply, err)
	}
	_, args, err := command(Request{Agent: req.Agent, Assignment: req.Assignment, SessionID: "agy-123"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"--conversation agy-123", "--mode plan", "--model Gemini Exact Display"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %q", want, joined)
		}
	}
}

func TestClaudeCompactUsesCLIStreamWithoutSDK(t *testing.T) {
	requireUnixShellFixture(t)
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "initial"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	bin := filepath.Join(dir, "fake-claude")
	inputPath := filepath.Join(t.TempDir(), "compact-input.txt")
	script := fmt.Sprintf(`#!/bin/sh
cat > '%s'
echo '{"type":"system","subtype":"compact_boundary"}'
echo '{"type":"result","subtype":"success","session_id":"claude-123","result":"{\"outcome\":\"planned\",\"content\":\"Plan\"}","usage":{"input_tokens":12,"output_tokens":4}}'
`, inputPath)
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	req := Request{Agent: enrollment.Agent{ID: "claude", Via: enrollment.Runtime, Runtime: "claude", Model: "m", Binary: bin}, Assignment: adaptive.Assignment{InvocationID: "inv", AgentID: "claude", Runtime: "claude", Role: "planner"}, WorkDir: dir, SessionID: "claude-123", CaptureSession: true, Compact: true}
	reply, err := (CLIExecutor{}).Execute(context.Background(), req)
	if err != nil || !reply.CompactCompleted || reply.SessionID != "claude-123" {
		t.Fatalf("compact reply: %+v %v", reply, err)
	}
	input, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(input)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"content":"/compact"`) || !strings.Contains(lines[1], `"content":"You are enrolled`) {
		t.Fatalf("compact input: %q", input)
	}
	_, args, err := command(req)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--input-format stream-json") || !strings.Contains(joined, "--output-format stream-json") {
		t.Fatalf("command: %q", joined)
	}
}

func TestChangeReportExcludesPreexistingBinary(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "initial"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "jevkit"), bytes.Repeat([]byte{0, 1, 2, 3}, 1024*1024), 0o700); err != nil {
		t.Fatal(err)
	}
	snapshot, err := newWorkspaceSnapshot(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.close()
	before, err := snapshot.capture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := snapshot.capture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	patch, err := snapshot.report(context.Background(), before, after)
	if err != nil || !strings.Contains(patch, "+new content") || strings.Contains(patch, "jevkit") || len(patch) > maxChangeReport {
		t.Fatalf("change report: %q %v", patch, err)
	}
	checkIndex := exec.Command("git", "ls-files", "--cached", "--", "jevkit", "new.txt")
	checkIndex.Dir = dir
	if out, err := checkIndex.Output(); err != nil || len(out) != 0 {
		t.Fatalf("snapshot modified user's Git index: %q %v", out, err)
	}
	checkObjects := exec.Command("git", "cat-file", "-e", after+"^{tree}")
	checkObjects.Dir = dir
	if err := checkObjects.Run(); err == nil {
		t.Fatal("snapshot wrote its tree into the repository object store")
	}
}

func TestCLIExecutorHandoffOmitsPreexistingBinary(t *testing.T) {
	requireUnixShellFixture(t)
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "initial"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "jevkit"), bytes.Repeat([]byte{0, 1, 2, 3}, 1024*1024), 0o700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "fake-claude")
	script := "#!/bin/sh\nprintf 'new code\\n' > new.go\necho '{\"result\":\"{\\\"outcome\\\":\\\"changed\\\",\\\"content\\\":\\\"done\\\"}\"}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	req := Request{Agent: enrollment.Agent{ID: "builder", Via: enrollment.Runtime, Runtime: "claude", Model: "sonnet", Binary: bin}, Assignment: adaptive.Assignment{InvocationID: "inv", AgentID: "builder", Runtime: "claude", Role: "implementer"}, WorkDir: dir, Task: "add new.go", CaptureSession: true}
	reply, err := (CLIExecutor{}).Execute(context.Background(), req)
	if err != nil || reply.Outcome != "changed" || !strings.Contains(reply.Content, "+new code") || strings.Contains(reply.Content, "jevkit") || len(reply.Content) > maxChangeReport {
		t.Fatalf("handoff: %+v %v", reply, err)
	}
}

func TestChangedBinaryIsDescribedWithoutPayload(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "initial"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	snapshot, err := newWorkspaceSnapshot(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.close()
	before, err := snapshot.capture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "image.bin"), bytes.Repeat([]byte{0, 1, 2, 3}, 1024*1024), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := snapshot.capture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	report, err := snapshot.report(context.Background(), before, after)
	if err != nil || len(report) > maxChangeReport || !strings.Contains(report, "image.bin") || !strings.Contains(report, "Binary files") || strings.Contains(report, "GIT binary patch") {
		t.Fatalf("binary report: %d bytes %v %q", len(report), err, report)
	}
}

func TestLargeTextChangeReportIsBounded(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "initial"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	snapshot, err := newWorkspaceSnapshot(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.close()
	before, err := snapshot.capture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "large.txt"), bytes.Repeat([]byte("a long changed line\n"), 100000), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := snapshot.capture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	report, err := snapshot.report(context.Background(), before, after)
	if err != nil || len(report) > maxChangeReport || !strings.Contains(report, "truncated") || !strings.Contains(report, "large.txt") {
		t.Fatalf("large report: %d bytes %v", len(report), err)
	}
}

func TestInvocationFailureRequiresUnchangedWorkspace(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "initial"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	snapshot, err := newWorkspaceSnapshot(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.close()
	before, err := snapshot.capture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("runtime exited")
	var invocation *InvocationFailure
	if err := retryableIfUnchanged(context.Background(), snapshot, before, cause); !errors.As(err, &invocation) {
		t.Fatalf("unchanged workspace should allow reroute: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "changed.txt"), []byte("change"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := retryableIfUnchanged(context.Background(), snapshot, before, cause); !errors.Is(err, cause) || errors.As(err, &invocation) {
		t.Fatalf("changed workspace should stop reroute: %v", err)
	}
}

func TestParseReplyRequiresStructuredArtifact(t *testing.T) {
	r, err := ParseReply([]byte("```json\n{\"outcome\":\"planned\",\"content\":\"Plan.\"}\n```"))
	if err != nil || r.Outcome != "planned" || r.Content != "Plan." {
		t.Fatalf("reply: %+v %v", r, err)
	}
	r, err = ParseReply([]byte("I'll locate the source first.\n{\"outcome\":\"planned\",\"content\":\"Replace the comment.\"}"))
	if err != nil || r.Outcome != "planned" || r.Content != "Replace the comment." {
		t.Fatalf("prose-prefixed JSON: %+v %v", r, err)
	}
	r, err = ParseReply([]byte(`{"outcome":"planner: planned","content":"Plan."}`))
	if err != nil || r.Outcome != "planned" || r.ReportedOutcome != "planner: planned" {
		t.Fatalf("scoped outcome: %+v %v", r, err)
	}
	if !allowedOutcome("planner", r.Outcome) || allowedOutcome("planner", "approved") {
		t.Fatal("role outcome validation is incorrect")
	}
	if _, err := ParseReply([]byte(`{"outcome":"changed"}`)); err == nil {
		t.Fatal("empty artifact accepted")
	}
	if _, err := ParseReply([]byte("changed")); err == nil {
		t.Fatal("unstructured reply accepted")
	}
	withZero, err := ParseReply([]byte(`{"outcome":"answer","costUsd":0}`))
	if err != nil || !withZero.CostReported {
		t.Fatalf("reported zero cost lost: %+v %v", withZero, err)
	}
	without, err := ParseReply([]byte(`{"outcome":"answer"}`))
	if err != nil || without.CostReported {
		t.Fatalf("missing cost invented: %+v %v", without, err)
	}
}
