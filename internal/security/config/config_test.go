package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectCannotLoosenAnything(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, ".jevkit")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		"version: 1\nmode: shadow\n",
		"version: 1\njev_scoring: false\n",
		"version: 1\nsandbox:\n  allow_read: [/tmp]\n",
	} {
		if err := os.WriteFile(filepath.Join(project, "security.yaml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(LoadOptions{WorkDir: dir}); err == nil {
			t.Fatalf("project policy loosened security with %q", body)
		}
	}
}

func TestProjectAdditiveKillswitchOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".jevkit", "security.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("version: 1\nkillswitch: ['echo danger']\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(LoadOptions{WorkDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"rm -rf /", "echo danger"} {
		if _, ok := cfg.Killswitch.Match(command); !ok {
			t.Fatalf("%q was not blocked", command)
		}
	}
}

func TestSandboxAllowlistDefaults(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Builtin(LoadOptions{ConfigDir: filepath.Join(dir, "config"), StateDir: filepath.Join(dir, "state")})
	if err != nil {
		t.Fatal(err)
	}
	for _, wanted := range []string{".claude", ".codex", ".cursor", ".opencode", "config", "state"} {
		found := false
		for _, path := range cfg.AllowRead {
			if strings.HasSuffix(path, wanted) {
				found = true
			}
		}
		if !found {
			t.Errorf("missing allowlist path %s", wanted)
		}
	}
	if cfg.JevScoring {
		t.Fatal("builtin scoring must default off")
	}
}

func TestInvalidPolicyFallsBackToBuiltin(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".jevkit", "security.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("version: 1\nmode: shadow\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadForHook(LoadOptions{WorkDir: dir})
	if err == nil || cfg.Mode != "enforce" {
		t.Fatalf("fallback=%+v err=%v", cfg, err)
	}
	if _, ok := cfg.Killswitch.Match("rm -rf /"); !ok {
		t.Fatal("builtin killswitch lost on fallback")
	}
}

func TestGuardRejectsEscapeAndAllowsNamedFile(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	file := filepath.Join(outside, "task.md")
	g := Guard{Workspace: root, AllowRead: []string{file}}
	if _, ok := g.Check("read " + file); !ok {
		t.Fatal("named task file rejected")
	}
	if _, ok := g.Check("read " + filepath.Join(outside, "other.md")); ok {
		t.Fatal("outside file allowed")
	}
	if _, ok := g.Check("read ../other.md"); ok {
		t.Fatal("parent path allowed")
	}
	if _, ok := g.Check("cd .."); ok {
		t.Fatal("bare parent segment allowed")
	}
	g.AllowRead = append(g.AllowRead, outside)
	if _, ok := g.CheckWorkDir(outside); ok {
		t.Fatal("allow_read permitted an outside working directory")
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err == nil {
		g.AllowRead = nil
		if _, ok := g.Check("read " + link + "/../secret"); ok {
			t.Fatal("symlink followed by parent segment escaped guard")
		}
	}
}
