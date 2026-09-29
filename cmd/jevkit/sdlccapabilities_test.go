package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
	code, out, errs := run(a, "", "sdlc", "agents", "capabilities")
	if code != exitOK {
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
