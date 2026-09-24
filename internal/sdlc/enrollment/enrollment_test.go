package enrollment

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoveryCannotEnrollAndBothPoliciesApply(t *testing.T) {
	p := DefaultPolicy()
	reach := Reach{Driver: "cli", Runtimes: map[string]RuntimeCapability{"codex": {Write: true}, "cursor": {Write: true}}}
	if got := Eligible(p, Roster{Version: 1}, reach, Requirement{Role: "implementer", Write: true}); len(got) != 0 {
		t.Fatalf("discovery without enrollment yielded %v", got)
	}
	r := Roster{Version: 1, Agents: []Agent{
		{ID: "one", Roles: []string{"implementer"}, Rubric: "implementation", Via: Runtime, Runtime: "codex", Model: "m"},
		{ID: "two", Roles: []string{"assessor"}, Rubric: "review", Via: Runtime, Runtime: "cursor", Model: "m"},
	}}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := Eligible(p, r, reach, Requirement{Role: "implementer", Write: true}); len(got) != 1 || got[0].Agent.ID != "one" {
		t.Fatalf("eligible=%v", got)
	}
	p.Roles["implementer"] = RolePolicy{Via: []string{Runtime}, Runtimes: []string{"cursor"}}
	if got := Eligible(p, r, reach, Requirement{Role: "implementer", Write: true}); len(got) != 0 {
		t.Fatalf("project restriction ignored: %v", got)
	}
}

func TestHostAndCLIModesDoNotPromoteNativeOrSelf(t *testing.T) {
	p := DefaultPolicy()
	r := Roster{Version: 1, Agents: []Agent{
		{ID: "self", Roles: []string{"planner"}, Rubric: "self", Via: HostSelf},
		{ID: "native", Roles: []string{"planner"}, Rubric: "native", Via: Native, Subagent: "planner"},
		{ID: "cli", Roles: []string{"planner"}, Rubric: "cli", Via: Runtime, Runtime: "codex", Model: "m"},
	}}
	cli := Reach{Driver: "cli", Runtimes: map[string]RuntimeCapability{"codex": {Write: true}}}
	if got := Eligible(p, r, cli, Requirement{Role: "planner"}); len(got) != 1 || got[0].Agent.ID != "cli" {
		t.Fatalf("CLI candidates=%v", got)
	}
	host := Reach{Driver: "host", Self: true, Native: map[string]HostCapability{"planner": {Write: true}}, Runtimes: cli.Runtimes}
	if got := Eligible(p, r, host, Requirement{Role: "planner"}); len(got) != 3 {
		t.Fatalf("host candidates=%v", got)
	}
	r.Agents = r.Agents[1:]
	if got := Eligible(p, r, host, Requirement{Role: "planner"}); len(got) != 2 {
		t.Fatalf("host-self was implicit: %v", got)
	}
}

func TestHostCannotClaimUnsupportedRestrictions(t *testing.T) {
	p := DefaultPolicy()
	r := Roster{Version: 1, Agents: []Agent{{ID: "n", Roles: []string{"assessor"}, Rubric: "review", Via: Native, Subagent: "n", ReadOnly: true, Isolated: true}}}
	reach := Reach{Driver: "host", Native: map[string]HostCapability{"n": {Write: true}}}
	if got := Eligible(p, r, reach, Requirement{Role: "assessor", ReadOnly: true, Isolated: true}); len(got) != 0 {
		t.Fatalf("unsupported restrictions claimed: %v", got)
	}
	reach.Native["n"] = HostCapability{ReadOnly: true, Isolated: true}
	if got := Eligible(p, r, reach, Requirement{Role: "assessor", ReadOnly: true, Isolated: true}); len(got) != 1 {
		t.Fatalf("supported restrictions filtered: %v", got)
	}
}

func TestProjectWriteScopeNeedsAdapterEnforcement(t *testing.T) {
	p := DefaultPolicy()
	p.Roles["implementer"] = RolePolicy{Via: []string{Runtime}, Write: true, WriteScopes: []string{"src/**"}}
	r := Roster{Version: 1, Agents: []Agent{{ID: "coder", Roles: []string{"implementer"}, Rubric: "code", Via: Runtime, Runtime: "cursor", Model: "m"}}}
	reach := Reach{Driver: "cli", Runtimes: map[string]RuntimeCapability{"cursor": {Write: true}}}
	if got := Eligible(p, r, reach, Requirement{Role: "implementer", Write: true}); len(got) != 0 {
		t.Fatalf("unenforced write scope accepted: %v", got)
	}
	reach.Runtimes["cursor"] = RuntimeCapability{Write: true, Scopes: true}
	if got := Eligible(p, r, reach, Requirement{Role: "implementer", Write: true, Scopes: []string{"elsewhere/**"}}); len(got) != 0 {
		t.Fatalf("outside project scope accepted: %v", got)
	}
	if got := Eligible(p, r, reach, Requirement{Role: "implementer", Write: true, Scopes: []string{"src/**"}}); len(got) != 1 {
		t.Fatalf("enforced scope rejected: %v", got)
	}
}

func TestRuntimeBinaryOverrideNeedsItsOwnReach(t *testing.T) {
	p := DefaultPolicy()
	r := Roster{Version: 1, Agents: []Agent{{ID: "custom", Roles: []string{"implementer"}, Rubric: "code", Via: Runtime, Runtime: "codex", Model: "m", Binary: "/custom/codex"}}}
	reach := Reach{Driver: "cli", Runtimes: map[string]RuntimeCapability{"codex": {Write: true}}}
	if got := Eligible(p, r, reach, Requirement{Role: "implementer", Write: true}); len(got) != 0 {
		t.Fatalf("default binary incorrectly covered override: %v", got)
	}
	reach.Binaries = map[string]RuntimeCapability{"/custom/codex": {Write: true}}
	if got := Eligible(p, r, reach, Requirement{Role: "implementer", Write: true}); len(got) != 1 {
		t.Fatalf("custom binary not reachable: %v", got)
	}
}

func TestQuorumCountsBindingsNotAliases(t *testing.T) {
	p := DefaultPolicy()
	if n, err := p.Quorum("assured"); err != nil || n != 3 {
		t.Fatalf("quorum=%d %v", n, err)
	}
	if n := DistinctBindings([]Candidate{{Binding: "runtime:cursor:m"}, {Binding: "runtime:cursor:m"}, {Binding: "runtime:codex:m"}}); n != 2 {
		t.Fatalf("distinct bindings=%d", n)
	}
	p.MinimumProfile = "collaborative"
	if _, err := p.Quorum("lean"); err == nil {
		t.Fatal("minimum profile ignored")
	}
}

func TestPolicyHasPositiveTimeLimits(t *testing.T) {
	p := DefaultPolicy()
	if p.MaxInvocationSeconds != 1800 || p.MaxRunSeconds != 21600 || p.MaxAssignments != 20 || p.MaxRevisions != 3 {
		t.Fatalf("defaults: %+v", p)
	}
	p.MaxInvocationSeconds = 0
	if err := p.Validate(); err == nil {
		t.Fatal("zero invocation timeout accepted")
	}
	p = DefaultPolicy()
	p.MaxRunSeconds = 0
	if err := p.Validate(); err == nil {
		t.Fatal("zero run timeout accepted")
	}
}

func TestPolicyAndRosterLoadSeparately(t *testing.T) {
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(policyPath, []byte("version: 1\nminimumProfile: assured\nmaxConcurrent: 2\nroles:\n  assessor:\n    via: [runtime]\n    runtimes: [cursor]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPolicy(policyPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Quorum("collaborative"); err == nil {
		t.Fatal("minimum ignored")
	}
	if got := p.Roles["assessor"].Runtimes; len(got) != 1 || got[0] != "cursor" {
		t.Fatalf("rule=%v", got)
	}
	r, err := LoadRoster(filepath.Join(dir, "missing.yaml"))
	if err != nil || len(r.Agents) != 0 {
		t.Fatalf("missing roster=%v %v", r, err)
	}
}
