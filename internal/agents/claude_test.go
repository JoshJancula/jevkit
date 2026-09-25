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
	"github.com/OWNER/jevkit/internal/jev"
	"github.com/OWNER/jevkit/internal/registry"
)

func TestClaudeLookupRegistered(t *testing.T) {
	got := agents.Lookup(agents.ClaudeName)
	if got == nil || got.Name() != agents.ClaudeName {
		t.Fatalf("claude not registered: %+v", got)
	}
	caps := got.Capabilities()
	if !caps.PostTool || !caps.OutputReplace || !caps.PreTool {
		t.Fatalf("unexpected caps: %+v", caps)
	}
}

func TestClaudePostToolFixturePassthroughBelowThreshold(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "hooks", "claude", "posttooluse-bash.json"))
	if err != nil {
		t.Fatal(err)
	}
	c := &agents.Claude{
		Getenv:         envMap{"JEVKIT_COMPACT": "1"}.Getenv,
		ThresholdBytes: 8192,
	}
	resp, err := c.HandlePostTool(context.Background(), agents.Request{
		Raw:   json.RawMessage(raw),
		Event: agents.EventPostTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(bytes.TrimSpace(resp.Body)) != "{}" {
		t.Fatalf("below threshold must passthrough, got %s", resp.Body)
	}
}

func TestClaudePostToolCompactedAboveThreshold(t *testing.T) {
	raw := mustClaudeFixture(t, "posttooluse-bash.json")
	payload := mutateClaudeStdout(t, raw, "custom-build-tool --verbose", largeCompactableOutput(80))

	c := &agents.Claude{
		Getenv:         envMap{"JEVKIT_COMPACT": "1"}.Getenv,
		ThresholdBytes: 200,
	}
	resp, err := c.HandlePostTool(context.Background(), agents.Request{
		Raw:   payload,
		Event: agents.EventPostTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			UpdatedToolOutput struct {
				Stdout string `json:"stdout"`
				Stderr string `json:"stderr"`
			} `json:"updatedToolOutput"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		t.Fatalf("body %s: %v", resp.Body, err)
	}
	if string(resp.Body) != "{}" {
		t.Fatalf("missing Jev classifier must preserve output, body=%s", resp.Body)
	}
}

func TestClaudePostToolShadowPassthrough(t *testing.T) {
	raw := mustClaudeFixture(t, "posttooluse-bash.json")
	payload := mutateClaudeStdout(t, raw, "custom-build-tool --verbose", largeCompactableOutput(80))

	c := &agents.Claude{
		Getenv: envMap{
			"JEVKIT_COMPACT":        "1",
			"JEVKIT_COMPACT_SHADOW": "1",
		}.Getenv,
		ThresholdBytes: 200,
	}
	resp, err := c.HandlePostTool(context.Background(), agents.Request{
		Raw:   payload,
		Event: agents.EventPostTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(bytes.TrimSpace(resp.Body)) != "{}" {
		t.Fatalf("shadow must passthrough, got %s", resp.Body)
	}
}

func TestClaudeShadowRunsAndRecordsDecision(t *testing.T) {
	output := largeCompactableOutput(160)
	payload := mutateClaudeStdout(t, mustClaudeFixture(t, "posttooluse-bash.json"), "custom-build-tool --verbose", output)
	dir := t.TempDir()
	response := &jev.Response{Answers: map[string]jev.Answer{
		"evidence_locus":       jev.ChoiceAnswer{Choice: "tail", Confidence: 0.99, Probabilities: map[string]float64{"tail": 0.99}},
		"outcome":              jev.ChoiceAnswer{Choice: "failure", Confidence: 0.99, Probabilities: map[string]float64{"failure": 0.99}},
		"content_kind":         jev.ChoiceAnswer{Choice: "build-compile", Confidence: 0.99, Probabilities: map[string]float64{"build-compile": 0.99}},
		"retention_budget":     jev.ScoreAnswer{Score: 0.2, Confidence: 0.99},
		"tail_explains":        jev.NoulAnswer{Noul: 0.99},
		"middle_omission_safe": jev.NoulAnswer{Noul: 0.99},
	}}
	c := &agents.Claude{Getenv: envMap{"JEVKIT_COMPACT": "1", "JEVKIT_COMPACT_SHADOW": "1"}.Getenv, ThresholdBytes: 200, StateDir: dir, Asker: codexAsker{response: response}}
	result, err := c.HandlePostTool(context.Background(), agents.Request{Raw: payload, Event: agents.EventPostTool})
	if err != nil || string(result.Body) != "{}" {
		t.Fatalf("shadow output changed: %s %v", result.Body, err)
	}
	b, err := os.ReadFile(registry.DecisionsPath(dir))
	if err != nil || !bytes.Contains(b, []byte(`"shadow":true`)) || !bytes.Contains(b, []byte(`"probabilities"`)) {
		t.Fatalf("missing shadow decision: %s %v", b, err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "tool-results", "claude", "*.log"))
	if err != nil || len(files) != 1 {
		t.Fatalf("raw files=%v %v", files, err)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil || string(raw) != output {
		t.Fatal("raw original changed")
	}
}

func TestClaudePostToolDisabledPassthrough(t *testing.T) {
	raw := mustClaudeFixture(t, "posttooluse-bash.json")
	payload := mutateClaudeStdout(t, raw, "custom-build-tool --verbose", largeCompactableOutput(80))

	c := &agents.Claude{
		Getenv:         envMap{}.Getenv,
		ThresholdBytes: 200,
	}
	resp, err := c.HandlePostTool(context.Background(), agents.Request{
		Raw:   payload,
		Event: agents.EventPostTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(bytes.TrimSpace(resp.Body)) != "{}" {
		t.Fatalf("disabled must passthrough, got %s", resp.Body)
	}
}

func TestClaudePostToolFailureFixturePassthrough(t *testing.T) {
	raw := mustClaudeFixture(t, "posttoolusefailure-bash.json")
	c := &agents.Claude{
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
		t.Fatalf("PostToolUseFailure must passthrough (no exit code / no tool_response), got %s", resp.Body)
	}
}

func TestClaudeInstallIdempotentAndUninstallRestoresBytes(t *testing.T) {
	dir := t.TempDir()
	settingsDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(settingsDir, "settings.json")
	original := []byte("{\n  \"permissions\": {\n    \"allow\": [\"Bash\"]\n  }\n}\n")
	if err := os.WriteFile(settingsPath, original, 0o644); err != nil {
		t.Fatal(err)
	}

	c := agents.NewClaude()
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

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	count := strings.Count(string(data), agents.ClaudeHookMarker)
	if count != 1 {
		t.Fatalf("expected one managed entry, found %d in:\n%s", count, data)
	}
	if !strings.Contains(string(data), "/opt/jevkit "+agents.ClaudeHookMarker) {
		t.Fatalf("missing command: %s", data)
	}
	if strings.Count(string(data), agents.ClaudePreHookMarker) != 1 || !strings.Contains(string(data), "\"PreToolUse\"") {
		t.Fatalf("missing single PreToolUse Bash hook: %s", data)
	}
	if !strings.Contains(string(data), `"permissions"`) {
		t.Fatalf("lost unrelated settings: %s", data)
	}

	if err := c.Uninstall(opts); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	restored, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored, original) {
		t.Fatalf("uninstall did not restore original bytes\nwant:\n%s\ngot:\n%s", original, restored)
	}
	if _, err := os.Stat(settingsPath + ".jevkit-original"); !os.IsNotExist(err) {
		t.Fatalf("backup should be removed after uninstall: %v", err)
	}
}

func TestClaudeInstallDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	c := agents.NewClaude()
	opts := agents.InstallOptions{
		WorkDir: dir,
		Scope:   "project",
		Binary:  "jevkit",
		DryRun:  true,
	}
	if err := c.Install(opts); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(dir, ".claude", "settings.json")
	if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run must not create settings: %v", err)
	}
}

func TestClaudeInstallUserScopeAndAbsentRestore(t *testing.T) {
	home := t.TempDir()
	c := agents.NewClaude()
	opts := agents.InstallOptions{
		ConfigDir: home,
		Scope:     "user",
		Binary:    "jevkit",
	}
	if err := c.Install(opts); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if _, err := os.Stat(settingsPath); err != nil {
		t.Fatal(err)
	}
	if err := c.Install(opts); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), agents.ClaudeHookMarker) != 1 {
		t.Fatalf("not idempotent: %s", data)
	}
	if err := c.Uninstall(opts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
		t.Fatalf("absent original should remove settings on uninstall: %v", err)
	}
}

type envMap map[string]string

func (e envMap) Getenv(k string) string { return e[k] }

func mustClaudeFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "hooks", "claude", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mutateClaudeStdout(t *testing.T, raw []byte, command, stdout string) json.RawMessage {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["tool_input"] = map[string]any{"command": command}
	tr, _ := m["tool_response"].(map[string]any)
	if tr == nil {
		tr = map[string]any{}
	}
	tr["stdout"] = stdout
	tr["stderr"] = ""
	m["tool_response"] = tr
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func largeCompactableOutput(lines int) string {
	var b strings.Builder
	for i := 1; i <= lines; i++ {
		b.WriteString("Downloading package ")
		b.WriteString(strings.Repeat("x", 40))
		b.WriteString(" seq=")
		b.WriteByte(byte('0' + i%10))
		b.WriteByte('\n')
	}
	b.WriteString("ERROR: module xyz failed to compile\n")
	b.WriteString("FINAL SUMMARY: build complete with warnings\n")
	return b.String()
}
