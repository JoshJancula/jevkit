package sdlc

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
)

func seedRun(t *testing.T, a *App, id, parent string, created time.Time) {
	t.Helper()
	st, err := adaptive.New("feature", "lean", 1, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	st.Stage, st.Outcome = "done", "merged"
	r := ledger.Run{
		RunID: id, ParentRunID: parent, Workflow: "feature", WorkDir: "/work/proj",
		Task: "fix the login bug for user@example.com", Adaptive: &st,
		CreatedAt: created.Format(time.RFC3339), UpdatedAt: created.Format(time.RFC3339),
	}
	store := ledger.Open(a.SDLCRunsDir(), id)
	if err := store.WriteRun(r); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteArtifact("plan.md", []byte("the plan")); err != nil {
		t.Fatal(err)
	}
}

func TestSdlcRunsInventoryListsStatusSizeAndChildren(t *testing.T) {
	a := newApp(t)
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	seedRun(t, a, "run-a", "", t0)
	seedRun(t, a, "run-b", "run-a", t0.Add(time.Minute))

	code, out, errb := run(a, "", "sdlc", "runs")
	if code != 0 || errb != "" {
		t.Fatalf("code=%d err=%q out=%s", code, errb, out)
	}
	for _, want := range []string{"run-a", "run-b", "done (merged)", "1", "Storage:", "TASK", "fix the login bug", "jevkit sdlc show RUN_ID"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "user@example.com") {
		t.Errorf("run list should redact task text:\n%s", out)
	}

	code, out, errb = run(a, "", "sdlc", "runs", "--format", "json")
	if code != 0 || errb != "" {
		t.Fatalf("json: code=%d err=%q", code, errb)
	}
	if !strings.Contains(out, `"runId": "run-a"`) || !strings.Contains(out, `"childRuns"`) {
		t.Errorf("json output missing expected fields:\n%s", out)
	}
}

func TestSdlcShowExplainsRunAndLinksEvidence(t *testing.T) {
	a := newApp(t)
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	seedRun(t, a, "run-a", "", t0)
	seedRun(t, a, "run-b", "run-a", t0.Add(time.Minute))
	for _, d := range []ledger.Decision{
		{At: t0.Add(time.Second).Format(time.RFC3339), RunID: "run-a", Kind: "invocation-outcome", Stage: "planning", Trigger: "planner", Choice: "planned", Outcome: "implementing", Invocation: "inv-plan"},
		{At: t0.Add(2 * time.Minute).Format(time.RFC3339), RunID: "run-b", Kind: "invocation-outcome", Stage: "assessing", Trigger: "reviewer", Choice: "approved", Outcome: "done", Invocation: "inv-review"},
	} {
		if err := ledger.Open(a.SDLCRunsDir(), d.RunID).AppendDecision(d); err != nil {
			t.Fatal(err)
		}
	}
	code, out, errb := run(a, "", "sdlc", "show", "run-a")
	if code != 0 || errb != "" {
		t.Fatalf("code=%d err=%q out=%s", code, errb, out)
	}
	for _, want := range []string{"WHAT HAPPENED", "planner: planned", "reviewer: approved", "jevkit sdlc logs run-a --invocation inv-plan", "jevkit sdlc logs run-b --invocation inv-review", filepath.Join(a.SDLCRunsDir(), "run-a", "artifacts", "plan.md"), "jevkit sdlc watch run-a"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestSdlcShowRedactsTaskByDefaultAndRawOptsIn(t *testing.T) {
	a := newApp(t)
	seedRun(t, a, "run-a", "", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))

	code, out, errb := run(a, "", "sdlc", "show", "run-a")
	if code != 0 || errb != "" {
		t.Fatalf("code=%d err=%q out=%s", code, errb, out)
	}
	if strings.Contains(out, "user@example.com") {
		t.Errorf("task should be redacted by default:\n%s", out)
	}
	for _, want := range []string{"run-a", "done (merged)", "plan.md", "/work/proj"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	code, out, errb = run(a, "", "sdlc", "show", "run-a", "--raw")
	if code != 0 || errb != "" {
		t.Fatalf("raw: code=%d err=%q", code, errb)
	}
	if !strings.Contains(out, "user@example.com") {
		t.Errorf("raw should show unredacted task:\n%s", out)
	}
}

func TestSdlcShowRejectsInvalidAndUnknownRunID(t *testing.T) {
	a := newApp(t)
	for _, bad := range []string{"..", ".", "../escape", "does-not-exist"} {
		if code, _, _ := run(a, "", "sdlc", "show", bad); code == 0 {
			t.Errorf("show %q: expected failure", bad)
		}
	}
}

func TestSdlcInventoryRefusesSymlinkedRunDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need elevated privileges on windows")
	}
	a := newApp(t)
	seedRun(t, a, "run-a", "", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))

	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(a.SDLCRunsDir(), "run-evil")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	infos, refused, err := a.sdlcInventory()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := infos["run-evil"]; ok {
		t.Error("symlinked entry should not appear as a run")
	}
	found := false
	for _, name := range refused {
		if name == "run-evil" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected run-evil in refused list, got %v", refused)
	}
	if len(infos) != 1 {
		t.Errorf("expected only run-a, got %v", infos)
	}

	if code, _, _ := run(a, "", "sdlc", "show", "run-evil"); code == 0 {
		t.Error("show on symlinked run directory should fail")
	}
}

func TestFormatBytes(t *testing.T) {
	cases := map[int64]string{0: "0 B", 999: "999 B", 1024: "1.0 KiB", 5 << 20: "5.0 MiB"}
	for n, want := range cases {
		if got := formatBytes(n); got != want {
			t.Errorf("formatBytes(%d) = %q, want %q", n, got, want)
		}
	}
}
