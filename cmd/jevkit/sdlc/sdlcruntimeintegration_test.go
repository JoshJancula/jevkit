package sdlc

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/cmd/jevkit/util"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/sdlc/worker"
)

func TestSDLCIntegrationAsksEachRunUntilDefaultIsSelected(t *testing.T) {
	a := newApp(t)
	answers := []bool{true, true, true, false, true, true, true, false}
	prompts := 0
	a.Confirm = func(string) (bool, error) {
		prompts++
		answer := answers[0]
		answers = answers[1:]
		return answer, nil
	}
	for i := 0; i < 2; i++ {
		choice, err := a.chooseSDLCRuntimeIntegration(context.Background(), true)
		if err != nil || choice == nil || !choice.Hooks || !choice.Compaction || !choice.MCP {
			t.Fatalf("run %d choice: %+v, %v", i, choice, err)
		}
		store := ledger.Open(a.SDLCRunsDir(), "run-20260928T120000Z-abcd1234")
		if err := store.WriteRun(ledger.Run{RunID: "run-20260928T120000Z-abcd1234", RuntimeIntegration: choice}); err != nil {
			t.Fatal(err)
		}
		saved, err := store.ReadRun()
		if err != nil {
			t.Fatal(err)
		}
		var req worker.Request
		a.applySDLCRuntimeIntegration(&req, saved)
		if !req.JevkitHooks || !req.JevkitMCP || req.JevkitCompaction == nil || !*req.JevkitCompaction || req.JevkitBinary != "jevkit" {
			t.Fatalf("integration did not reach worker: %+v", req)
		}
	}
	if prompts != 8 {
		t.Fatalf("expected four questions per run, got %d", prompts)
	}
	if _, configured, err := a.loadSDLCRuntimeIntegration(); err != nil || configured {
		t.Fatalf("temporary choices became a default: configured=%v, err=%v", configured, err)
	}
	answers = []bool{true, false, true, true}
	choice, err := a.chooseSDLCRuntimeIntegration(context.Background(), true)
	if err != nil || choice == nil || !choice.Hooks || choice.Compaction {
		t.Fatalf("default choice: %+v, %v", choice, err)
	}
	if _, err := a.chooseSDLCRuntimeIntegration(context.Background(), true); err != nil || prompts != 12 {
		t.Fatalf("saved default should skip prompting: prompts=%d, err=%v", prompts, err)
	}
	path, err := a.sdlcRuntimeIntegrationPath()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 || strings.HasPrefix(path, a.WorkDir) {
		t.Fatalf("preference should be personal and private: %s, %v, %v", path, info, err)
	}
	other := a.clone()
	other.WorkDir = filepath.Join(filepath.Dir(a.WorkDir), "other")
	if _, configured, err := other.loadSDLCRuntimeIntegration(); err != nil || configured {
		t.Fatalf("choice leaked to another project: configured=%v, err=%v", configured, err)
	}
}

func TestSDLCRunCanDisablePreviouslyInstalledHooks(t *testing.T) {
	a := newApp(t)
	a.Environ = append(a.Environ, "JEVKIT_SDLC_RUN_ID=run-20260928T120000Z-abcd1234", "JEVKIT_SDLC_HOOKS=0")
	a.Stdin = strings.NewReader(`{}`)
	var output bytes.Buffer
	a.Stdout = &output
	if code := (&util.App{App: a.App}).Hook(context.Background(), "claude", "post-tool"); code != app.ExitOK {
		t.Fatalf("disabled hook exit: %d", code)
	}
	actual := output.String()
	output.Reset()
	if code := (&util.App{App: a.App}).RuntimePassthrough("claude", "post-tool"); code != app.ExitOK || output.String() != actual {
		t.Fatalf("disabled hook did not pass through: %d %q vs %q", code, actual, output.String())
	}
}

func TestSDLCStartSavesPerRunChoiceWithoutCreatingDefault(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	answers := []bool{true, false, false, false, false, false, false}
	prompts := 0
	a.Confirm = func(string) (bool, error) {
		prompts++
		answer := answers[0]
		answers = answers[1:]
		return answer, nil
	}
	for i := 0; i < 2; i++ {
		code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "Add a line")
		if code != app.ExitOK {
			t.Fatalf("start %d: %d %q %q", i, code, out, errs)
		}
		fields := strings.Fields(out)
		if len(fields) < 2 {
			t.Fatalf("start %d did not print run ID: %q", i, out)
		}
		saved, err := ledger.Open(a.SDLCRunsDir(), fields[1]).ReadRun()
		if err != nil || saved.RuntimeIntegration == nil || saved.RuntimeIntegration.Hooks != (i == 0) || saved.RuntimeIntegration.Compaction {
			t.Fatalf("run %d integration: %+v, %v", i, saved.RuntimeIntegration, err)
		}
	}
	if prompts != 7 {
		t.Fatalf("expected a prompt on each new run, got %d questions", prompts)
	}
	if _, configured, err := a.loadSDLCRuntimeIntegration(); err != nil || configured {
		t.Fatalf("temporary run choice persisted as default: %v, %v", configured, err)
	}
}

func TestSDLCIntegrationCommandConfiguresNoninteractiveRuns(t *testing.T) {
	a := newApp(t)
	initial, err := a.chooseSDLCRuntimeIntegration(context.Background(), false)
	if err != nil || initial == nil || initial.Hooks || initial.Compaction || initial.MCP {
		t.Fatalf("noninteractive run should use disabled integrations until configured: %+v, %v", initial, err)
	}
	code, out, errs := run(a, "", "sdlc", "integrations", "--hooks", "on", "--compaction", "on")
	if code != app.ExitOK || errs != "" || !strings.Contains(out, "Tool-output compaction │ on") {
		t.Fatalf("configure: %d %q %q", code, out, errs)
	}
	code, out, errs = run(a, "", "sdlc", "integrations", "--mcp", "on")
	if code != app.ExitOK || errs != "" || !strings.Contains(out, "MCP auto-install       │ on") {
		t.Fatalf("enable MCP: %d %q %q", code, out, errs)
	}
	code, _, errs = run(a, "", "sdlc", "integrations", "--hooks", "off")
	if code != app.ExitOK || errs != "" {
		t.Fatalf("disable: %d %q", code, errs)
	}
	choice, configured, err := a.loadSDLCRuntimeIntegration()
	if err != nil || !configured || choice.Hooks || choice.Compaction || !choice.MCP {
		t.Fatalf("disabled choice: %+v, %v, %v", choice, configured, err)
	}
	code, _, errs = run(a, "", "sdlc", "integrations", "--compaction", "on")
	if code == app.ExitOK || !strings.Contains(errs, "requires --hooks on") {
		t.Fatalf("compaction without hooks accepted: %d %q", code, errs)
	}
	code, out, errs = run(a, "", "sdlc", "integrations", "--ask-every-run")
	if code != app.ExitOK || errs != "" || !strings.Contains(out, "ask at each") {
		t.Fatalf("clear default: %d %q %q", code, out, errs)
	}
	if _, configured, err := a.loadSDLCRuntimeIntegration(); err != nil || configured {
		t.Fatalf("default remains after reset: configured=%v, err=%v", configured, err)
	}
}

func TestIntegrationShortcutsConfigureAndInspectTheSameDefaults(t *testing.T) {
	for _, shortcut := range [][]string{
		{"sdlc", "integrations"},
		{"sdlc", "i"},
		{"sdlc", "int"},
	} {
		t.Run(strings.Join(shortcut, "/"), func(t *testing.T) {
			a := newApp(t)
			code, out, errs := run(a, "", append(shortcut, "--hooks", "on", "--mcp", "on", "--compaction", "on")...)
			if code != app.ExitOK || errs != "" || !strings.Contains(out, "FEATURE") || !strings.Contains(out, "DEFAULT") {
				t.Fatalf("configure shortcut: %d %s %s", code, out, errs)
			}
			choice, saved, err := a.loadSDLCRuntimeIntegration()
			if err != nil || !saved || !choice.KeepDefault || !choice.Hooks || !choice.MCP || !choice.Compaction {
				t.Fatalf("shortcut did not persist preferences: %+v %t %v", choice, saved, err)
			}
			code, out, errs = run(a, "", "sdlc", "i")
			if code != app.ExitOK || errs != "" || !strings.Contains(out, "Tool-output compaction │ on") || !strings.Contains(out, "Per-agent tool permissions: jevkit sdlc agents c") {
				t.Fatalf("inspect shortcut: %d %s %s", code, out, errs)
			}
			code, out, errs = run(a, "", append(shortcut, "--ask-every-run")...)
			if code != app.ExitOK || errs != "" || !strings.Contains(out, "ask at each") {
				t.Fatalf("reset shortcut: %d %s %s", code, out, errs)
			}
			if _, saved, err := a.loadSDLCRuntimeIntegration(); err != nil || saved {
				t.Fatalf("shortcut did not clear defaults: %t %v", saved, err)
			}
		})
	}
}
