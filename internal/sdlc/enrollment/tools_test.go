package enrollment

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRosterToolPolicyValidationAndRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name, fields, want string
	}{
		{"omitted", "runtime: codex", ""},
		{"auto codex", "runtime: codex, tools: auto", ""},
		{"auto claude", "runtime: claude, tools: auto", ""},
		{"auto opencode", "runtime: opencode, tools: auto", ""},
		{"auto cursor", "runtime: cursor, tools: auto", ""},
		{"auto antigravity", "runtime: antigravity, tools: auto", ""},
		{"auto native", "runtime: claude, agent: reviewer, tools: auto", ""},
		{"all", "runtime: codex, tools: all", "tools must be auto"},
		{"sequence", "runtime: codex, tools: [shell]", "tools must be auto"},
		{"codex", "runtime: codex, tools: {shell: false, web: true, delegate: false}", ""},
		{"claude", "runtime: claude, tools: {web: false}", ""},
		{"native", "runtime: claude, agent: reviewer", ""},
		{"native tools", "runtime: claude, agent: reviewer, tools: {web: false}", "native runtime"},
		{"native empty tools", "runtime: claude, agent: reviewer, tools: {}", "native runtime"},
		{"unsupported controls", "runtime: cursor, tools: {shell: false}", "not supported"},
		{"unsupported native", "runtime: codex, agent: reviewer", "named-agent selection is not supported"},
		{"empty", "runtime: claude, tools: {}", "must set"},
		{"typo", "runtime: codex, tools: {shel: false}", "field shel"},
		{"wrong type", "runtime: codex, tools: {web: never}", "cannot unmarshal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "roster.yaml")
			raw := "version: 1\nagents:\n  - {id: a, via: runtime, model: m, roles: [assessor], rubric: Review, " + tc.fields + "}\n"
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			roster, err := LoadRoster(path)
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("got %v, want %q", err, tc.want)
				}
				return
			}
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
			if err != nil {
				t.Fatal(err)
			}
			if decoded.Agents[0].Tools.Fingerprint() != roster.Agents[0].Tools.Fingerprint() {
				t.Fatal("tool settings changed in YAML round trip")
			}
			if strings.HasPrefix(tc.name, "auto ") {
				p := decoded.Agents[0].Tools
				if p == nil || !p.Auto || p.Fingerprint() != (*ToolPolicy)(nil).Fingerprint() || !strings.Contains(string(encoded), "tools: auto") {
					t.Fatalf("explicit inheritance changed in round trip: %s", encoded)
				}
			}
			if tc.name == "codex" && (decoded.Agents[0].Tools.Shell == nil || *decoded.Agents[0].Tools.Shell) {
				t.Fatal("explicit false did not survive round trip")
			}
		})
	}
}

func TestNativeToolOwnershipAndReadOnlyEligibility(t *testing.T) {
	for _, runtime := range []string{"claude", "opencode", "antigravity"} {
		t.Run(runtime, func(t *testing.T) {
			a := Agent{ID: "reviewer", Via: Runtime, Runtime: runtime, RuntimeAgent: "reviewer", Model: "m", Roles: []string{"assessor"}, Rubric: "Review"}
			r := Roster{Version: 1, Agents: []Agent{a}}
			reach := Reach{Driver: "cli", Runtimes: map[string]RuntimeCapability{runtime: {Write: true, ReadOnly: true}}}
			if got := Eligible(DefaultPolicy(), r, reach, Requirement{Role: "assessor"}); len(got) != 1 {
				t.Fatalf("native agent not eligible: %v", got)
			}
			r.Agents[0].ReadOnly = true
			if got := Eligible(DefaultPolicy(), r, reach, Requirement{Role: "assessor", ReadOnly: true}); len(got) != 0 {
				t.Fatalf("claimed scaffold enforcement for native agent: %v", got)
			}
		})
	}
	off := false
	for _, via := range []string{Native, HostSelf} {
		a := Agent{ID: "native", Via: via, Tools: &ToolPolicy{Shell: &off}}
		if err := a.ValidateTools(); err == nil {
			t.Fatalf("tools accepted for %s", via)
		}
		a.Tools = &ToolPolicy{Auto: true}
		if err := a.ValidateTools(); err != nil {
			t.Fatalf("explicit inheritance rejected for %s: %v", via, err)
		}
	}
}

func TestToolMappingsPreserveYAMLAliasesAndStrictValidation(t *testing.T) {
	for _, field := range []string{"web", "typo"} {
		raw := "version: 1\nagents:\n" +
			"  - {id: a, via: runtime, runtime: claude, model: m, roles: [assessor], rubric: Review, tools: &policy {" + field + ": false}}\n" +
			"  - {id: b, via: runtime, runtime: claude, model: m, roles: [assessor], rubric: Review, tools: {<<: *policy, shell: false}}\n"
		path := filepath.Join(t.TempDir(), "roster.yaml")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		roster, err := LoadRoster(path)
		if field == "typo" {
			if err == nil || !strings.Contains(err.Error(), "field typo") {
				t.Fatalf("unknown key accepted: %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		p := roster.Agents[1].Tools
		if p.Web == nil || *p.Web || p.Shell == nil || *p.Shell {
			t.Fatalf("merged restriction lost: %+v", p)
		}
	}
}

func TestToolPolicyDoesNotCreateIndependentQuorumBindings(t *testing.T) {
	off := false
	one := Agent{ID: "one", Via: Runtime, Runtime: "codex", Model: "m", Roles: []string{"assessor"}, Rubric: "Review"}
	two := one
	two.ID, two.Tools = "two", &ToolPolicy{Web: &off}
	r := Roster{Version: 1, Agents: []Agent{one, two}}
	reach := Reach{Driver: "cli", Runtimes: map[string]RuntimeCapability{"codex": {Write: true}}}
	candidates := Eligible(DefaultPolicy(), r, reach, Requirement{Role: "assessor"})
	if len(candidates) != 2 || DistinctBindings(candidates) != 1 {
		t.Fatalf("tool settings inflated quorum: %v", candidates)
	}
}
