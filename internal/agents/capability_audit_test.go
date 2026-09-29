package agents_test

import (
	"os"
	"strings"
	"testing"

	"github.com/JoshJancula/jevkit/internal/agents"
)

func TestRuntimeCapabilityAuditCoversEveryAdapter(t *testing.T) {
	want := map[string]agents.ReplacementScope{
		agents.ClaudeName:   agents.ReplacementToolShapes,
		agents.CodexName:    agents.ReplacementToolShapes,
		agents.OpenCodeName: agents.ReplacementToolShapes,
	}
	for name, replacement := range want {
		profile, ok := agents.RuntimeCapabilities(name)
		if !ok {
			t.Fatalf("%s has no audited capability profile", name)
		}
		if profile.Replacement != replacement || profile.Evidence == "" || profile.Installation == "" {
			t.Fatalf("%s profile = %+v", name, profile)
		}
		if !profile.Telemetry {
			t.Fatalf("%s must support telemetry", name)
		}
		if name == agents.ClaudeName && (!profile.PreToolDecision || !agents.Lookup(name).Capabilities().PreTool) {
			t.Fatalf("Claude PreToolUse decision must be installed and audited")
		}
	}
}

func TestRuntimeCapabilityAuditIsDocumented(t *testing.T) {
	doc, err := os.ReadFile("../../docs/AGENT-REFERENCE.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{
		"Claude Code and OpenCode", "Codex, Cursor, and Antigravity",
		"pre-tool hook replaces an eligible shell command", "undocumented post-tool output mutation",
	} {
		if !strings.Contains(string(doc), needle) {
			t.Errorf("capability documentation missing %q", needle)
		}
	}
}

func TestRuntimeCapabilityAuditFlagsOpenCodeMissingShellPolicyCoverage(t *testing.T) {
	profile, ok := agents.RuntimeCapabilities(agents.OpenCodeName)
	if !ok {
		t.Fatal("OpenCode has no audited capability profile")
	}
	if profile.ShellPolicyCoverage {
		t.Fatal("OpenCode does not receive a pre-tool event and must not claim shell policy coverage")
	}
	if agents.Lookup(agents.OpenCodeName).Capabilities().PreTool {
		t.Fatal("OpenCode adapter must not advertise PreTool while its plugin never dispatches a pre-tool event")
	}
}

func TestRuntimeCapabilityAuditRecordsAntigravityPermissionBypassContext(t *testing.T) {
	profile, ok := agents.RuntimeCapabilities(agents.AntigravityName)
	if !ok {
		t.Fatal("Antigravity has no audited capability profile")
	}
	if !profile.ShellPolicyCoverage || !profile.PreToolDecision {
		t.Fatal("Antigravity's pre-tool hook is what makes its permission-bypass rewrite reviewable; both must stay true")
	}
	if profile.PostToolOutput {
		t.Fatal("Antigravity's PostToolUse payload has no output field; PostToolOutput must stay false, not be advertised")
	}
}

func TestRuntimeCapabilityAuditShellPolicyCoverageMatchesPreToolDecision(t *testing.T) {
	for _, name := range []string{agents.ClaudeName, agents.CursorName, agents.CodexName, agents.OpenCodeName, agents.AntigravityName} {
		profile, ok := agents.RuntimeCapabilities(name)
		if !ok {
			t.Fatalf("%s has no audited capability profile", name)
		}
		if profile.ShellPolicyCoverage != profile.PreToolDecision {
			t.Fatalf("%s: ShellPolicyCoverage=%v must match PreToolDecision=%v; a runtime cannot policy-check shell calls without a pre-tool decision", name, profile.ShellPolicyCoverage, profile.PreToolDecision)
		}
	}
}

func TestRuntimeCapabilityAuditMatchesAdapterClaims(t *testing.T) {
	for _, name := range []string{
		agents.ClaudeName,
		agents.CodexName,
		agents.OpenCodeName,
	} {
		adapter := agents.Lookup(name)
		profile, ok := agents.RuntimeCapabilities(name)
		if adapter == nil || !ok {
			t.Fatalf("missing adapter or profile for %q", name)
		}
		caps := adapter.Capabilities()
		if caps.OutputReplace != (profile.Replacement != agents.ReplacementNone) {
			t.Fatalf("%s OutputReplace=%v conflicts with audited %s", name, caps.OutputReplace, profile.Replacement)
		}
		if profile.Telemetry && !caps.PostTool {
			t.Fatalf("%s telemetry requires PostTool dispatch", name)
		}
	}
}
