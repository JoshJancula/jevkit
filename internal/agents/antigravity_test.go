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

func TestAntigravityLookupRegistered(t *testing.T) {
	got := agents.Lookup(agents.AntigravityName)
	if got == nil || got.Name() != agents.AntigravityName {
		t.Fatalf("antigravity not registered: %+v", got)
	}
	caps := got.Capabilities()
	if !caps.PreTool || !caps.PreToolRewrite || caps.PostTool {
		t.Fatalf("unexpected caps: %+v", caps)
	}
	if caps.OutputReplace {
		t.Fatalf("agy has no output replace: %+v", caps)
	}
}

func TestAntigravityPreToolRewritesRunCommand(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "hooks", "antigravity", "pretooluse-run-command.json"))
	if err != nil {
		t.Fatal(err)
	}
	a := agents.NewAntigravity()
	resp, err := a.HandlePreTool(context.Background(), agents.Request{
		Raw:   json.RawMessage(raw),
		Event: agents.EventPreTool,
	})
	if err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	if err := json.Unmarshal(resp.Body, &got); err != nil {
		t.Fatalf("body %s: %v", resp.Body, err)
	}
	gotCmd := nestedString(got, "overwrite", "CommandLine")
	if got["decision"] != "allow" {
		t.Fatalf("decision %v", got["decision"])
	}
	if !strings.Contains(gotCmd, "_runtime shell-wrapper") || !strings.Contains(gotCmd, "go test ./...") {
		t.Fatalf("pre-tool must rewrite, got %q", gotCmd)
	}
}

func TestAntigravityPreToolDoesNotMutateExistingCommand(t *testing.T) {
	payload := map[string]any{
		"toolCall": map[string]any{
			"name": "run_command",
			"args": map[string]any{"CommandLine": "jevkit _runtime shell-wrapper --command 'go test ./...'"},
		},
		"workspacePaths": []string{"/tmp/proj"},
	}
	raw, _ := json.Marshal(payload)
	a := agents.NewAntigravity()
	resp, err := a.HandlePreTool(context.Background(), agents.Request{
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
	cmd := nestedString(got, "overwrite", "CommandLine")
	if cmd != "" {
		t.Fatalf("pre-tool must not mutate wrapper: %q", cmd)
	}
}

func TestAntigravityPreToolNonRunCommandAllow(t *testing.T) {
	payload := map[string]any{
		"toolCall": map[string]any{
			"name": "read_file",
			"args": map[string]any{"path": "README.md"},
		},
	}
	raw, _ := json.Marshal(payload)
	a := agents.NewAntigravity()
	resp, err := a.HandlePreTool(context.Background(), agents.Request{
		Raw:   raw,
		Event: agents.EventPreTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(bytes.TrimSpace(resp.Body)) != `{"decision":"allow"}` {
		t.Fatalf("non-run_command must allow-passthrough, got %s", resp.Body)
	}
}

func TestAntigravityPassthroughAlwaysDecision(t *testing.T) {
	a := agents.NewAntigravity()
	for _, ev := range []agents.Event{agents.EventPreTool, agents.EventPostTool, agents.EventStop} {
		body := a.Passthrough(ev)
		var got map[string]any
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("%s: %v body=%s", ev, err, body)
		}
		if got["decision"] != "allow" {
			t.Fatalf("%s missing decision allow: %s", ev, body)
		}
	}
}

func TestAntigravityInstallIdempotentAndUninstallRestoresBytes(t *testing.T) {
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, ".agents")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hooksPath := filepath.Join(hooksDir, "hooks.json")
	original := []byte("{\n  \"ralph-native\": {\n    \"Stop\": [\n      {\n        \"command\": \"./my-stop.sh\",\n        \"timeout\": 60\n      }\n    ]\n  }\n}\n")
	if err := os.WriteFile(hooksPath, original, 0o644); err != nil {
		t.Fatal(err)
	}

	a := agents.NewAntigravity()
	opts := agents.InstallOptions{
		WorkDir: dir,
		Scope:   "project",
		Binary:  "/opt/jevkit",
	}
	if err := a.Install(opts); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := a.Install(opts); err != nil {
		t.Fatalf("second install: %v", err)
	}

	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	markerCount := strings.Count(string(data), agents.AntigravityPreToolMarker)
	if markerCount != 1 {
		t.Fatalf("expected one pre-tool entry, found %d in:\n%s", markerCount, data)
	}
	if !strings.Contains(string(data), `"`+agents.AntigravityHooksGroup+`"`) {
		t.Fatalf("missing jevkit group: %s", data)
	}
	if !strings.Contains(string(data), `"PreToolUse"`) {
		t.Fatalf("missing PreToolUse: %s", data)
	}
	if !strings.Contains(string(data), `"matcher": "run_command"`) {
		t.Fatalf("missing run_command matcher: %s", data)
	}
	if !strings.Contains(string(data), `"./my-stop.sh"`) {
		t.Fatalf("lost unrelated ralph-native stop hook: %s", data)
	}
	if !strings.Contains(string(data), "/opt/jevkit "+agents.AntigravityPreToolMarker) {
		t.Fatalf("missing pre command: %s", data)
	}

	if err := a.Uninstall(opts); err != nil {
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

func TestAntigravityInstallDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	a := agents.NewAntigravity()
	opts := agents.InstallOptions{
		WorkDir: dir,
		Scope:   "project",
		Binary:  "jevkit",
		DryRun:  true,
	}
	if err := a.Install(opts); err != nil {
		t.Fatal(err)
	}
	hooksPath := filepath.Join(dir, ".agents", "hooks.json")
	if _, err := os.Stat(hooksPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run must not create hooks.json: %v", err)
	}
}

func TestAntigravityInstallUserScopeAndAbsentRestore(t *testing.T) {
	home := t.TempDir()
	a := agents.NewAntigravity()
	opts := agents.InstallOptions{
		ConfigDir: home,
		Scope:     "user",
		Binary:    "jevkit",
	}
	if err := a.Install(opts); err != nil {
		t.Fatal(err)
	}
	hooksPath := filepath.Join(home, ".agents", "hooks.json")
	if _, err := os.Stat(hooksPath); err != nil {
		t.Fatal(err)
	}
	if err := a.Install(opts); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), agents.AntigravityPreToolMarker) != 1 {
		t.Fatalf("not idempotent: %s", data)
	}
	if err := a.Uninstall(opts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(hooksPath); !os.IsNotExist(err) {
		t.Fatalf("absent original should remove hooks.json on uninstall: %v", err)
	}
}
