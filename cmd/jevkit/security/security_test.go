package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/cmd/jevkit/internal/testkit"
	"github.com/JoshJancula/jevkit/internal/usage"
)

func TestSecurityCLIAndInvalidPolicyHookFallback(t *testing.T) {
	a := newApp(t)
	out, _ := mustRun(t, a, "", app.ExitOK, "security", "list")
	if !strings.Contains(out, "* builtin (default)") {
		t.Fatal(out)
	}
	out, _ = mustRun(t, a, "", app.ExitOK, "security", "check", "rm -rf /")
	if !strings.Contains(out, "deny: killswitch") {
		t.Fatal(out)
	}
	out, _ = mustRun(t, a, "", app.ExitOK, "security", "check", "echo hi")
	if strings.TrimSpace(out) != "allow" {
		t.Fatal(out)
	}

	projectPath := filepath.Join(a.WorkDir, ".jevkit", "security.yaml")
	testkit.WriteFile(t, projectPath, "version: 1\nmode: shadow\n")
	if code, _, _ := run(a, "", "security", "check", "echo hi"); code == app.ExitOK {
		t.Fatal("invalid project policy was accepted by CLI")
	}
	payload := `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"rm -rf /"},"cwd":"/tmp/project"}`
	code, body, errText := run(a, payload, "_runtime", "dispatch", "--protocol", "1", "codex", "pre-tool")
	if code != app.ExitOK || !strings.Contains(body, `"permissionDecision":"deny"`) {
		t.Fatalf("builtin fallback did not deny: code=%d body=%s stderr=%s", code, body, errText)
	}
	record, err := os.ReadFile(usage.HookPath(a.StateHome()))
	if err != nil || !strings.Contains(string(record), "loadError") {
		t.Fatalf("policy load error missing from hook telemetry: %s %v", record, err)
	}
}

func TestSecurityPolicyFlagSelectsShadowMode(t *testing.T) {
	a := newApp(t)
	testkit.WriteFile(t, filepath.Join(a.ConfigDir, "security", "local.yaml"), "version: 1\nmode: shadow\n")
	code, out, errs := run(a, "", "--security-policy", "local", "security", "check", "rm -rf /")
	if code != app.ExitOK || strings.TrimSpace(out) != "allow" {
		t.Fatalf("security-policy selection: code=%d out=%q err=%q", code, out, errs)
	}
	if _, err := os.Stat(filepath.Join(a.StateDir, "jevkit", "security-shadow.jsonl")); err != nil {
		t.Fatal("shadow decision was not recorded:", err)
	}
}
