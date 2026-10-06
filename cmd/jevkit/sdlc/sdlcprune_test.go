package sdlc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JoshJancula/jevkit/cmd/jevkit/internal/testkit"
	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/usage"
)

// seedRunWithStage is seedRun (sdlcinventory_test.go) with a caller-chosen
// adaptive stage, so tests can control whether a run counts as active.
func seedRunWithStage(t *testing.T, a *App, id, parent, stage string, updated time.Time) {
	t.Helper()
	st, err := adaptive.New("feature", "lean", 1, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	st.Stage = stage
	if stage == adaptive.Done {
		st.Outcome = "merged"
	}
	r := ledger.Run{
		RunID: id, ParentRunID: parent, Workflow: "feature", WorkDir: "/work/proj",
		Task: "seed task", Adaptive: &st,
		CreatedAt: updated.Format(time.RFC3339), UpdatedAt: updated.Format(time.RFC3339),
	}
	store := ledger.Open(a.SDLCRunsDir(), id)
	if err := store.WriteRun(r); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteArtifact("plan.md", []byte("the plan")); err != nil {
		t.Fatal(err)
	}
}

func seedLog(t *testing.T, a *App, runID, invocation, agent, runtime, stdout, stderr string) {
	t.Helper()
	dir := filepath.Join(a.SDLCRunsDir(), runID, "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	meta := struct {
		Invocation string `json:"invocation"`
		Agent      string `json:"agent"`
		Runtime    string `json:"runtime"`
	}{invocation, agent, runtime}
	b, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	testkit.WriteFile(t, filepath.Join(dir, invocation+".json"), string(b))
	testkit.WriteFile(t, filepath.Join(dir, invocation+".stdout"), stdout)
	testkit.WriteFile(t, filepath.Join(dir, invocation+".stderr"), stderr)
}

func dirExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestSdlcDeletePreviewShowsActiveRunsAsBlocking(t *testing.T) {
	a := newApp(t)
	seedRunWithStage(t, a, "run-a", "", adaptive.Implementing, time.Now())

	code, out, errb := run(a, "", "sdlc", "delete", "run-a")
	if code != 0 || errb != "" {
		t.Fatalf("preview: code=%d err=%q out=%s", code, errb, out)
	}
	if !strings.Contains(out, "blocks delete") {
		t.Errorf("expected active run flagged as blocking, got:\n%s", out)
	}
	if !dirExists(filepath.Join(a.SDLCRunsDir(), "run-a")) {
		t.Error("preview must not delete anything")
	}
}

func TestSdlcDeleteRefusesActiveRun(t *testing.T) {
	a := newApp(t)
	seedRunWithStage(t, a, "run-a", "", adaptive.Assessing, time.Now())

	code, _, errb := run(a, "", "sdlc", "delete", "run-a", "--apply")
	if code == 0 {
		t.Fatal("expected --apply to refuse an active run")
	}
	if !strings.Contains(errb, "active") {
		t.Errorf("expected refusal to mention active run, got %q", errb)
	}
	if !dirExists(filepath.Join(a.SDLCRunsDir(), "run-a")) {
		t.Error("active run must not be deleted")
	}
}

func TestSdlcDeleteRefusesWholeTreeIfChildIsActive(t *testing.T) {
	a := newApp(t)
	seedRunWithStage(t, a, "run-a", "", adaptive.Done, time.Now())
	seedRunWithStage(t, a, "run-b", "run-a", adaptive.Implementing, time.Now())

	code, _, errb := run(a, "", "sdlc", "delete", "run-a", "--apply")
	if code == 0 {
		t.Fatal("expected refusal: child run-b is active")
	}
	if !strings.Contains(errb, "run-b") {
		t.Errorf("expected error to name the active child, got %q", errb)
	}
	if !dirExists(filepath.Join(a.SDLCRunsDir(), "run-a")) || !dirExists(filepath.Join(a.SDLCRunsDir(), "run-b")) {
		t.Error("neither run should be deleted when the tree is refused")
	}
}

func TestSdlcDeleteRemovesInactiveTreeAndAttributableUsagePreservesShared(t *testing.T) {
	a := newApp(t)
	seedRunWithStage(t, a, "run-a", "", adaptive.Done, time.Now())
	seedRunWithStage(t, a, "run-b", "run-a", adaptive.Paused, time.Now())
	seedRunWithStage(t, a, "run-c", "", adaptive.Done, time.Now())

	for _, rec := range []usage.Record{
		{RunID: "run-a", InputTokens: 10, UsageSource: usage.SourceMeasured},
		{RunID: "run-b", InputTokens: 20, UsageSource: usage.SourceMeasured},
		{RunID: "run-c", InputTokens: 30, UsageSource: usage.SourceMeasured},
		{RunID: "", InputTokens: 40, UsageSource: usage.SourceMeasured}, // unattributable
	} {
		if err := usage.Append(a.StateHome(), rec); err != nil {
			t.Fatal(err)
		}
	}

	code, out, errb := run(a, "", "sdlc", "delete", "run-a", "--apply")
	if code != 0 || errb != "" {
		t.Fatalf("code=%d err=%q out=%s", code, errb, out)
	}
	if dirExists(filepath.Join(a.SDLCRunsDir(), "run-a")) || dirExists(filepath.Join(a.SDLCRunsDir(), "run-b")) {
		t.Error("run-a and its child run-b should be deleted")
	}
	if !dirExists(filepath.Join(a.SDLCRunsDir(), "run-c")) {
		t.Error("run-c is outside the deleted tree and must survive")
	}

	recs, err := usage.ReadRecords(usage.Path(a.StateHome()))
	if err != nil {
		t.Fatal(err)
	}
	var runIDs []string
	for _, r := range recs {
		runIDs = append(runIDs, r.RunID)
	}
	if len(recs) != 2 {
		t.Fatalf("expected 2 surviving usage records (run-c + unattributable), got %v", runIDs)
	}
	for _, id := range runIDs {
		if id == "run-a" || id == "run-b" {
			t.Errorf("attributable usage record for deleted run %q should have been removed", id)
		}
	}
}

func TestSdlcDeleteIdempotentSecondCallErrorsCleanly(t *testing.T) {
	a := newApp(t)
	seedRunWithStage(t, a, "run-a", "", adaptive.Done, time.Now())

	if code, _, errb := run(a, "", "sdlc", "delete", "run-a", "--apply"); code != 0 || errb != "" {
		t.Fatalf("first delete: code=%d err=%q", code, errb)
	}
	code, _, errb := run(a, "", "sdlc", "delete", "run-a", "--apply")
	if code == 0 {
		t.Fatal("second delete of an already-deleted run should fail cleanly, not succeed silently")
	}
	if strings.Contains(errb, "panic") {
		t.Errorf("second delete crashed: %s", errb)
	}
}

func TestSdlcDeleteRefusesSymlinkedRunDirectory(t *testing.T) {
	a := newApp(t)
	outside := t.TempDir()
	if err := os.MkdirAll(a.SDLCRunsDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(a.SDLCRunsDir(), "run-evil")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if code, _, _ := run(a, "", "sdlc", "delete", "run-evil", "--apply"); code == 0 {
		t.Error("delete on a symlinked run directory should fail")
	}
	if _, err := os.Lstat(outside); err != nil {
		t.Errorf("symlink target must survive: %v", err)
	}
}

func TestSdlcDeleteApplyRechecksStatusUnderLockBeforeDeleting(t *testing.T) {
	a := newApp(t)
	seedRunWithStage(t, a, "run-a", "", adaptive.Paused, time.Now())

	scope, err := a.sdlcTree("run-a")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a race: the run resumed and became active after the scope was
	// built (e.g. between an operator's preview call and their --apply call).
	store := ledger.Open(a.SDLCRunsDir(), "run-a")
	fresh, err := store.ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	fresh.Adaptive.Stage = adaptive.Implementing
	if err := store.WriteRun(fresh); err != nil {
		t.Fatal(err)
	}

	if _, _, err := a.sdlcDeleteApply(scope); err == nil {
		t.Fatal("expected the recheck under lock to refuse a now-active run")
	}
	if !dirExists(filepath.Join(a.SDLCRunsDir(), "run-a")) {
		t.Error("run must survive when the recheck refuses it")
	}
}

func TestSdlcPruneOlderThanAndStatusSelectors(t *testing.T) {
	a := newApp(t)
	old := time.Now().Add(-30 * 24 * time.Hour)
	recent := time.Now()
	seedRunWithStage(t, a, "run-old-done", "", adaptive.Done, old)
	seedRunWithStage(t, a, "run-old-paused", "", adaptive.Paused, old)
	seedRunWithStage(t, a, "run-recent-done", "", adaptive.Done, recent)

	code, out, errb := run(a, "", "sdlc", "prune", "--older-than", "720h", "--status", "done", "--apply")
	if code != 0 || errb != "" {
		t.Fatalf("code=%d err=%q out=%s", code, errb, out)
	}
	if dirExists(filepath.Join(a.SDLCRunsDir(), "run-old-done")) {
		t.Error("run-old-done matches age+status and should be pruned")
	}
	if !dirExists(filepath.Join(a.SDLCRunsDir(), "run-old-paused")) {
		t.Error("run-old-paused has the wrong status and must survive")
	}
	if !dirExists(filepath.Join(a.SDLCRunsDir(), "run-recent-done")) {
		t.Error("run-recent-done is too recent and must survive")
	}
}

func TestSdlcPruneDefaultIsPreviewOnly(t *testing.T) {
	a := newApp(t)
	seedRunWithStage(t, a, "run-a", "", adaptive.Done, time.Now().Add(-1000*time.Hour))

	code, out, errb := run(a, "", "sdlc", "prune", "--older-than", "1h")
	if code != 0 || errb != "" {
		t.Fatalf("code=%d err=%q out=%s", code, errb, out)
	}
	if !strings.Contains(out, "preview") && !strings.Contains(out, "WOULD DELETE") {
		t.Errorf("expected a preview label, got:\n%s", out)
	}
	if !dirExists(filepath.Join(a.SDLCRunsDir(), "run-a")) {
		t.Error("prune without --apply must not delete anything")
	}
}

func TestSdlcPruneLogsOnlyMarksPrunedKeepsPlanArtifactsAndUsage(t *testing.T) {
	a := newApp(t)
	old := time.Now().Add(-1000 * time.Hour)
	seedRunWithStage(t, a, "run-a", "", adaptive.Done, old)
	seedLog(t, a, "run-a", "inv-1", "agent-x", "claude", "hello stdout", "hello stderr")
	if err := usage.Append(a.StateHome(), usage.Record{RunID: "run-a", InputTokens: 5, UsageSource: usage.SourceMeasured}); err != nil {
		t.Fatal(err)
	}

	code, out, errb := run(a, "", "sdlc", "prune", "--logs-only", "--older-than", "1h", "--apply")
	if code != 0 || errb != "" {
		t.Fatalf("code=%d err=%q out=%s", code, errb, out)
	}

	logsDir := filepath.Join(a.SDLCRunsDir(), "run-a", "logs")
	if dirExists(filepath.Join(logsDir, "inv-1.stdout")) || dirExists(filepath.Join(logsDir, "inv-1.stderr")) {
		t.Error("diagnostic streams should have been removed")
	}
	if !dirExists(filepath.Join(logsDir, "inv-1.json")) {
		t.Error("invocation metadata must survive logs-only pruning")
	}
	if !dirExists(filepath.Join(logsDir, "inv-1.pruned")) {
		t.Error("expected a pruned marker for the invocation")
	}
	if !dirExists(filepath.Join(a.SDLCRunsDir(), "run-a", "artifacts", "plan.md")) {
		t.Error("artifacts (plans/receipts) must survive logs-only pruning")
	}
	if !dirExists(filepath.Join(a.SDLCRunsDir(), "run-a", "run.json")) {
		t.Error("run status must survive logs-only pruning")
	}
	recs, err := usage.ReadRecords(usage.Path(a.StateHome()))
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Errorf("usage records must survive logs-only pruning, got %d", len(recs))
	}

	code, showOut, errb := run(a, "", "sdlc", "show", "run-a")
	if code != 0 || errb != "" {
		t.Fatalf("show: code=%d err=%q", code, errb)
	}
	if !strings.Contains(showOut, "pruned") {
		t.Errorf("sdlc show should report the invocation as pruned, not missing:\n%s", showOut)
	}
	if strings.Contains(showOut, "none saved") {
		t.Errorf("a pruned invocation must never be presented as logs that were never captured:\n%s", showOut)
	}
}

func TestSdlcPruneLogsOnlyRemovesVerificationLogsKeepsReceipts(t *testing.T) {
	a := newApp(t)
	old := time.Now().Add(-1000 * time.Hour)
	seedRunWithStage(t, a, "run-a", "", adaptive.Done, old)
	vdir := filepath.Join(a.SDLCRunsDir(), "run-a", "logs", "verification")
	if err := os.MkdirAll(vdir, 0o700); err != nil {
		t.Fatal(err)
	}
	testkit.WriteFile(t, filepath.Join(vdir, "unit.json"), `{"checkId":"unit"}`)
	testkit.WriteFile(t, filepath.Join(vdir, "unit.log"), "check output")
	store := ledger.Open(a.SDLCRunsDir(), "run-a")
	if err := store.WriteArtifact(adaptive.ArtifactVerification, []byte(`{"status":"passed"}`)); err != nil {
		t.Fatal(err)
	}

	code, _, errb := run(a, "", "sdlc", "prune", "--logs-only", "--older-than", "1h", "--apply")
	if code != 0 || errb != "" {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	if dirExists(filepath.Join(vdir, "unit.log")) {
		t.Error("verification log stream should be pruned")
	}
	if !dirExists(filepath.Join(vdir, "unit.json")) {
		t.Error("verification meta should survive logs-only prune")
	}
	if !dirExists(filepath.Join(vdir, "verification.pruned")) {
		t.Error("expected verification pruned marker")
	}
	if !dirExists(filepath.Join(a.SDLCRunsDir(), "run-a", "artifacts", "verification", "receipts.json")) {
		t.Error("verification receipts must survive logs-only pruning")
	}
}
