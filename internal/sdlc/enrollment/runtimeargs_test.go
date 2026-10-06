package enrollment

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRuntimeArgsQuotingAndLiteralValues(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want []string
	}{
		{"", nil},
		{" \t\n ", nil},
		{"--dangerously-skip-permissions --effort high", []string{"--dangerously-skip-permissions", "--effort", "high"}},
		{`--append-system-prompt "a value with spaces"`, []string{"--append-system-prompt", "a value with spaces"}},
		{`--append-system-prompt 'a "quoted" value'`, []string{"--append-system-prompt", `a "quoted" value`}},
		{`--tools ""`, []string{"--tools", ""}},
		{`--flag escaped\ space --other=one" two"`, []string{"--flag", "escaped space", "--other=one two"}},
		{`--flag "$HOME $(touch marker) *.go ; |"`, []string{"--flag", "$HOME $(touch marker) *.go ; |"}},
		{`--flag "C:\project\file"`, []string{"--flag", `C:\project\file`}},
		{`--flag "a\"b"`, []string{"--flag", `a"b`}},
	} {
		got, err := splitRuntimeArgs(tc.raw)
		if err != nil || !slices.Equal(got, tc.want) {
			t.Fatalf("%q: got %q %v, want %q", tc.raw, got, err, tc.want)
		}
	}
	for _, raw := range []string{`--flag "unclosed`, `--flag 'unclosed`, "--flag unfinished\\", "--flag \x00"} {
		if _, err := splitRuntimeArgs(raw); err == nil {
			t.Fatalf("malformed arguments accepted: %q", raw)
		}
	}
}

func TestRuntimeArgsValidationAndEnforcement(t *testing.T) {
	off := false
	for _, tc := range []struct {
		runtime, raw string
		readOnly     bool
		tools        *ToolPolicy
		want         string
	}{
		{"claude", "--dangerously-skip-permissions", false, nil, ""},
		{"claude", "--effort high", true, nil, ""},
		{"claude", "--dangerously-skip-permissions", true, nil, "read-only"},
		{"claude", "--permission-mode=acceptEdits", true, nil, "read-only"},
		{"claude", "--settings custom.json", true, nil, "read-only"},
		{"claude", "--tools default", false, &ToolPolicy{Web: &off}, "tools restriction"},
		{"claude", "--disallowed-tools=Bash", false, &ToolPolicy{Web: &off}, "tools restriction"},
		{"claude", "--dangerously-skip-permissions", false, &ToolPolicy{Web: &off}, ""},
		{"codex", `-c model_reasoning_effort="high"`, false, &ToolPolicy{Web: &off}, ""},
		{"codex", "--enable multi_agent", false, &ToolPolicy{Delegate: &off}, "tools restriction"},
		{"codex", "--yolo", true, nil, "read-only"},
		{"codex", "-csandbox_mode=danger-full-access", true, nil, "read-only"},
		{"cursor", "--force", false, nil, ""},
		{"cursor", "--force", true, nil, "read-only"},
		{"opencode", "--variant high", false, nil, ""},
		{"antigravity", "--dangerously-skip-permissions", false, nil, ""},
		{"antigravity", "--dangerously-skip-permissions", true, nil, "read-only"},
		{"claude", "--model=other", false, nil, "invocation control"},
		{"claude", "--resume session", false, nil, "invocation control"},
		{"cursor", "--output-format text", false, nil, "invocation control"},
		{"opencode", "--agent reviewer", false, nil, "invocation control"},
		{"codex", "-mother", false, nil, "invocation control"},
		{"claude", "-- --dangerously-skip-permissions", false, nil, "invocation control"},
		{"claude", "a prompt", false, nil, "CLI option"},
	} {
		a := Agent{ID: "a", Via: Runtime, Runtime: tc.runtime, RuntimeArgs: tc.raw, Tools: tc.tools}
		_, err := a.ParseRuntimeArgs(tc.readOnly)
		if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Fatalf("%s %q readOnly=%t: got %v, want %q", tc.runtime, tc.raw, tc.readOnly, err, tc.want)
		}
	}
	for _, via := range []string{HostSelf, Native} {
		if _, err := (Agent{Via: via, RuntimeArgs: "--effort high"}).ParseRuntimeArgs(false); err == nil {
			t.Fatalf("runtimeArgs accepted for %s", via)
		}
	}
}

func TestRosterRuntimeArgsRoundTripAndBindingIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roster.yaml")
	raw := "version: 1\nagents:\n  - {id: a, via: runtime, runtime: claude, model: m, roles: [implementer], rubric: Build, tools: auto, runtimeArgs: '--dangerously-skip-permissions --effort high'}\n"
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	roster, err := LoadRoster(path)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := yaml.Marshal(roster)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	decoded, err := LoadRoster(path)
	if err != nil || decoded.Agents[0].RuntimeArgs != roster.Agents[0].RuntimeArgs {
		t.Fatalf("runtimeArgs lost in round trip: %v %s", err, encoded)
	}
	other := decoded.Agents[0]
	other.ID, other.RuntimeArgs = "b", "--effort low"
	roster.Agents = append(roster.Agents, other)
	reach := Reach{Runtimes: map[string]RuntimeCapability{"claude": {Write: true, ReadOnly: true}}}
	candidates := Eligible(DefaultPolicy(), roster, reach, Requirement{Role: "implementer"})
	if len(candidates) != 2 || DistinctBindings(candidates) != 1 {
		t.Fatalf("runtimeArgs changed independent quorum bindings: %+v", candidates)
	}
	roster.Agents[0].ReadOnly = true
	if err := roster.Validate(); err == nil {
		t.Fatal("permission override accepted with explicit readOnly")
	}
	if got := (Agent{RuntimeArgs: "  \t"}).RuntimeArgsFingerprint(); got != (Agent{}).RuntimeArgsFingerprint() {
		t.Fatal("empty arguments changed session identity")
	}
}
