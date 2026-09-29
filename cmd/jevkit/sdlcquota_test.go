package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/sdlc/worker"
)

// bigArtifact writes an artifact of n bytes so a run's dirSize reaches a
// target for quota tests, without depending on any real invocation output.
func bigArtifact(t *testing.T, a *App, runID, name string, n int) {
	t.Helper()
	path := filepath.Join(a.sdlcRunsDir(), runID, "artifacts", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, n), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSdlcQuotaCheckRunTreeExceededPausesWithUsageLimitAndCommands(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	a.Environ = append(a.Environ, "JEVKIT_SDLC_RUN_TREE_QUOTA_BYTES=1000")
	f := &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "planned", Content: "Plan"}, {Outcome: "changed", Content: "diff --git a/a b/a\n+line\n"}}}
	a.SdlcExecutor = f
	code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "add line", "--auto")
	if code != exitOK {
		t.Fatalf("start: %d %s %s", code, out, errs)
	}
	id := strings.Fields(out)[1]
	bigArtifact(t, a, id, "bloat.bin", 2000)
	code, _, errs = run(a, "", "sdlc", "drive", id)
	if code == exitOK {
		t.Fatalf("expected quota pause, drive succeeded: %s", errs)
	}
	if !strings.Contains(errs, "run tree") || !strings.Contains(errs, "storage quota") ||
		!strings.Contains(errs, "jevkit sdlc show "+id) || !strings.Contains(errs, "jevkit sdlc prune") ||
		!strings.Contains(errs, "jevkit sdlc resume "+id+" --retry-failed") {
		t.Fatalf("expected usage/limit/commands in pause message: %q", errs)
	}
	stored, err := ledger.Open(a.sdlcRunsDir(), id).ReadRun()
	if err != nil || stored.Adaptive.Stage != adaptive.Paused {
		t.Fatalf("expected paused run: %+v %v", stored.Adaptive, err)
	}
	if len(f.requests) != 0 {
		t.Fatalf("no invocation should have been launched once quota was exceeded: %d", len(f.requests))
	}

	// Freeing space (here: raising the quota, standing in for a human
	// pruning) lets a plain --retry-failed resume proceed with the same
	// reserved assignment; jevkit never deleted anything on its own.
	for i := range a.Environ {
		if strings.HasPrefix(a.Environ[i], "JEVKIT_SDLC_RUN_TREE_QUOTA_BYTES=") {
			a.Environ[i] = "JEVKIT_SDLC_RUN_TREE_QUOTA_BYTES=100000000"
		}
	}
	code, _, errs = run(a, "", "sdlc", "resume", id, "--retry-failed", "--step")
	if code != exitOK {
		t.Fatalf("retry after freeing space: %d %s", code, errs)
	}
	if len(f.requests) != 1 {
		t.Fatalf("expected exactly one invocation after quota cleared: %d", len(f.requests))
	}
}

func TestSdlcQuotaCheckTotalStateExceededPausesWithUsageLimitAndCommands(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	a.Environ = append(a.Environ, "JEVKIT_SDLC_STORAGE_QUOTA_BYTES=1000")
	f := &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "planned", Content: "Plan"}}}
	a.SdlcExecutor = f
	code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "add line", "--auto")
	if code != exitOK {
		t.Fatalf("start: %d %s %s", code, out, errs)
	}
	id := strings.Fields(out)[1]
	bigArtifact(t, a, id, "bloat.bin", 2000)
	code, _, errs = run(a, "", "sdlc", "drive", id)
	if code == exitOK {
		t.Fatalf("expected quota pause, drive succeeded: %s", errs)
	}
	if !strings.Contains(errs, "total storage quota") || !strings.Contains(errs, "jevkit sdlc runs") ||
		!strings.Contains(errs, "jevkit sdlc prune") {
		t.Fatalf("expected usage/limit/commands in pause message: %q", errs)
	}
}

func TestSdlcQuotaExceededResolvedByDeletingAnotherRunFreesSpaceForRetry(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	a.Environ = append(a.Environ, "JEVKIT_SDLC_STORAGE_QUOTA_BYTES=20000")
	f := &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "planned", Content: "Plan"}, {Outcome: "changed", Content: "diff --git a/a b/a\n+line\n"}}}
	a.SdlcExecutor = f
	code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "add line", "--auto")
	if code != exitOK {
		t.Fatalf("start: %d %s %s", code, out, errs)
	}
	id := strings.Fields(out)[1]

	// A second, finished, unrelated run is the one over the shared total
	// state quota; the paused run's own tree stays small.
	other := "other-finished-run"
	otherState, err := adaptive.New("feature", "lean", 1, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	otherState.Stage = adaptive.Done
	now := a.now().UTC().Format("2006-01-02T15:04:05Z07:00")
	if err := ledger.Open(a.sdlcRunsDir(), other).WriteRun(ledger.Run{RunID: other, CreatedAt: now, UpdatedAt: now, Adaptive: &otherState}); err != nil {
		t.Fatal(err)
	}
	bigArtifact(t, a, other, "bloat.bin", 50000)

	code, _, errs = run(a, "", "sdlc", "drive", id)
	if code == exitOK {
		t.Fatalf("expected total-quota pause, drive succeeded: %s", errs)
	}
	if !strings.Contains(errs, "total storage quota") {
		t.Fatalf("expected total quota pause: %q", errs)
	}

	// Deleting the unrelated finished run (never the paused one) is what
	// actually frees space, exactly the exact command the pause message
	// names; jevkit never removed it on its own.
	code, out, errs = run(a, "", "sdlc", "delete", other, "--apply")
	if code != exitOK || !strings.Contains(out, "DELETED") {
		t.Fatalf("delete other run: %d %q %q", code, out, errs)
	}
	if _, err := os.Stat(filepath.Join(a.sdlcRunsDir(), other)); !os.IsNotExist(err) {
		t.Fatalf("expected other run directory to be gone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(a.sdlcRunsDir(), id)); err != nil {
		t.Fatalf("the originally paused run must never be touched by deleting a different run: %v", err)
	}

	code, _, errs = run(a, "", "sdlc", "resume", id, "--retry-failed", "--step")
	if code != exitOK {
		t.Fatalf("retry after deleting the other run: %d %s", code, errs)
	}
	if len(f.requests) != 1 {
		t.Fatalf("expected exactly one invocation once quota cleared: %d", len(f.requests))
	}
}

func TestSdlcQuotaCheckCountsChildRunsInRunTree(t *testing.T) {
	a := newApp(t)
	now := a.now().UTC().Format("2006-01-02T15:04:05Z07:00")
	rootID, childID := "root-run", "child-run"
	rootState, err := adaptive.New("feature", "lean", 1, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	childState, err := adaptive.New("feature", "lean", 1, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Open(a.sdlcRunsDir(), rootID).WriteRun(ledger.Run{RunID: rootID, CreatedAt: now, UpdatedAt: now, Adaptive: &rootState}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Open(a.sdlcRunsDir(), childID).WriteRun(ledger.Run{RunID: childID, ParentRunID: rootID, CreatedAt: now, UpdatedAt: now, Adaptive: &childState}); err != nil {
		t.Fatal(err)
	}
	// Neither run alone is over quota, but the child's bulk pushes the whole
	// tree (root + child) over a quota set between the two.
	bigArtifact(t, a, childID, "bloat.bin", 800)
	a.Environ = append(a.Environ, "JEVKIT_SDLC_RUN_TREE_QUOTA_BYTES=500")
	if err := a.sdlcQuotaCheck(rootID); err == nil {
		t.Fatalf("expected root's quota check to see child usage")
	}
	if err := a.sdlcQuotaCheck(childID); err == nil {
		t.Fatalf("expected child's quota check to walk up to the root tree total")
	} else if !strings.Contains(err.Error(), rootID) {
		t.Fatalf("expected the error to name the tree root %s: %v", rootID, err)
	}
}

func TestSdlcShowDisplaysOmittedByteCountAfterTailRollover(t *testing.T) {
	a := newApp(t)
	now := a.now().UTC().Format("2006-01-02T15:04:05Z07:00")
	id := "rolled-over"
	st, err := adaptive.New("feature", "lean", 1, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Open(a.sdlcRunsDir(), id).WriteRun(ledger.Run{RunID: id, CreatedAt: now, UpdatedAt: now, Adaptive: &st}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(a.sdlcRunsDir(), id, "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "inv.json"), []byte(`{"invocation":"inv","agent":"planner","runtime":"codex"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// Simulate exactly what worker.log.go produces on rollover: the marker
	// embedded directly in the raw stream, and a byte-count sidecar for the
	// combined lines journal.
	marker := worker.TruncationMarkerPrefix + " 4096 bytes omitted]\n"
	if err := os.WriteFile(filepath.Join(dir, "inv.stdout"), []byte(marker+"tail kept\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "inv.lines.jsonl"), []byte(`{"at":1,"stream":"stdout","text":"tail kept"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "inv.lines.jsonl.truncated"), []byte("2048\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errs := run(a, "", "sdlc", "show", id)
	if code != exitOK || !strings.Contains(out, "truncated: 2048 bytes omitted") {
		t.Fatalf("show: %d %q %q", code, out, errs)
	}
	code, out, errs = run(a, "", "sdlc", "logs", id, "--raw", "--stream", "stdout")
	if code != exitOK || !strings.Contains(out, "4096 bytes omitted") {
		t.Fatalf("raw logs should surface the embedded marker unchanged: %d %q %q", code, out, errs)
	}
	code, out, errs = run(a, "", "sdlc", "logs", id, "--stream", "stdout")
	if code != exitOK || !strings.Contains(out, "4096 bytes omitted") {
		t.Fatalf("normal (redacted) logs must still show the truncation marker: %d %q %q", code, out, errs)
	}
}
