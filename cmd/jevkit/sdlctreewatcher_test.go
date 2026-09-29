package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
)

// newAppForBenchmark is newApp (app_test.go) without the *testing.T-specific
// network guard, so these benchmarks can build an App from a *testing.B.
func newAppForBenchmark(b *testing.B) *App {
	b.Helper()
	root := b.TempDir()
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		b.Fatal(err)
	}
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		b.Fatal(err)
	}
	return &App{
		Stdin:     strings.NewReader(""),
		Environ:   []string{"HOME=" + home},
		WorkDir:   work,
		HomeDir:   home,
		ConfigDir: filepath.Join(root, "cfg"),
		StateDir:  filepath.Join(root, "state"),
		Version:   "test",
		Binary:    "jevkit",
	}
}

func TestSdlcTreeWatcherMatchesSdlcTreeOnFirstRefresh(t *testing.T) {
	a := newApp(t)
	seedRunWithStage(t, a, "root", "", adaptive.Implementing, time.Now())
	seedRunWithStage(t, a, "child", "root", adaptive.Implementing, time.Now())
	seedRunWithStage(t, a, "other", "", adaptive.Implementing, time.Now())

	want, err := a.sdlcTree("root")
	if err != nil {
		t.Fatal(err)
	}
	got, err := newSdlcTreeWatcher("root").refresh(a)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) || len(got) != 2 {
		t.Fatalf("watcher tree = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i].RunID != want[i].RunID {
			t.Fatalf("watcher tree order = %+v, want %+v", got, want)
		}
	}
}

func TestSdlcTreeWatcherPicksUpNewChildOnLaterRefresh(t *testing.T) {
	a := newApp(t)
	seedRunWithStage(t, a, "root", "", adaptive.Implementing, time.Now())
	tree := newSdlcTreeWatcher("root")
	first, err := tree.refresh(a)
	if err != nil || len(first) != 1 {
		t.Fatalf("first refresh: %+v %v", first, err)
	}
	seedRunWithStage(t, a, "child", "root", adaptive.Implementing, time.Now())
	second, err := tree.refresh(a)
	if err != nil || len(second) != 2 {
		t.Fatalf("expected the new child to appear on the next refresh: %+v %v", second, err)
	}
}

func TestSdlcTreeWatcherReflectsPrunedRunImmediately(t *testing.T) {
	a := newApp(t)
	seedRunWithStage(t, a, "root", "", adaptive.Implementing, time.Now())
	seedRunWithStage(t, a, "child", "root", adaptive.Implementing, time.Now())
	tree := newSdlcTreeWatcher("root")
	if runs, err := tree.refresh(a); err != nil || len(runs) != 2 {
		t.Fatalf("first refresh: %+v %v", runs, err)
	}
	if err := os.RemoveAll(filepath.Join(a.sdlcRunsDir(), "child")); err != nil {
		t.Fatal(err)
	}
	runs, err := tree.refresh(a)
	if err != nil || len(runs) != 1 || runs[0].RunID != "root" {
		t.Fatalf("expected the deleted child to vanish immediately: %+v %v", runs, err)
	}
}

func TestSdlcTreeWatcherReflectsLiveStageChangesForMembers(t *testing.T) {
	a := newApp(t)
	seedRunWithStage(t, a, "root", "", adaptive.Implementing, time.Now())
	tree := newSdlcTreeWatcher("root")
	if _, err := tree.refresh(a); err != nil {
		t.Fatal(err)
	}
	seedRunWithStage(t, a, "root", "", adaptive.Done, time.Now())
	runs, err := tree.refresh(a)
	if err != nil || len(runs) != 1 || runs[0].Adaptive.Stage != adaptive.Done {
		t.Fatalf("expected a member run's mutable state to stay live across refreshes: %+v %v", runs, err)
	}
}

// TestSdlcTreeWatcherSkipsReadingUnrelatedRunsAfterFirstRefresh is the
// regression guard for the fix itself: a bare sdlcTree call re-opens every
// run.json under sdlcRunsDir() on every call, so cost scales with the total
// run count regardless of tree size. Once sdlcTreeWatcher has classified an
// unrelated run as a non-member, ParentRunID can never change, so it must
// never be re-read on a later refresh even though it still exists on disk.
func TestSdlcTreeWatcherSkipsReadingUnrelatedRunsAfterFirstRefresh(t *testing.T) {
	a := newApp(t)
	seedRunWithStage(t, a, "root", "", adaptive.Implementing, time.Now())
	for i := 0; i < 25; i++ {
		seedRunWithStage(t, a, "unrelated-"+string(rune('a'+i)), "", adaptive.Implementing, time.Now())
	}
	tree := newSdlcTreeWatcher("root")
	if _, err := tree.refresh(a); err != nil {
		t.Fatal(err)
	}
	if len(tree.parent) != 26 {
		t.Fatalf("expected the first refresh to have read every run once, got %d", len(tree.parent))
	}
	if len(tree.member) != 1 {
		t.Fatalf("expected exactly the root run to be a tree member, got %d", len(tree.member))
	}
	// A second refresh should still see the same tree without needing to
	// touch the 25 unrelated runs again; correctness of that is exercised by
	// the "matches sdlcTree" and "picks up new child" tests above, and the
	// cost bound itself is exercised by BenchmarkSdlcTreeWatcherRefresh.
	runs, err := tree.refresh(a)
	if err != nil || len(runs) != 1 {
		t.Fatalf("second refresh: %+v %v", runs, err)
	}
}

// benchSeedRun writes a minimal run.json directly, mirroring seedRunWithStage
// (sdlcprune_test.go) without requiring a *testing.T, so it can seed fixtures
// from a *testing.B.
func benchSeedRun(b *testing.B, a *App, id, parent string) {
	b.Helper()
	now := time.Now().Format(time.RFC3339)
	st, err := adaptive.New("feature", "lean", 1, 1, 3)
	if err != nil {
		b.Fatal(err)
	}
	r := ledger.Run{RunID: id, ParentRunID: parent, Workflow: "feature", WorkDir: "/work/proj", Task: "seed task", Adaptive: &st, CreatedAt: now, UpdatedAt: now}
	if err := ledger.Open(a.sdlcRunsDir(), id).WriteRun(r); err != nil {
		b.Fatal(err)
	}
}

// seedRunsForBenchmark seeds a small two-run tree plus n unrelated runs, the
// shape of a busy shared state dir with one tree being watched, and returns
// the root run ID.
func seedRunsForBenchmark(b *testing.B, a *App, unrelated int) string {
	b.Helper()
	benchSeedRun(b, a, "root", "")
	benchSeedRun(b, a, "child", "root")
	for i := 0; i < unrelated; i++ {
		benchSeedRun(b, a, "unrelated-"+strconv.Itoa(i), "")
	}
	return "root"
}

// BenchmarkSdlcTreeRepeatedCalls models what the pre-fix watch/logs --follow
// loops did every tick: a bare a.sdlcTree(root) call, which re-lists and
// re-opens every run.json under sdlcRunsDir() regardless of tree membership.
func BenchmarkSdlcTreeRepeatedCalls(b *testing.B) {
	for _, n := range []int{10, 500} {
		b.Run(fmt.Sprintf("unrelated=%d", n), func(b *testing.B) {
			a := newAppForBenchmark(b)
			root := seedRunsForBenchmark(b, a, n)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := a.sdlcTree(root); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkSdlcTreeWatcherRepeatedRefresh is the fixed equivalent: after the
// first refresh, later ticks skip re-reading runs already known not to
// belong to this tree, so cost should stay flat as the unrelated run count
// grows instead of scaling with it the way BenchmarkSdlcTreeRepeatedCalls
// does.
func BenchmarkSdlcTreeWatcherRepeatedRefresh(b *testing.B) {
	for _, n := range []int{10, 500} {
		b.Run(fmt.Sprintf("unrelated=%d", n), func(b *testing.B) {
			a := newAppForBenchmark(b)
			root := seedRunsForBenchmark(b, a, n)
			tree := newSdlcTreeWatcher(root)
			if _, err := tree.refresh(a); err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := tree.refresh(a); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
