package main

import (
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/OWNER/jevkit/internal/jev"
)

func TestModelSelectionPersistsAndEnvironmentOverrides(t *testing.T) {
	a := newApp(t)
	out, _ := mustRun(t, a, "", exitOK, "model", "status")
	if !strings.Contains(out, "model: "+jev.DefaultModel) || !strings.Contains(out, "source: built-in default") {
		t.Fatalf("default status: %q", out)
	}
	mustRun(t, a, "", exitOK, "model", "set", "jev-1.13.0")
	if got := readFile(t, a.modelPath()); got != "jev-1.13.0\n" {
		t.Fatalf("saved model = %q", got)
	}
	if info, err := os.Stat(a.modelPath()); err != nil {
		t.Fatal(err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("model file mode = %04o", info.Mode().Perm())
	}

	// A new process using the same config directory gets the saved choice.
	b := newApp(t)
	b.ConfigDir = a.ConfigDir
	cfg, err := b.jevConfig()
	if err != nil || cfg.Model != "jev-1.13.0" {
		t.Fatalf("persisted config = %+v, %v", cfg, err)
	}
	b.Environ = append(b.Environ, "JEVKIT_MODEL=jev-override")
	cfg, err = b.jevConfig()
	if err != nil || cfg.Model != "jev-override" {
		t.Fatalf("environment config = %+v, %v", cfg, err)
	}
	out, _ = mustRun(t, b, "", exitOK, "model", "status")
	if !strings.Contains(out, "model: jev-override") || !strings.Contains(out, "source: JEVKIT_MODEL") {
		t.Fatalf("environment status: %q", out)
	}
	mustRun(t, b, "", exitOK, "model", "clear")
	if _, err := os.Stat(a.modelPath()); !os.IsNotExist(err) {
		t.Fatalf("model file remains after clear: %v", err)
	}
	b.Environ = b.Environ[:len(b.Environ)-1]
	cfg, err = b.jevConfig()
	if err != nil || cfg.Model != jev.DefaultModel {
		t.Fatalf("cleared config = %+v, %v", cfg, err)
	}
}

func TestModelSelectionRejectsInvalidSetting(t *testing.T) {
	a := newApp(t)
	mustRun(t, a, "", exitOK, "model", "set", "jev-good")
	for _, name := range []string{"", "bad model", "../bad", "jev\nother"} {
		if code, _, _ := run(a, "", "model", "set", name); code != exitUsage {
			t.Errorf("set %q exit = %d", name, code)
		}
	}
	if got := readFile(t, a.modelPath()); got != "jev-good\n" {
		t.Fatalf("invalid set replaced saved model: %q", got)
	}
	writeFile(t, a.modelPath(), "bad model\n")
	if code, _, _ := run(a, "", "model", "status"); code != exitFail {
		t.Fatalf("invalid stored model status exit = %d", code)
	}
	if _, err := a.jevConfig(); err == nil {
		t.Fatal("invalid stored model accepted")
	}
	a.Environ = append(a.Environ, "JEVKIT_MODEL=jev-env")
	if cfg, err := a.jevConfig(); err != nil || cfg.Model != "jev-env" {
		t.Fatalf("environment override should bypass stored model: %+v, %v", cfg, err)
	}
	a.Environ[len(a.Environ)-1] = "JEVKIT_MODEL=bad model"
	if _, err := a.jevConfig(); err == nil {
		t.Fatal("invalid environment model accepted")
	}
}

func TestSavedModelReachesJevClient(t *testing.T) {
	a, _, fj := cliApp(t)
	mustRun(t, a, secretKey+"\n", exitOK, "key", "set")
	mustRun(t, a, "", exitOK, "model", "set", "jev-1.13.0")
	var used string
	a.NewJev = func(cfg jev.Config, key func() (string, error)) Asker {
		used = cfg.Model
		return fj
	}
	fj.resp = &jev.Response{Answers: map[string]jev.Answer{"answer": jev.NoulAnswer{Noul: 0.8}}}
	mustRun(t, a, "", exitOK, "ask", "noul", "--state", "tests passed", "--question", "Did they pass?")
	if used != "jev-1.13.0" {
		t.Fatalf("Jev client model = %q", used)
	}
}
