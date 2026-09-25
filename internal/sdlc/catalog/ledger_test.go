package catalog

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadLedgerMissingFileIsNotFatal(t *testing.T) {
	l, err := LoadLedger(filepath.Join(t.TempDir(), "agents.yaml"))
	if err != nil {
		t.Fatalf("LoadLedger: %v", err)
	}
	if len(l.Agents) != 0 {
		t.Errorf("expected no agents from a missing ledger, got %v", l.Agents)
	}
}

func TestLoadLedgerParsesPlanExample(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.yaml")
	writeFile(t, path, `version: 1
agents:
  - id: codex-implementer
    rubric: |
      WHEN: a spec is precise and the change is mechanical across several files.
    via: runtime
    runtime: codex
    model: gpt-5.6-luna
    writeScopes: ["src/**", "internal/**"]

  - id: cursor-planner
    rubric: |
      WHEN: the task is underspecified and needs analysis before any edit.
    via: runtime
    runtime: cursor
    model: gpt-5
    readOnly: true

  - id: opencode-reviewer
    rubric: |
      WHEN: an independent second opinion on a diff is worth its cost.
    via: runtime
    runtime: opencode
    model: anthropic/claude-opus-5
    agent: reviewer

  - id: local-security-reviewer
    via: native
    subagent: security-reviewer
    rubric: |
      WHEN: the change touches authn, authz, crypto or secret handling.
`)
	l, err := LoadLedger(path)
	if err != nil {
		t.Fatalf("LoadLedger: %v", err)
	}
	if len(l.Agents) != 4 {
		t.Fatalf("len(Agents) = %d, want 4", len(l.Agents))
	}
	byID := map[string]LedgerAgent{}
	for _, a := range l.Agents {
		byID[a.ID] = a
	}
	if byID["codex-implementer"].Runtime != "codex" || len(byID["codex-implementer"].WriteScopes) != 2 {
		t.Errorf("codex-implementer = %+v", byID["codex-implementer"])
	}
	if !byID["cursor-planner"].ReadOnly {
		t.Errorf("cursor-planner should be readOnly")
	}
	if byID["opencode-reviewer"].Agent != "reviewer" {
		t.Errorf("opencode-reviewer.Agent = %q", byID["opencode-reviewer"].Agent)
	}
	if byID["local-security-reviewer"].Via != ViaNative || byID["local-security-reviewer"].Subagent != "security-reviewer" {
		t.Errorf("local-security-reviewer = %+v", byID["local-security-reviewer"])
	}
}

func TestLoadLedgerRejectsUnknownField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.yaml")
	writeFile(t, path, "version: 1\nagents:\n  - id: x\n    rubric: r\n    via: runtime\n    runtime: codex\n    bogus: true\n")
	if _, err := LoadLedger(path); err == nil {
		t.Fatal("expected an error for an unknown field")
	}
}

func TestLoadLedgerRejectsDuplicateID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.yaml")
	writeFile(t, path, "version: 1\nagents:\n  - {id: x, rubric: r, via: runtime, runtime: codex}\n  - {id: x, rubric: r2, via: runtime, runtime: cursor}\n")
	_, err := LoadLedger(path)
	if err == nil || !strings.Contains(err.Error(), "duplicate agent id") {
		t.Fatalf("expected a duplicate-id error, got %v", err)
	}
}

func TestLoadLedgerValidatesViaShape(t *testing.T) {
	cases := []struct {
		name, yaml, want string
	}{
		{"native missing subagent", "version: 1\nagents:\n  - {id: x, rubric: r, via: native}\n", "requires subagent"},
		{"runtime missing runtime", "version: 1\nagents:\n  - {id: x, rubric: r, via: runtime}\n", "requires runtime"},
		{"native with runtime field", "version: 1\nagents:\n  - {id: x, rubric: r, via: native, subagent: s, runtime: codex}\n", "cannot declare runtime reach"},
		{"missing rubric", "version: 1\nagents:\n  - {id: x, via: runtime, runtime: codex}\n", "rubric is required"},
		{"bad via", "version: 1\nagents:\n  - {id: x, rubric: r, via: sideways}\n", "via must be"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "agents.yaml")
			writeFile(t, path, tc.yaml)
			_, err := LoadLedger(path)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want error containing %q", err, tc.want)
			}
		})
	}
}
