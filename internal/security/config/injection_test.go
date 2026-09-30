package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectInjectionCanOnlyTighten(t *testing.T) {
	root := t.TempDir()
	policy := filepath.Join(root, ".jevkit", "security.yaml")
	if err := os.MkdirAll(filepath.Dir(policy), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policy, []byte("version: 1\ninjection:\n  mode: enforce\n  heuristic_halt: true\n  halt_on: escalate\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(LoadOptions{WorkDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Injection.Mode != "enforce" || !cfg.Injection.HeuristicHalt {
		t.Fatalf("project did not tighten: %+v", cfg.Injection)
	}
	if err := os.WriteFile(policy, []byte("version: 1\ninjection:\n  mode: off\n  halt_on: act\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(LoadOptions{WorkDir: root, Environ: []string{"JEVKIT_INJECTION_GUARD=1"}})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Injection.Mode != "enforce" || cfg.Injection.HaltOn != "escalate" {
		t.Fatalf("project loosened: %+v", cfg.Injection)
	}
}
