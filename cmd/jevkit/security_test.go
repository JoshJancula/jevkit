package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OWNER/jevkit/internal/sdlc/worker"
	securityconfig "github.com/OWNER/jevkit/internal/security/config"
	"github.com/OWNER/jevkit/internal/usage"
)

func TestSecurityCLIAndInvalidPolicyHookFallback(t *testing.T) {
	a := newApp(t)
	out, _ := mustRun(t, a, "", exitOK, "security", "list")
	if !strings.Contains(out, "* builtin (default)") {
		t.Fatal(out)
	}
	out, _ = mustRun(t, a, "", exitOK, "security", "check", "rm -rf /")
	if !strings.Contains(out, "deny: killswitch") {
		t.Fatal(out)
	}
	out, _ = mustRun(t, a, "", exitOK, "security", "check", "echo hi")
	if strings.TrimSpace(out) != "allow" {
		t.Fatal(out)
	}

	projectPath := filepath.Join(a.WorkDir, ".jevkit", "security.yaml")
	writeFile(t, projectPath, "version: 1\nmode: shadow\n")
	if code, _, _ := run(a, "", "security", "check", "echo hi"); code == exitOK {
		t.Fatal("invalid project policy was accepted by CLI")
	}
	payload := `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"rm -rf /"},"cwd":"/tmp/project"}`
	code, body, errText := run(a, payload, "_runtime", "dispatch", "--protocol", "1", "codex", "pre-tool")
	if code != exitOK || !strings.Contains(body, `"permissionDecision":"deny"`) {
		t.Fatalf("builtin fallback did not deny: code=%d body=%s stderr=%s", code, body, errText)
	}
	record, err := os.ReadFile(usage.HookPath(a.stateHome()))
	if err != nil || !strings.Contains(string(record), "loadError") {
		t.Fatalf("policy load error missing from hook telemetry: %s %v", record, err)
	}
}

func TestSDLCSecurityAllowlistComesFromInputFlags(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	outside := t.TempDir()
	taskPath := filepath.Join(outside, "task.md")
	referencePath := filepath.Join(outside, "reference.md")
	writeFile(t, taskPath, "Read "+taskPath+" and implement the feature.\n")
	writeFile(t, referencePath, "# Reference\n")
	executor := &fakeSDLCExecutor{replies: []worker.Reply{
		{Outcome: "planned", Content: "Plan."},
		{Outcome: "changed", Content: "diff --git a/a b/a\n+new\n"},
		{Outcome: "approved"},
	}}
	a.SdlcExecutor = executor
	code, _, errs := run(a, "", "sdlc", "run", "feature", "--task-file", taskPath, "--file", referencePath+"=reference.md", "--auto")
	if code != exitOK || len(executor.requests) == 0 {
		t.Fatalf("SDLC run: code=%d requests=%d err=%q", code, len(executor.requests), errs)
	}
	req := executor.requests[0]
	if len(req.AllowRead) != 2 || req.AllowRead[0] != taskPath || req.AllowRead[1] != referencePath {
		t.Fatalf("per-run allowlist = %v", req.AllowRead)
	}
	guard := securityconfig.Guard{Workspace: req.Workspace, AllowRead: req.AllowRead}
	if violation, ok := guard.Check(req.Task); !ok {
		t.Fatalf("task-file path was not allowed: %s", violation)
	}
}

func TestSecurityPolicyFlagSelectsShadowMode(t *testing.T) {
	a := newApp(t)
	writeFile(t, filepath.Join(a.ConfigDir, "security", "local.yaml"), "version: 1\nmode: shadow\n")
	code, out, errs := run(a, "", "--security-policy", "local", "security", "check", "rm -rf /")
	if code != exitOK || strings.TrimSpace(out) != "allow" {
		t.Fatalf("security-policy selection: code=%d out=%q err=%q", code, out, errs)
	}
	if _, err := os.Stat(filepath.Join(a.StateDir, "jevkit", "security-shadow.jsonl")); err != nil {
		t.Fatal("shadow decision was not recorded:", err)
	}
}
