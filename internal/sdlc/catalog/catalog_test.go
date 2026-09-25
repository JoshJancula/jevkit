package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverNativeParsesRubric(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "security-reviewer.md"), "---\nname: security-reviewer\ndescription: Reviews auth, crypto, input validation and secret handling.\nmodel: opus\n---\n\nBody text.\n")
	got := DiscoverNative(dir)
	if len(got) != 1 {
		t.Fatalf("DiscoverNative() = %d agents, want 1", len(got))
	}
	if got[0].Name != "security-reviewer" || got[0].Model != "opus" {
		t.Errorf("got %+v", got[0])
	}
	if got[0].Description != "Reviews auth, crypto, input validation and secret handling." {
		t.Errorf("Description = %q", got[0].Description)
	}
}

func TestDiscoverRuntimeAgentsProjectAndGlobal(t *testing.T) {
	project, global := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(project, "review.md"), "---\ndescription: Project review.\nmodel: opus\n---\n")
	writeFile(t, filepath.Join(global, "review.md"), "---\ndescription: Global review.\n---\n")
	writeFile(t, filepath.Join(global, "writer.md"), "---\ndescription: Writes docs.\n---\n")
	got := DiscoverRuntimeAgents("opencode", project, global)
	if len(got) != 2 || got[0].Name != "review" || got[0].Description != "Project review." || got[1].Name != "writer" {
		t.Fatalf("runtime discovery: %+v", got)
	}
	c, err := Merge(got)
	if err != nil {
		t.Fatal(err)
	}
	if agent, ok := c.Agent("opencode/review"); !ok || agent.RuntimeAgent != "review" || agent.Via != ViaRuntime {
		t.Fatalf("named runtime entry: %+v %v", agent, ok)
	}
}

func TestDiscoverRuntimeAgentConfigJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "implementation"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "implementation", "config.json"), `{"description":"Implement changes.","model":"model-x"}`)
	got := DiscoverRuntimeAgents("antigravity", dir)
	if len(got) != 1 || got[0].Name != "implementation" || got[0].Model != "model-x" {
		t.Fatalf("config discovery: %+v", got)
	}
}

func TestDiscoverNativeSkipsMalformedNotFatal(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "good.md"), "---\nname: good\ndescription: A fine agent.\n---\n")
	writeFile(t, filepath.Join(dir, "no-frontmatter.md"), "just a markdown file, no frontmatter\n")
	writeFile(t, filepath.Join(dir, "bad-yaml.md"), "---\nname: [unterminated\n---\n")
	writeFile(t, filepath.Join(dir, "missing-desc.md"), "---\nname: incomplete\n---\n")

	got := DiscoverNative(dir)
	var names []string
	var warnings int
	for _, a := range got {
		if a.Name != "" {
			names = append(names, a.Name)
		} else {
			warnings += len(a.Warnings)
		}
	}
	if len(names) != 1 || names[0] != "good" {
		t.Fatalf("parsed names = %v, want only [good]", names)
	}
	if warnings != 3 {
		t.Fatalf("expected 3 warnings for the 3 malformed files, got %d", warnings)
	}
}

func TestDiscoverNativeAbsentDirIsNotFatal(t *testing.T) {
	got := DiscoverNative(filepath.Join(t.TempDir(), "does-not-exist"))
	if len(got) != 0 {
		t.Fatalf("expected no agents from an absent directory, got %v", got)
	}
}

func TestDiscoverNativeProjectWinsOverUser(t *testing.T) {
	project, user := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(project, "reviewer.md"), "---\nname: reviewer\ndescription: Project version.\n---\n")
	writeFile(t, filepath.Join(user, "reviewer.md"), "---\nname: reviewer\ndescription: User version.\n---\n")
	got := DiscoverNative(project, user)
	if len(got) != 1 || got[0].Description != "Project version." {
		t.Fatalf("got %+v, want the project definition to win", got)
	}
}

const codexRubric = `WHEN: a spec is precise and the change is mechanical across several files.
WHERE: any language; strong at large refactors with a clear contract.
WHY: fast and cheap on well-specified work; weak when scope is ambiguous.
`

func TestMergeIDCollisionIsAnError(t *testing.T) {
	native := []NativeAgent{{Name: "codex-implementer", Description: "native version", Path: "a.md"}}
	ledger := LedgerFile{Version: 1, Path: "agents.yaml", Agents: []LedgerAgent{
		{ID: "codex-implementer", Rubric: codexRubric, Via: ViaRuntime, Runtime: "codex", Model: "gpt-5.6-luna"},
	}}
	_, err := Merge(native, ledger)
	if err == nil || !strings.Contains(err.Error(), "declared by both") {
		t.Fatalf("expected an id-collision error, got %v", err)
	}
}

func TestMergeBuildsHomogeneousCriteria(t *testing.T) {
	native := []NativeAgent{{Name: "security-reviewer", Description: "Reviews auth, crypto.", Model: "opus", Path: "a.md"}}
	ledger := LedgerFile{Version: 1, Path: "agents.yaml", Agents: []LedgerAgent{
		{ID: "codex-implementer", Rubric: codexRubric, Via: ViaRuntime, Runtime: "codex", Model: "gpt-5.6-luna", WriteScopes: []string{"src/**"}},
		{ID: "local-security-reviewer", Rubric: "WHEN: touches authn/authz.", Via: ViaNative, Subagent: "security-reviewer"},
	}}
	c, err := Merge(native, ledger)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	wantIDs := []string{"codex-implementer", "local-security-reviewer", "security-reviewer"}
	if got := c.IDs(); !equalScopes(got, wantIDs) {
		t.Fatalf("IDs() = %v, want %v", got, wantIDs)
	}
	rubrics := c.Rubrics()
	if rubrics["codex-implementer"] != codexRubric {
		t.Errorf("codex-implementer rubric mismatch")
	}
	sr, ok := c.Agent("security-reviewer")
	if !ok || sr.Via != ViaNative || sr.Subagent != "security-reviewer" {
		t.Errorf("security-reviewer = %+v", sr)
	}
	augment, ok := c.Agent("local-security-reviewer")
	if !ok || augment.Via != ViaNative || augment.Subagent != "security-reviewer" {
		t.Errorf("local-security-reviewer = %+v", augment)
	}
}

func TestReadOnlyMustBeEnforceable(t *testing.T) {
	cases := []struct {
		runtime string
		wantErr bool
	}{
		{"cursor", false}, {"claude", false}, {"codex", false},
		{"opencode", true}, {"antigravity", false}, {"nonesuch", true},
	}
	for _, tc := range cases {
		t.Run(tc.runtime, func(t *testing.T) {
			ledger := LedgerFile{Version: 1, Path: "agents.yaml", Agents: []LedgerAgent{
				{ID: "x", Rubric: "r", Via: ViaRuntime, Runtime: tc.runtime, Model: "m", ReadOnly: true},
			}}
			_, err := Merge(nil, ledger)
			if tc.wantErr && err == nil {
				t.Fatalf("expected readOnly-not-enforceable error for runtime %q", tc.runtime)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for runtime %q: %v", tc.runtime, err)
			}
		})
	}
}

func TestUserOverrideChangesReachOnly(t *testing.T) {
	ledger := LedgerFile{Version: 1, Path: "project.yaml", Agents: []LedgerAgent{
		{ID: "codex-implementer", Rubric: codexRubric, Via: ViaRuntime, Runtime: "codex", Model: "gpt-5.6-luna", WriteScopes: []string{"src/**"}},
	}}
	c, err := Merge(nil, ledger)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	override := LedgerFile{Version: 1, Path: "~/.config/jevkit/sdlc/agents.yaml", Agents: []LedgerAgent{
		{ID: "codex-implementer", Model: "gpt-6"},
	}}
	if err := c.ApplyUserOverride(override); err != nil {
		t.Fatalf("ApplyUserOverride: %v", err)
	}
	a, _ := c.Agent("codex-implementer")
	if a.Model != "gpt-6" {
		t.Errorf("Model = %q, want gpt-6", a.Model)
	}
	if a.Rubric != codexRubric {
		t.Errorf("rubric changed by a reach-only override")
	}
}

func TestUserOverrideRejectsRubricChange(t *testing.T) {
	ledger := LedgerFile{Version: 1, Path: "project.yaml", Agents: []LedgerAgent{
		{ID: "codex-implementer", Rubric: codexRubric, Via: ViaRuntime, Runtime: "codex", Model: "gpt-5.6-luna"},
	}}
	c, err := Merge(nil, ledger)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	override := LedgerFile{Version: 1, Path: "user.yaml", Agents: []LedgerAgent{
		{ID: "codex-implementer", Rubric: "a different rubric entirely"},
	}}
	if err := c.ApplyUserOverride(override); err == nil || !strings.Contains(err.Error(), "may not change rubric") {
		t.Fatalf("expected a rubric-override rejection, got %v", err)
	}
}

func TestUserOverrideRejectsWriteScopeChange(t *testing.T) {
	ledger := LedgerFile{Version: 1, Path: "project.yaml", Agents: []LedgerAgent{
		{ID: "codex-implementer", Rubric: codexRubric, Via: ViaRuntime, Runtime: "codex", Model: "gpt-5.6-luna", WriteScopes: []string{"src/**"}},
	}}
	c, err := Merge(nil, ledger)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	override := LedgerFile{Version: 1, Path: "user.yaml", Agents: []LedgerAgent{
		{ID: "codex-implementer", WriteScopes: []string{"**"}},
	}}
	if err := c.ApplyUserOverride(override); err == nil || !strings.Contains(err.Error(), "may not change writeScopes") {
		t.Fatalf("expected a writeScopes-override rejection, got %v", err)
	}
}

func TestUserOverrideRejectsUnknownID(t *testing.T) {
	c, err := Merge(nil)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	override := LedgerFile{Version: 1, Path: "user.yaml", Agents: []LedgerAgent{
		{ID: "nonexistent", Model: "gpt-6"},
	}}
	if err := c.ApplyUserOverride(override); err == nil || !strings.Contains(err.Error(), "not in the catalog") {
		t.Fatalf("expected an unknown-id rejection, got %v", err)
	}
}
