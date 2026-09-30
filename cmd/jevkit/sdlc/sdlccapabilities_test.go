package sdlc

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/sdlc/enrollment"
)

func TestSdlcAgentsCapabilitiesReportsProbedVersionAndFailsClosedWhenMissing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture is a POSIX shell script")
	}
	a := newApp(t)
	dir := t.TempDir()
	fakeCodex := filepath.Join(dir, "codex")
	if err := os.WriteFile(fakeCodex, []byte("#!/bin/sh\necho 'codex-cli 1.2.3'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	a.LookPath = func(name string) (string, error) {
		if name == "codex" {
			return fakeCodex, nil
		}
		return "", errors.New("not found")
	}
	code, out, errs := run(a, "", "sdlc", "agents", "capabilities", "--details")
	if code != app.ExitOK {
		t.Fatalf("capabilities: %d %q %q", code, out, errs)
	}
	if !strings.Contains(out, "codex-cli 1.2.3") {
		t.Fatalf("expected probed codex version in output: %q", out)
	}
	if !strings.Contains(out, "cursor-agent not found on PATH") {
		t.Fatalf("expected fail-closed CLI reach for an uninstalled runtime: %q", out)
	}
	if !strings.Contains(out, "OPENCODE") || !strings.Contains(out, "Shell hook coverage: false") {
		t.Fatalf("expected OpenCode's missing shell hook coverage called out: %q", out)
	}
	if !strings.Contains(out, "Permission-bypass argument: --dangerously-skip-permissions") {
		t.Fatalf("expected Antigravity's permission-bypass argument named explicitly: %q", out)
	}
	if !strings.Contains(out, "Read-only execution: not enforced") {
		t.Fatalf("expected OpenCode's unenforceable read-only execution to be reported: %q", out)
	}
}

func TestCapabilityShortcutsShowValidPerAgentConfiguration(t *testing.T) {
	for _, args := range [][]string{
		{"sdlc", "agents", "capabilities"},
		{"sdlc", "agents", "caps"},
		{"sdlc", "agents", "c"},
	} {
		t.Run(strings.Join(args, "/"), func(t *testing.T) {
			a := newApp(t)
			a.LookPath = func(string) (string, error) { return "", errors.New("not installed") }
			code, out, errs := run(a, "", args...)
			if code != app.ExitOK || errs != "" {
				t.Fatalf("shortcut: %d %s %s", code, out, errs)
			}
			for _, want := range []string{sdlcFileURL(a.sdlcRosterPath()), "TOOLS", "CONFIGURE ONE AGENT", "web: false", "delegate: false", "tools: auto", "agent: security-reviewer", "runtimeArgs:", "true or an omitted field", "mappings are rejected"} {
				if !strings.Contains(out, want) {
					t.Fatalf("missing configuration guidance %q: %s", want, out)
				}
			}
			if strings.Contains(out, "Shell hook coverage:") || strings.Contains(out, "Cancellation / child cleanup:") {
				t.Fatal("detailed diagnostics cluttered the default view")
			}
			if _, err := os.Stat(a.sdlcRosterPath()); !os.IsNotExist(err) {
				t.Fatalf("read-only configuration guide created a roster: %v", err)
			}
		})
	}
	for _, runtime := range []string{"codex", "claude", "cursor", "opencode", "antigravity"} {
		t.Run("example/"+runtime, func(t *testing.T) {
			a := newApp(t)
			var probed []string
			a.LookPath = func(name string) (string, error) {
				probed = append(probed, name)
				return "", errors.New("not installed")
			}
			code, out, errs := run(a, "", "sdlc", "agents", "c", "-r", runtime)
			if code != app.ExitOK || len(probed) != 1 || probed[0] != sdlcRuntimeBinaries[runtime] {
				t.Fatalf("runtime filter: %d %s %s probes=%v", code, out, errs, probed)
			}
			if strings.Count(out, "│ missing") > 1 {
				t.Fatal("runtime filter included unrelated rows")
			}
			_, example, ok := strings.Cut(out, "  - id: focused-builder\n")
			if !ok {
				t.Fatal("missing copyable roster example")
			}
			example, _, _ = strings.Cut(example, "\n\n")
			path := filepath.Join(t.TempDir(), "example.yaml")
			if err := os.WriteFile(path, []byte("version: 1\nagents:\n  - id: focused-builder\n"+example+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			roster, err := enrollment.LoadRoster(path)
			if err != nil {
				t.Fatalf("displayed YAML does not load: %v\n%s", err, example)
			}
			agent := roster.Agents[0]
			if agent.Runtime != runtime || enrollment.ToolPolicySupported(runtime) == agent.Tools.Auto {
				t.Fatalf("example does not match runtime support: %+v", agent)
			}
		})
	}
}

func TestCapabilityCommandsRejectInvalidOptions(t *testing.T) {
	for _, args := range [][]string{
		{"sdlc", "-c", "-i"},
		{"sdlc", "-c", "--runtime", "unknown"},
		{"sdlc", "agents", "c", "-r", "unknown"},
		{"sdlc", "--details"},
		{"sdlc", "agents", "--details"},
		{"sdlc", "--hooks", "on"},
		{"sdlc", "-c", "--mcp", "on"},
		{"sdlc", "-i", "--runtime", "claude"},
	} {
		a := newApp(t)
		code, out, errs := run(a, "", args...)
		if code != app.ExitUsage || errs == "" {
			t.Fatalf("invalid shortcut accepted %v: %d %s %s", args, code, out, errs)
		}
		if _, configured, err := a.loadSDLCRuntimeIntegration(); err != nil || configured {
			t.Fatalf("invalid options changed integration defaults: %t %v", configured, err)
		}
	}
}
