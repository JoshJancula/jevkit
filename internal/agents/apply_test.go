package agents_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OWNER/jevkit/internal/agents"
)

// fileFake is a controllable adapter that installs a marker file with the
// same backup / dry-run / restore semantics the real adapters use.
type fileFake struct {
	name string
	path string // relative to WorkDir (project) or ConfigDir (user)
}

func (f *fileFake) Name() string                      { return f.name }
func (f *fileFake) Capabilities() agents.Capabilities { return agents.Capabilities{} }
func (f *fileFake) Passthrough(agents.Event) []byte   { return []byte(`{}`) }
func (f *fileFake) HandlePreTool(context.Context, agents.Request) (agents.Response, error) {
	return agents.Response{Body: []byte(`{}`)}, nil
}
func (f *fileFake) HandlePostTool(context.Context, agents.Request) (agents.Response, error) {
	return agents.Response{Body: []byte(`{}`)}, nil
}
func (f *fileFake) HandleStop(context.Context, agents.Request) (agents.Response, error) {
	return agents.Response{Body: []byte(`{}`)}, nil
}

const fakeMarker = "JEVKIT_FAKE_INSTALL"

func (f *fileFake) target(opts agents.InstallOptions) (string, error) {
	scope := agents.ResolveScope(opts)
	switch scope {
	case "project":
		if opts.WorkDir == "" {
			return "", errors.New("project requires WorkDir")
		}
		return filepath.Join(opts.WorkDir, f.path), nil
	case "user":
		if opts.ConfigDir == "" {
			return "", errors.New("user requires ConfigDir")
		}
		return filepath.Join(opts.ConfigDir, f.path), nil
	default:
		return "", errors.New("bad scope")
	}
}

func (f *fileFake) Install(opts agents.InstallOptions) error {
	path, err := f.target(opts)
	if err != nil {
		return err
	}
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if errors.Is(err, os.ErrNotExist) {
		existing = nil
	}
	out := []byte("{\n  \"marker\": \"" + fakeMarker + "\",\n  \"binary\": \"" + opts.Binary + "\"\n}\n")
	if opts.Preview != nil {
		opts.Preview(agents.FilePreview{Path: path, Before: existing, After: out})
	}
	if opts.DryRun {
		return nil
	}
	if bytes.Equal(existing, out) {
		return nil
	}
	bak := path + ".jevkit-original"
	if _, err := os.Stat(bak); errors.Is(err, os.ErrNotExist) {
		body := existing
		if existing == nil {
			body = []byte("JEVKIT_ABSENT\n")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(bak, body, 0o644); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

func (f *fileFake) Uninstall(opts agents.InstallOptions) error {
	path, err := f.target(opts)
	if err != nil {
		return err
	}
	existing, _ := os.ReadFile(path)
	bak := path + ".jevkit-original"
	var after []byte
	data, err := os.ReadFile(bak)
	switch {
	case err == nil && string(data) == "JEVKIT_ABSENT\n":
		after = nil
	case err == nil:
		after = data
	case errors.Is(err, os.ErrNotExist):
		after = nil
	default:
		return err
	}
	if opts.Preview != nil {
		opts.Preview(agents.FilePreview{Path: path, Before: existing, After: after})
	}
	if opts.DryRun {
		return nil
	}
	if after == nil {
		_ = os.Remove(path)
		_ = os.Remove(bak)
		return nil
	}
	if err := os.WriteFile(path, after, 0o644); err != nil {
		return err
	}
	return os.Remove(bak)
}

func TestSelectAgentsDetection(t *testing.T) {
	agents.Register(&fileFake{name: "z-detect-fake", path: ".fake/config.json"})
	look := func(name string) (string, error) {
		if name == "claude" {
			return "/bin/claude", nil
		}
		return "", errors.New("missing")
	}
	got, err := agents.SelectAgents("all", look, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name() != "claude" {
		t.Fatalf("detected = %v", namesOf(got))
	}
	one, err := agents.SelectAgents("claude", look, true)
	if err != nil || len(one) != 1 {
		t.Fatalf("explicit: %v %v", one, err)
	}
}

func namesOf(list []agents.Agent) []string {
	out := make([]string, len(list))
	for i, a := range list {
		out[i] = a.Name()
	}
	return out
}

func TestInstallAgentIdempotentDryRunBackupUninstall(t *testing.T) {
	// Use a real adapter so HookConfigPath / MCPConfigPath / StateOf work.
	// Fake covers SelectAgents; Claude covers the full InstallAgent path.
	dir := t.TempDir()
	home := t.TempDir()
	original := []byte("{\n  \"permissions\": {\n    \"allow\": [\"Bash\"]\n  }\n}\n")
	settingsDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(settingsDir, "settings.json")
	if err := os.WriteFile(settingsPath, original, 0o644); err != nil {
		t.Fatal(err)
	}
	mcpOriginal := []byte("{\n  \"mcpServers\": {\n    \"other\": {\"command\": \"o\"}\n  }\n}\n")
	mcpPath := filepath.Join(dir, ".mcp.json")
	if err := os.WriteFile(mcpPath, mcpOriginal, 0o644); err != nil {
		t.Fatal(err)
	}

	a := agents.NewClaude()
	opts := agents.InstallOptions{
		WorkDir: dir,
		Scope:   "project",
		Binary:  "/opt/jevkit",
	}

	dry := opts
	dry.DryRun = true
	rep, err := agents.InstallAgent(a, dry)
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	diffs := agents.FormatPreviews(rep.Previews)
	if diffs == "" {
		t.Fatal("dry-run should preview diffs")
	}
	if _, err := os.Stat(settingsPath + ".jevkit-original"); !os.IsNotExist(err) {
		t.Fatal("dry-run must not create hook backup")
	}
	if _, err := os.Stat(mcpPath + ".jevkit-original"); !os.IsNotExist(err) {
		t.Fatal("dry-run must not create mcp backup")
	}
	gotSettings, _ := os.ReadFile(settingsPath)
	if !bytes.Equal(gotSettings, original) {
		t.Fatal("dry-run mutated settings")
	}
	gotMCP, _ := os.ReadFile(mcpPath)
	if !bytes.Equal(gotMCP, mcpOriginal) {
		t.Fatal("dry-run mutated mcp")
	}

	if _, err := agents.InstallAgent(a, opts); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := agents.InstallAgent(a, opts); err != nil {
		t.Fatalf("second install: %v", err)
	}
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), agents.ClaudeHookMarker) != 1 {
		t.Fatalf("hooks not idempotent: %s", data)
	}
	if _, err := os.Stat(settingsPath + ".jevkit-original"); err != nil {
		t.Fatalf("hooks backup missing: %v", err)
	}
	if _, err := os.Stat(mcpPath + ".jevkit-original"); err != nil {
		t.Fatalf("mcp backup missing: %v", err)
	}
	mcpData, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mcpData), `"jevkit"`) || !strings.Contains(string(mcpData), `"other"`) {
		t.Fatalf("mcp merge lost peers or jevkit: %s", mcpData)
	}

	st := agents.StateOf(agents.ClaudeName, dir, home, func(string) (string, error) {
		return "/bin/claude", nil
	})
	if !st.Detected || st.Hooks != "project" || st.MCP != "project" {
		t.Fatalf("state = %+v", st)
	}

	if _, err := agents.UninstallAgent(a, opts); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	restored, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored, original) {
		t.Fatalf("hooks restore\nwant:\n%s\ngot:\n%s", original, restored)
	}
	restoredMCP, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restoredMCP, mcpOriginal) {
		t.Fatalf("mcp restore\nwant:\n%s\ngot:\n%s", mcpOriginal, restoredMCP)
	}
	st = agents.StateOf(agents.ClaudeName, dir, home, nil)
	if st.Hooks != "not installed" || st.MCP != "not installed" {
		t.Fatalf("after uninstall state = %+v", st)
	}
}

func TestFakeAdapterInstallUninstallByteExact(t *testing.T) {
	f := &fileFake{name: "fake-byte", path: ".fake/config.json"}
	agents.Register(f)
	dir := t.TempDir()
	original := []byte("ambient\n")
	path := filepath.Join(dir, f.path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	opts := agents.InstallOptions{WorkDir: dir, Scope: "project", Binary: "jevkit"}
	if err := f.Install(opts); err != nil {
		t.Fatal(err)
	}
	if err := f.Install(opts); err != nil {
		t.Fatal(err)
	}
	if err := f.Uninstall(opts); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("want %q got %q", original, got)
	}
}

func TestInstallComponentsAreSelectiveAndReversible(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, ".claude", "settings.json")
	mcp := filepath.Join(dir, ".mcp.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	originalHooks := []byte("{\"permissions\":{}}\n")
	originalMCP := []byte("{\"mcpServers\":{}}\n")
	if err := os.WriteFile(settings, originalHooks, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcp, originalMCP, 0o644); err != nil {
		t.Fatal(err)
	}
	opts := agents.InstallOptions{WorkDir: dir, Scope: "project", Binary: "jevkit"}
	a := agents.NewClaude()
	if _, err := agents.InstallAgentComponents(a, opts, agents.Components{MCP: true}); err != nil {
		t.Fatal(err)
	}
	gotHooks, _ := os.ReadFile(settings)
	if !bytes.Equal(gotHooks, originalHooks) {
		t.Fatalf("MCP-only changed hooks: %s", gotHooks)
	}
	if _, err := agents.UninstallAgentComponents(a, opts, agents.Components{MCP: true}); err != nil {
		t.Fatal(err)
	}
	gotMCP, _ := os.ReadFile(mcp)
	if !bytes.Equal(gotMCP, originalMCP) {
		t.Fatalf("MCP-only uninstall did not restore bytes: %s", gotMCP)
	}
	if _, err := agents.InstallAgentComponents(a, opts, agents.Components{Hooks: true}); err != nil {
		t.Fatal(err)
	}
	gotMCP, _ = os.ReadFile(mcp)
	if !bytes.Equal(gotMCP, originalMCP) {
		t.Fatalf("hooks-only changed MCP: %s", gotMCP)
	}
	if _, err := agents.UninstallAgentComponents(a, opts, agents.Components{Hooks: true}); err != nil {
		t.Fatal(err)
	}
	gotHooks, _ = os.ReadFile(settings)
	if !bytes.Equal(gotHooks, originalHooks) {
		t.Fatalf("hooks-only uninstall did not restore bytes: %s", gotHooks)
	}
}
