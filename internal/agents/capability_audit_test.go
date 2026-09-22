package agents_test

import (
	"os"
	"strings"
	"testing"

	"github.com/OWNER/jevkit/internal/agents"
)

func TestRuntimeCapabilityAuditCoversEveryAdapter(t *testing.T) {
	want := map[string]agents.ReplacementScope{
		agents.ClaudeName:      agents.ReplacementToolShapes,
		agents.CursorName:      agents.ReplacementMCPOnly,
		agents.CodexName:       agents.ReplacementNone,
		agents.OpenCodeName:    agents.ReplacementNone,
		agents.AntigravityName: agents.ReplacementNone,
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
	}
}

func TestRuntimeCapabilityAuditIsDocumented(t *testing.T) {
	doc, err := os.ReadFile("../../docs/AGENTS.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{
		"| Claude Code |", "| Cursor |", "| Codex |", "| OpenCode |", "| Antigravity |",
		"validated tool-result shape", "MCP** results", "No generic result replacement", "Telemetry only",
	} {
		if !strings.Contains(string(doc), needle) {
			t.Errorf("capability matrix missing %q", needle)
		}
	}
}

func TestRuntimeCapabilityAuditMatchesAdapterClaims(t *testing.T) {
	for _, name := range []string{
		agents.ClaudeName,
		agents.CursorName,
		agents.CodexName,
		agents.OpenCodeName,
		agents.AntigravityName,
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
