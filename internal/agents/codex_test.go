package agents_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/OWNER/jevkit/internal/agents"
	"github.com/OWNER/jevkit/internal/usage"
)

func TestCodexLookupRegistered(t *testing.T) {
	got := agents.Lookup(agents.CodexName)
	if got == nil || got.Name() != agents.CodexName {
		t.Fatalf("codex not registered: %+v", got)
	}
	caps := got.Capabilities()
	if !caps.PreTool || !caps.PreToolRewrite || !caps.PostTool {
		t.Fatalf("unexpected caps: %+v", caps)
	}
	if caps.OutputReplace {
		t.Fatalf("codex PostToolUse replace is unproven: %+v", caps)
	}
}

func TestCodexPreToolRewritesBashToJevkitExec(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "hooks", "codex", "pretooluse-bash.json"))
	if err != nil {
		t.Fatal(err)
	}
	wantRaw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "hooks", "codex", "pretooluse-bash-response.json"))
	if err != nil {
		t.Fatal(err)
	}

	c := agents.NewCodex()
	resp, err := c.HandlePreTool(context.Background(), agents.Request{
		Raw:   json.RawMessage(raw),
		Event: agents.EventPreTool,
	})
	if err != nil {
		t.Fatal(err)
	}

	var got, want map[string]any
	if err := json.Unmarshal(resp.Body, &got); err != nil {
		t.Fatalf("body %s: %v", resp.Body, err)
	}
	if err := json.Unmarshal(wantRaw, &want); err != nil {
		t.Fatal(err)
	}
	gotCmd := nestedString(got, "hookSpecificOutput", "updatedInput", "command")
	wantCmd := nestedString(want, "hookSpecificOutput", "updatedInput", "command")
	if gotCmd != wantCmd {
		t.Fatalf("command\n got %q\nwant %q", gotCmd, wantCmd)
	}
	hso, _ := got["hookSpecificOutput"].(map[string]any)
	if hso["permissionDecision"] != "allow" {
		t.Fatalf("permissionDecision %v", hso["permissionDecision"])
	}
	if hso["hookEventName"] != "PreToolUse" {
		t.Fatalf("hookEventName %v", hso["hookEventName"])
	}
	if !strings.HasPrefix(gotCmd, "jevkit exec -- ") {
		t.Fatalf("expected jevkit exec rewrite, got %q", gotCmd)
	}
}

func TestCodexPreToolRewritesCommandExecution(t *testing.T) {
	payload := map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       "command_execution",
		"tool_input":      map[string]any{"command": "npm test"},
	}
	raw, _ := json.Marshal(payload)
	c := agents.NewCodex()
	resp, err := c.HandlePreTool(context.Background(), agents.Request{
		Raw:   raw,
		Event: agents.EventPreTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(resp.Body, &got); err != nil {
		t.Fatal(err)
	}
	cmd := nestedString(got, "hookSpecificOutput", "updatedInput", "command")
	if cmd != "jevkit exec -- npm test" {
		t.Fatalf("command_execution rewrite: %q", cmd)
	}
}

func TestCodexPreToolIdempotentAlreadyRewritten(t *testing.T) {
	payload := map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       "Bash",
		"tool_input":      map[string]any{"command": "jevkit exec -- go test ./..."},
	}
	raw, _ := json.Marshal(payload)
	c := agents.NewCodex()
	resp, err := c.HandlePreTool(context.Background(), agents.Request{
		Raw:   raw,
		Event: agents.EventPreTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(resp.Body, &got); err != nil {
		t.Fatal(err)
	}
	cmd := nestedString(got, "hookSpecificOutput", "updatedInput", "command")
	if cmd != "jevkit exec -- go test ./..." {
		t.Fatalf("double-wrapped: %q", cmd)
	}
	if strings.Count(cmd, "jevkit exec --") != 1 {
		t.Fatalf("expected single wrap: %q", cmd)
	}
}

func TestCodexPreToolNonShellPassthrough(t *testing.T) {
	payload := map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       "read_file",
		"tool_input":      map[string]any{"path": "README.md"},
	}
	raw, _ := json.Marshal(payload)
	c := agents.NewCodex()
	resp, err := c.HandlePreTool(context.Background(), agents.Request{
		Raw:   raw,
		Event: agents.EventPreTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(resp.Body, &got); err != nil {
		t.Fatalf("body %s: %v", resp.Body, err)
	}
	hso, _ := got["hookSpecificOutput"].(map[string]any)
	if hso["permissionDecision"] != "allow" {
		t.Fatalf("non-shell must allow-passthrough, got %s", resp.Body)
	}
	if _, ok := hso["updatedInput"]; ok {
		t.Fatalf("non-shell must not rewrite input: %s", resp.Body)
	}
}

func TestCodexPostToolTelemetryOnlyNoMutation(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "hooks", "codex", "posttooluse-bash.json"))
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var recs []usage.HookInvocation
	c := agents.NewCodex()
	code := agents.Run(context.Background(), c, agents.EventPostTool, bytes.NewReader(raw), &bytes.Buffer{}, agents.Options{
		StateDir: t.TempDir(),
		AppendHook: func(_ string, rec usage.HookInvocation) error {
			mu.Lock()
			defer mu.Unlock()
			recs = append(recs, rec)
			return nil
		},
	})
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if len(recs) != 1 {
		t.Fatalf("expected one telemetry record, got %d", len(recs))
	}
	if recs[0].Agent != agents.CodexName || recs[0].Event != string(agents.EventPostTool) {
		t.Fatalf("unexpected record: %+v", recs[0])
	}
	if recs[0].Tool != "Bash" {
		t.Fatalf("tool from fixture: %q", recs[0].Tool)
	}
	if recs[0].Outcome != agents.OutcomeOK {
		t.Fatalf("outcome: %q", recs[0].Outcome)
	}

	resp, err := c.HandlePostTool(context.Background(), agents.Request{
		Raw:   json.RawMessage(raw),
		Event: agents.EventPostTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(bytes.TrimSpace(resp.Body)) != `{}` {
		t.Fatalf("post-tool must be telemetry-only {}, got %s", resp.Body)
	}
}

func TestCodexInstallIdempotentAndUninstallRestoresBytes(t *testing.T) {
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, ".codex")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hooksPath := filepath.Join(hooksDir, "hooks.json")
	original := []byte("{\n  \"hooks\": {\n    \"Stop\": [\n      {\n        \"matcher\": \"\",\n        \"hooks\": [\n          {\n            \"type\": \"command\",\n            \"command\": \"./my-stop.sh\",\n            \"timeout\": 60\n          }\n        ]\n      }\n    ]\n  }\n}\n")
	if err := os.WriteFile(hooksPath, original, 0o644); err != nil {
		t.Fatal(err)
	}

	c := agents.NewCodex()
	opts := agents.InstallOptions{
		WorkDir: dir,
		Scope:   "project",
		Binary:  "/opt/jevkit",
	}
	if err := c.Install(opts); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := c.Install(opts); err != nil {
		t.Fatalf("second install: %v", err)
	}

	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	preCount := strings.Count(string(data), agents.CodexPreToolMarker)
	postCount := strings.Count(string(data), agents.CodexPostToolMarker)
	// Bash + command_execution for each event.
	if preCount != 2 {
		t.Fatalf("expected two pre-tool entries, found %d in:\n%s", preCount, data)
	}
	if postCount != 2 {
		t.Fatalf("expected two post-tool entries, found %d in:\n%s", postCount, data)
	}
	if !strings.Contains(string(data), `"PreToolUse"`) || !strings.Contains(string(data), `"PostToolUse"`) {
		t.Fatalf("missing PascalCase events: %s", data)
	}
	if !strings.Contains(string(data), `"matcher": "Bash"`) {
		t.Fatalf("missing Bash matcher: %s", data)
	}
	if !strings.Contains(string(data), `"matcher": "command_execution"`) {
		t.Fatalf("missing command_execution matcher: %s", data)
	}
	if !strings.Contains(string(data), `"./my-stop.sh"`) {
		t.Fatalf("lost unrelated Stop hook: %s", data)
	}
	if !strings.Contains(string(data), "/opt/jevkit "+agents.CodexPreToolMarker) {
		t.Fatalf("missing pre command: %s", data)
	}
	if !strings.Contains(string(data), `"type": "command"`) {
		t.Fatalf("missing type command: %s", data)
	}

	if err := c.Uninstall(opts); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	restored, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored, original) {
		t.Fatalf("uninstall did not restore original bytes\nwant:\n%s\ngot:\n%s", original, restored)
	}
	if _, err := os.Stat(hooksPath + ".jevkit-original"); !os.IsNotExist(err) {
		t.Fatalf("backup should be removed after uninstall: %v", err)
	}
}

func TestCodexInstallDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	c := agents.NewCodex()
	opts := agents.InstallOptions{
		WorkDir: dir,
		Scope:   "project",
		Binary:  "jevkit",
		DryRun:  true,
	}
	if err := c.Install(opts); err != nil {
		t.Fatal(err)
	}
	hooksPath := filepath.Join(dir, ".codex", "hooks.json")
	if _, err := os.Stat(hooksPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run must not create hooks.json: %v", err)
	}
}

func TestCodexInstallUserScopeAndAbsentRestore(t *testing.T) {
	home := t.TempDir()
	c := agents.NewCodex()
	opts := agents.InstallOptions{
		ConfigDir: home,
		Scope:     "user",
		Binary:    "jevkit",
	}
	if err := c.Install(opts); err != nil {
		t.Fatal(err)
	}
	hooksPath := filepath.Join(home, ".codex", "hooks.json")
	if _, err := os.Stat(hooksPath); err != nil {
		t.Fatal(err)
	}
	if err := c.Install(opts); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), agents.CodexPreToolMarker) != 2 {
		t.Fatalf("not idempotent: %s", data)
	}
	if err := c.Uninstall(opts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(hooksPath); !os.IsNotExist(err) {
		t.Fatalf("absent original should remove hooks.json on uninstall: %v", err)
	}
}
