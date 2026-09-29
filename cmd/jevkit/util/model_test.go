package util

import (
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/cmd/jevkit/internal/testkit"
	"github.com/JoshJancula/jevkit/internal/jev"
)

func TestModelSelectionPersistsAndEnvironmentOverrides(t *testing.T) {
	a := newApp(t)
	out, _ := mustRun(t, a, "", app.ExitOK, "model", "status")
	if !strings.Contains(out, "model: "+jev.DefaultModel) || !strings.Contains(out, "source: built-in default") {
		t.Fatalf("default status: %q", out)
	}
	mustRun(t, a, "", app.ExitOK, "model", "set", "jev-1.13.0")
	if got := testkit.ReadFile(t, a.ModelPath()); got != "jev-1.13.0\n" {
		t.Fatalf("saved model = %q", got)
	}
	if info, err := os.Stat(a.ModelPath()); err != nil {
		t.Fatal(err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("model file mode = %04o", info.Mode().Perm())
	}

	// A new process using the same config directory gets the saved choice.
	b := newApp(t)
	b.ConfigDir = a.ConfigDir
	cfg, err := b.JevConfig()
	if err != nil || cfg.Model != "jev-1.13.0" {
		t.Fatalf("persisted config = %+v, %v", cfg, err)
	}
	b.Environ = append(b.Environ, "JEVKIT_MODEL=jev-override")
	cfg, err = b.JevConfig()
	if err != nil || cfg.Model != "jev-override" {
		t.Fatalf("environment config = %+v, %v", cfg, err)
	}
	out, _ = mustRun(t, b, "", app.ExitOK, "model", "status")
	if !strings.Contains(out, "model: jev-override") || !strings.Contains(out, "source: JEVKIT_MODEL") {
		t.Fatalf("environment status: %q", out)
	}
	mustRun(t, b, "", app.ExitOK, "model", "clear")
	if _, err := os.Stat(a.ModelPath()); !os.IsNotExist(err) {
		t.Fatalf("model file remains after clear: %v", err)
	}
	b.Environ = b.Environ[:len(b.Environ)-1]
	cfg, err = b.JevConfig()
	if err != nil || cfg.Model != jev.DefaultModel {
		t.Fatalf("cleared config = %+v, %v", cfg, err)
	}
}

func TestModelSelectionRejectsInvalidSetting(t *testing.T) {
	a := newApp(t)
	mustRun(t, a, "", app.ExitOK, "model", "set", "jev-good")
	for _, name := range []string{"", "bad model", "../bad", "jev\nother"} {
		if code, _, _ := run(a, "", "model", "set", name); code != app.ExitUsage {
			t.Errorf("set %q exit = %d", name, code)
		}
	}
	if got := testkit.ReadFile(t, a.ModelPath()); got != "jev-good\n" {
		t.Fatalf("invalid set replaced saved model: %q", got)
	}
	testkit.WriteFile(t, a.ModelPath(), "bad model\n")
	if code, _, _ := run(a, "", "model", "status"); code != app.ExitFail {
		t.Fatalf("invalid stored model status exit = %d", code)
	}
	if _, err := a.JevConfig(); err == nil {
		t.Fatal("invalid stored model accepted")
	}
	a.Environ = append(a.Environ, "JEVKIT_MODEL=jev-env")
	if cfg, err := a.JevConfig(); err != nil || cfg.Model != "jev-env" {
		t.Fatalf("environment override should bypass stored model: %+v, %v", cfg, err)
	}
	a.Environ[len(a.Environ)-1] = "JEVKIT_MODEL=bad model"
	if _, err := a.JevConfig(); err == nil {
		t.Fatal("invalid environment model accepted")
	}
}

func TestSavedModelReachesJevClient(t *testing.T) {
	a, _, fj := cliApp(t)
	mustRun(t, a, testkit.SecretKey+"\n", app.ExitOK, "key", "set")
	mustRun(t, a, "", app.ExitOK, "model", "set", "jev-1.13.0")
	var used string
	a.NewJev = func(cfg jev.Config, key func() (string, error)) app.Asker {
		used = cfg.Model
		return fj
	}
	fj.Resp = &jev.Response{Answers: map[string]jev.Answer{"answer": jev.NoulAnswer{Noul: 0.8}}}
	mustRun(t, a, "", app.ExitOK, "ask", "noul", "--state", "tests passed", "--question", "Did they pass?")
	if used != "jev-1.13.0" {
		t.Fatalf("Jev client model = %q", used)
	}
}
