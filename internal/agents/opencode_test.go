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
	"github.com/OWNER/jevkit/internal/jev"
	"github.com/OWNER/jevkit/internal/usage"
)

func TestOpenCodeLookupRegistered(t *testing.T) {
	got := agents.Lookup(agents.OpenCodeName)
	if got == nil || got.Name() != agents.OpenCodeName {
		t.Fatalf("opencode not registered: %+v", got)
	}
	caps := got.Capabilities()
	if caps.PreTool || caps.PreToolRewrite || !caps.PostTool {
		t.Fatalf("unexpected caps: %+v", caps)
	}
	if !caps.OutputReplace {
		t.Fatalf("post-tool output replacement must be enabled: %+v", caps)
	}
}

func TestOpenCodePreToolDoesNotRewriteBash(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "hooks", "opencode", "tool-execute-before.json"))
	if err != nil {
		t.Fatal(err)
	}
	o := agents.NewOpenCode()
	resp, err := o.HandlePreTool(context.Background(), agents.Request{
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
	gotCmd, _ := got["command"].(string)
	if gotCmd != "" {
		t.Fatalf("pre-tool must not rewrite, got %q", gotCmd)
	}
}

func TestOpenCodePreToolDoesNotMutateExistingCommand(t *testing.T) {
	payload := map[string]any{
		"input":  map[string]any{"tool": "bash", "sessionID": "s", "callID": "c"},
		"output": map[string]any{"args": map[string]any{"command": "jevkit exec -- go test ./..."}},
	}
	raw, _ := json.Marshal(payload)
	o := agents.NewOpenCode()
	resp, err := o.HandlePreTool(context.Background(), agents.Request{
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
	cmd, _ := got["command"].(string)
	if cmd != "" {
		t.Fatalf("pre-tool must not mutate: %q", cmd)
	}
}

func TestOpenCodePreToolNonShellPassthrough(t *testing.T) {
	payload := map[string]any{
		"input":  map[string]any{"tool": "read", "sessionID": "s", "callID": "c"},
		"output": map[string]any{"args": map[string]any{"path": "README.md"}},
	}
	raw, _ := json.Marshal(payload)
	o := agents.NewOpenCode()
	resp, err := o.HandlePreTool(context.Background(), agents.Request{
		Raw:   raw,
		Event: agents.EventPreTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(bytes.TrimSpace(resp.Body)) != `{}` {
		t.Fatalf("non-shell must passthrough {}, got %s", resp.Body)
	}
}

func TestOpenCodePostToolCompactsOutput(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "hooks", "opencode", "tool-execute-after.json"))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	payload["output"] = map[string]any{"title": "go test ./...", "output": strings.Repeat("Downloading dependency package\n", 100), "metadata": map[string]any{}}
	raw, err = json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var recs []usage.HookInvocation
	o := &agents.OpenCode{Getenv: envMap{"JEVKIT_COMPACT": "1"}.Getenv, ThresholdBytes: 1, Asker: codexAsker{response: &jev.Response{Answers: map[string]jev.Answer{
		"disposition": jev.ChoiceAnswer{Choice: "deterministic-compact", Confidence: 0.99},
		"outcome":     jev.ChoiceAnswer{Choice: "success", Confidence: 0.99},
	}}}}
	code := agents.Run(context.Background(), o, agents.EventPostTool, bytes.NewReader(raw), &bytes.Buffer{}, agents.Options{
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
	if recs[0].Agent != agents.OpenCodeName || recs[0].Event != string(agents.EventPostTool) {
		t.Fatalf("unexpected record: %+v", recs[0])
	}
	if recs[0].Tool != "bash" {
		t.Fatalf("tool from fixture: %q", recs[0].Tool)
	}
	if recs[0].Outcome != agents.OutcomeOK {
		t.Fatalf("outcome: %q", recs[0].Outcome)
	}

	resp, err := o.HandlePostTool(context.Background(), agents.Request{
		Raw:   json.RawMessage(raw),
		Event: agents.EventPostTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(resp.Body, &got); err != nil || got["output"] == "" {
		t.Fatalf("post-tool replacement %s: %v", resp.Body, err)
	}
}

func TestOpenCodeInstallIdempotentAndUninstallRestores(t *testing.T) {
	dir := t.TempDir()
	pluginsDir := filepath.Join(dir, ".opencode", "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ambient := filepath.Join(pluginsDir, "other-plugin.ts")
	if err := os.WriteFile(ambient, []byte("export const Other = async () => ({});\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	o := agents.NewOpenCode()
	opts := agents.InstallOptions{
		WorkDir: dir,
		Scope:   "project",
		Binary:  "/opt/jevkit",
	}
	if err := o.Install(opts); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := o.Install(opts); err != nil {
		t.Fatalf("second install: %v", err)
	}

	pluginPath := filepath.Join(pluginsDir, agents.OpenCodePluginFile)
	data, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), agents.OpenCodePluginMarker) {
		t.Fatalf("missing marker: %s", truncate(data, 200))
	}
	if !strings.Contains(string(data), `"/opt/jevkit"`) {
		t.Fatalf("binary not baked in: %s", truncate(data, 400))
	}
	if strings.Count(string(data), agents.OpenCodePluginMarker) != 1 {
		t.Fatalf("marker duplicated")
	}
	if _, err := os.Stat(ambient); err != nil {
		t.Fatalf("ambient plugin must be preserved: %v", err)
	}

	if err := o.Uninstall(opts); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Stat(pluginPath); !os.IsNotExist(err) {
		t.Fatalf("managed plugin should be removed: %v", err)
	}
	if _, err := os.Stat(ambient); err != nil {
		t.Fatalf("ambient plugin lost on uninstall: %v", err)
	}
	if _, err := os.Stat(pluginPath + ".jevkit-original"); !os.IsNotExist(err) {
		t.Fatalf("backup should be removed: %v", err)
	}
}

func TestOpenCodeInstallDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	o := agents.NewOpenCode()
	opts := agents.InstallOptions{
		WorkDir: dir,
		Scope:   "project",
		Binary:  "jevkit",
		DryRun:  true,
	}
	if err := o.Install(opts); err != nil {
		t.Fatal(err)
	}
	pluginPath := filepath.Join(dir, ".opencode", "plugins", agents.OpenCodePluginFile)
	if _, err := os.Stat(pluginPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run must not create plugin: %v", err)
	}
}

func TestOpenCodeInstallUserScopeAndRestorePriorFile(t *testing.T) {
	home := t.TempDir()
	pluginsDir := filepath.Join(home, ".opencode", "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pluginPath := filepath.Join(pluginsDir, agents.OpenCodePluginFile)
	original := []byte("// prior ambient file\nexport const Prior = async () => ({});\n")
	if err := os.WriteFile(pluginPath, original, 0o644); err != nil {
		t.Fatal(err)
	}

	o := agents.NewOpenCode()
	opts := agents.InstallOptions{
		ConfigDir: home,
		Scope:     "user",
		Binary:    "jevkit",
	}
	if err := o.Install(opts); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), agents.OpenCodePluginMarker) {
		t.Fatalf("expected managed plugin: %s", truncate(data, 200))
	}
	if err := o.Uninstall(opts); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored, original) {
		t.Fatalf("uninstall did not restore prior bytes\nwant:\n%s\ngot:\n%s", original, restored)
	}
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n])
}
