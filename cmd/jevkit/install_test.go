package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallUninstallClaudeRoundTrip(t *testing.T) {
	a, _, _ := cliApp(t)
	a.LookPath = func(name string) (string, error) {
		if name == "claude" {
			return "/opt/bin/claude", nil
		}
		return "", errors.New("not found")
	}
	a.Binary = "/opt/jevkit"

	settingsDir := filepath.Join(a.WorkDir, ".claude")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(settingsDir, "settings.json")
	original := []byte("{\n  \"permissions\": {\n    \"allow\": [\"Bash\"]\n  }\n}\n")
	if err := os.WriteFile(settingsPath, original, 0o644); err != nil {
		t.Fatal(err)
	}
	mcpPath := filepath.Join(a.WorkDir, ".mcp.json")
	mcpOriginal := []byte("{\n  \"mcpServers\": {\n    \"other\": {\"command\": \"o\"}\n  }\n}\n")
	if err := os.WriteFile(mcpPath, mcpOriginal, 0o644); err != nil {
		t.Fatal(err)
	}

	code, out, errs := run(a, "", "install", "claude", "--dry-run")
	if code != exitOK {
		t.Fatalf("dry-run exit %d: %s%s", code, out, errs)
	}
	if !strings.Contains(out, "dry-run") || !strings.Contains(out, "---") {
		t.Fatalf("expected diff preview:\n%s", out)
	}
	if _, err := os.Stat(settingsPath + ".jevkit-original"); !os.IsNotExist(err) {
		t.Fatal("dry-run wrote hooks backup")
	}
	got, _ := os.ReadFile(settingsPath)
	if string(got) != string(original) {
		t.Fatal("dry-run mutated settings")
	}

	code, out, errs = run(a, "", "install", "claude")
	if code != exitOK {
		t.Fatalf("install exit %d: %s%s", code, out, errs)
	}
	code, out, errs = run(a, "", "install", "claude")
	if code != exitOK {
		t.Fatalf("reinstall exit %d: %s%s", code, out, errs)
	}
	if !strings.Contains(out, "already up to date") {
		t.Fatalf("expected idempotent message:\n%s", out)
	}
	if _, err := os.Stat(settingsPath + ".jevkit-original"); err != nil {
		t.Fatalf("backup: %v", err)
	}
	if _, err := os.Stat(mcpPath + ".jevkit-original"); err != nil {
		t.Fatalf("mcp backup: %v", err)
	}

	code, out, errs = run(a, "", "doctor")
	if code != exitOK {
		t.Fatalf("doctor exit %d: %s%s", code, out, errs)
	}
	if !strings.Contains(out, "claude:        /opt/bin/claude") {
		t.Fatalf("doctor detect:\n%s", out)
	}
	if !strings.Contains(out, "hooks:       project") || !strings.Contains(out, "mcp:         project") {
		t.Fatalf("doctor install state:\n%s", out)
	}

	code, out, errs = run(a, "", "uninstall", "claude")
	if code != exitOK {
		t.Fatalf("uninstall exit %d: %s%s", code, out, errs)
	}
	restored, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(original) {
		t.Fatalf("hooks restore failed\nwant:\n%s\ngot:\n%s", original, restored)
	}
	restoredMCP, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(restoredMCP) != string(mcpOriginal) {
		t.Fatalf("mcp restore failed\nwant:\n%s\ngot:\n%s", mcpOriginal, restoredMCP)
	}
}

func TestInstallAllUsesDetection(t *testing.T) {
	a, _, _ := cliApp(t)
	a.LookPath = func(name string) (string, error) {
		return "", errors.New("none")
	}
	code, _, errs := run(a, "", "install", "all")
	if code != exitFail {
		t.Fatalf("exit %d, want fail; errs=%s", code, errs)
	}
	if !strings.Contains(errs, "no agents detected") {
		t.Fatalf("errs=%q", errs)
	}
}

func TestInstallUnknownAgent(t *testing.T) {
	a, _, _ := cliApp(t)
	code, _, errs := run(a, "", "install", "nope")
	if code != exitFail || !strings.Contains(errs, "unknown agent") {
		t.Fatalf("exit %d errs %q", code, errs)
	}
}
