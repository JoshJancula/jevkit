package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCompactPolicyCommands(t *testing.T) {
	a := newApp(t)
	p := filepath.Join(a.ConfigDir, "compaction.yaml")
	writeFile(t, p, "version: 1\nrules:\n  - id: keep\n    command: '^npm run build'\n    action: never-compact\n")
	code, out, _ := run(a, "", "compact", "validate")
	if code != exitOK || !strings.Contains(out, "policy ok") {
		t.Fatalf("validate: %d %q", code, out)
	}
	code, out, _ = run(a, "", "compact", "explain", "--command", "npm run build")
	if code != exitOK || !strings.Contains(out, "keep (never-compact)") {
		t.Fatalf("explain: %d %q", code, out)
	}
	code, out, _ = run(a, "", "compact", "explain", "--command", "git diff")
	if code != exitOK || !strings.Contains(out, "hard protection") {
		t.Fatalf("hard protection: %d %q", code, out)
	}
}

func TestCompactListWithoutPolicy(t *testing.T) {
	a := newApp(t)
	for _, args := range [][]string{{"compact", "list"}, {"compact", "list", "--project"}} {
		code, out, _ := run(a, "", args...)
		if code != exitOK || out != "no compaction rules\n" {
			t.Fatalf("%v: code=%d output=%q", args, code, out)
		}
	}
}

func TestCompactRuleLifecycle(t *testing.T) {
	a := newApp(t)
	code, _, _ := run(a, "", "compact", "init")
	if code != exitOK {
		t.Fatalf("init: %d", code)
	}
	code, out, _ := run(a, "", "compact", "add", "--id", "build", "--action", "never-compact", "--command", "^npm run build")
	if code != exitOK || !strings.Contains(out, "added build") {
		t.Fatalf("add: %d %q", code, out)
	}
	code, out, _ = run(a, "", "compact", "list")
	if code != exitOK || !strings.Contains(out, "Compaction rules") || !strings.Contains(out, "│ build ") || !strings.Contains(out, "│ never-compact ") {
		t.Fatalf("list: %d %q", code, out)
	}
	code, out, _ = run(a, "", "compact", "remove", "build")
	if code != exitOK || !strings.Contains(out, "removed build") {
		t.Fatalf("remove: %d %q", code, out)
	}
}
