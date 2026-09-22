package agents_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OWNER/jevkit/internal/agents"
)

func TestCursorLookupRegistered(t *testing.T) {
	got := agents.Lookup(agents.CursorName)
	if got == nil || got.Name() != agents.CursorName {
		t.Fatalf("cursor not registered: %+v", got)
	}
	caps := got.Capabilities()
	if !caps.PreTool || !caps.PreToolRewrite || !caps.PostTool || !caps.OutputReplace {
		t.Fatalf("unexpected caps: %+v", caps)
	}
}

func TestCursorPreToolRewritesShellToJevkitExec(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "hooks", "cursor", "pretooluse-shell.json"))
	if err != nil {
		t.Fatal(err)
	}
	wantRaw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "hooks", "cursor", "pretooluse-shell-response.json"))
	if err != nil {
		t.Fatal(err)
	}

	c := agents.NewCursor()
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
	gotCmd := nestedString(got, "updated_input", "command")
	wantCmd := nestedString(want, "updated_input", "command")
	if gotCmd != wantCmd {
		t.Fatalf("command\n got %q\nwant %q", gotCmd, wantCmd)
	}
	if got["permission"] != "allow" {
		t.Fatalf("permission %v", got["permission"])
	}
	if !strings.HasPrefix(gotCmd, "jevkit exec -- ") {
		t.Fatalf("expected jevkit exec rewrite, got %q", gotCmd)
	}
}

func TestCursorPreToolIdempotentAlreadyRewritten(t *testing.T) {
	payload := map[string]any{
		"hook_event_name": "preToolUse",
		"tool_name":       "Shell",
		"tool_input":      map[string]any{"command": "jevkit exec -- go test ./..."},
	}
	raw, _ := json.Marshal(payload)
	c := agents.NewCursor()
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
	cmd := nestedString(got, "updated_input", "command")
	if cmd != "jevkit exec -- go test ./..." {
		t.Fatalf("double-wrapped: %q", cmd)
	}
	if strings.Count(cmd, "jevkit exec --") != 1 {
		t.Fatalf("expected single wrap: %q", cmd)
	}
}

func TestCursorPreToolNonShellPassthrough(t *testing.T) {
	payload := map[string]any{
		"hook_event_name": "preToolUse",
		"tool_name":       "Read",
		"tool_input":      map[string]any{"path": "README.md"},
	}
	raw, _ := json.Marshal(payload)
	c := agents.NewCursor()
	resp, err := c.HandlePreTool(context.Background(), agents.Request{
		Raw:   raw,
		Event: agents.EventPreTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(bytes.TrimSpace(resp.Body)) != `{"permission":"allow"}` {
		t.Fatalf("non-Shell must allow-passthrough, got %s", resp.Body)
	}
}

func TestCursorPostToolShellTelemetryPassthrough(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "hooks", "cursor", "posttooluse-shell.json"))
	if err != nil {
		t.Fatal(err)
	}
	c := &agents.Cursor{
		Getenv:         envMap{"JEVKIT_COMPACT": "1"}.Getenv,
		ThresholdBytes: 1,
	}
	resp, err := c.HandlePostTool(context.Background(), agents.Request{
		Raw:   json.RawMessage(raw),
		Event: agents.EventPostTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(bytes.TrimSpace(resp.Body)) != "{}" {
		t.Fatalf("Shell postToolUse must not replace output, got %s", resp.Body)
	}
}

func TestCursorPostToolNativeResultCompact(t *testing.T) {
	text := largeCompactableOutput(80)
	payload := map[string]any{
		"hook_event_name": "postToolUse",
		"tool_name":       "Read",
		"tool_input":      map[string]any{"path": "build.log"},
		"tool_output": map[string]any{
			"content": text,
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	c := &agents.Cursor{
		Getenv:         envMap{"JEVKIT_COMPACT": "1"}.Getenv,
		ThresholdBytes: 200,
	}
	resp, err := c.HandlePostTool(context.Background(), agents.Request{
		Raw:   raw,
		Event: agents.EventPostTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		UpdatedToolOutput struct {
			Content string `json:"content"`
		} `json:"updated_tool_output"`
	}
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		t.Fatalf("body %s: %v", resp.Body, err)
	}
	if out.UpdatedToolOutput.Content == "" || len(out.UpdatedToolOutput.Content) >= len(text) {
		t.Fatalf("expected compacted native content, len=%d body=%s", len(out.UpdatedToolOutput.Content), resp.Body)
	}
}

func TestCursorPostToolMCPResultCompact(t *testing.T) {
	text := largeCompactableOutput(80)
	payload := map[string]any{
		"hook_event_name": "postToolUse",
		"tool_name":       "MCP:ralph_proxy_shell",
		"tool_output": map[string]any{
			"content": []any{
				map[string]any{"type": "text", "text": text},
			},
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	c := &agents.Cursor{
		Getenv:         envMap{"JEVKIT_COMPACT": "1"}.Getenv,
		ThresholdBytes: 200,
	}
	resp, err := c.HandlePostTool(context.Background(), agents.Request{
		Raw:   raw,
		Event: agents.EventPostTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		UpdatedMCPToolOutput struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"updated_mcp_tool_output"`
	}
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		t.Fatalf("body %s: %v", resp.Body, err)
	}
	if len(out.UpdatedMCPToolOutput.Content) == 0 {
		t.Fatalf("missing mcp content: %s", resp.Body)
	}
	got := out.UpdatedMCPToolOutput.Content[0].Text
	if got == "" || len(got) >= len(text) {
		t.Fatalf("expected compacted MCP text, len=%d body=%s", len(got), resp.Body)
	}
}

func TestCursorPostToolShadowAndDisabledPassthrough(t *testing.T) {
	text := largeCompactableOutput(80)
	payload := map[string]any{
		"hook_event_name": "postToolUse",
		"tool_name":       "Grep",
		"tool_output":     map[string]any{"content": text},
	}
	raw, _ := json.Marshal(payload)

	for _, env := range []envMap{
		{"JEVKIT_COMPACT": "1", "JEVKIT_COMPACT_SHADOW": "1"},
		{},
	} {
		c := &agents.Cursor{Getenv: env.Getenv, ThresholdBytes: 200}
		resp, err := c.HandlePostTool(context.Background(), agents.Request{
			Raw:   raw,
			Event: agents.EventPostTool,
		})
		if err != nil {
			t.Fatal(err)
		}
		if string(bytes.TrimSpace(resp.Body)) != "{}" {
			t.Fatalf("env %#v must passthrough, got %s", env, resp.Body)
		}
	}
}

func TestCursorInstallIdempotentAndUninstallRestoresBytes(t *testing.T) {
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, ".cursor")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hooksPath := filepath.Join(hooksDir, "hooks.json")
	original := []byte("{\n  \"version\": 1,\n  \"hooks\": {\n    \"stop\": [\n      {\n        \"command\": \"./my-stop.sh\"\n      }\n    ]\n  }\n}\n")
	if err := os.WriteFile(hooksPath, original, 0o644); err != nil {
		t.Fatal(err)
	}

	c := agents.NewCursor()
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
	preCount := strings.Count(string(data), agents.CursorPreToolMarker)
	postCount := strings.Count(string(data), agents.CursorPostToolMarker)
	if preCount != 1 {
		t.Fatalf("expected one pre-tool entry, found %d in:\n%s", preCount, data)
	}
	// Shell + native + MCP + afterShellExecution = 4 post-tool commands.
	if postCount != 4 {
		t.Fatalf("expected four post-tool entries, found %d in:\n%s", postCount, data)
	}
	if !strings.Contains(string(data), `"version": 1`) {
		t.Fatalf("missing version 1: %s", data)
	}
	if !strings.Contains(string(data), `"preToolUse"`) || !strings.Contains(string(data), `"afterShellExecution"`) {
		t.Fatalf("missing camelCase events: %s", data)
	}
	if !strings.Contains(string(data), `"./my-stop.sh"`) {
		t.Fatalf("lost unrelated stop hook: %s", data)
	}
	if !strings.Contains(string(data), "/opt/jevkit "+agents.CursorPreToolMarker) {
		t.Fatalf("missing pre command: %s", data)
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

func TestCursorInstallDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	c := agents.NewCursor()
	opts := agents.InstallOptions{
		WorkDir: dir,
		Scope:   "project",
		Binary:  "jevkit",
		DryRun:  true,
	}
	if err := c.Install(opts); err != nil {
		t.Fatal(err)
	}
	hooksPath := filepath.Join(dir, ".cursor", "hooks.json")
	if _, err := os.Stat(hooksPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run must not create hooks.json: %v", err)
	}
}

func TestCursorInstallUserScopeAndAbsentRestore(t *testing.T) {
	home := t.TempDir()
	c := agents.NewCursor()
	opts := agents.InstallOptions{
		ConfigDir: home,
		Scope:     "user",
		Binary:    "jevkit",
	}
	if err := c.Install(opts); err != nil {
		t.Fatal(err)
	}
	hooksPath := filepath.Join(home, ".cursor", "hooks.json")
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
	if strings.Count(string(data), agents.CursorPreToolMarker) != 1 {
		t.Fatalf("not idempotent: %s", data)
	}
	if err := c.Uninstall(opts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(hooksPath); !os.IsNotExist(err) {
		t.Fatalf("absent original should remove hooks.json on uninstall: %v", err)
	}
}

func nestedString(m map[string]any, keys ...string) string {
	var cur any = m
	for _, k := range keys {
		obj, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = obj[k]
	}
	s, _ := cur.(string)
	return s
}
