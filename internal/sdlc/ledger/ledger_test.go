package ledger

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/JoshJancula/jevkit/internal/sdlc/engine"
)

func TestNewRunAndReadRun(t *testing.T) {
	root := t.TempDir()
	store := Open(root, "run-1")
	st := engine.State{Current: "write-spec", Status: engine.StatusRunning}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	r, err := NewRun(store, "run-1", "ship-feature", "deadbeef", st, now)
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	if r.CreatedAt != "2026-01-02T03:04:05Z" || r.UpdatedAt != r.CreatedAt {
		t.Errorf("timestamps = %+v", r)
	}

	got, err := store.ReadRun()
	if err != nil {
		t.Fatalf("ReadRun: %v", err)
	}
	if got.RunID != "run-1" || got.Workflow != "ship-feature" || got.GraphSHA256 != "deadbeef" || got.State.Current != "write-spec" {
		t.Fatalf("got %+v", got)
	}
}

func TestUpdateStateStampsUpdatedAt(t *testing.T) {
	root := t.TempDir()
	store := Open(root, "run-1")
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := NewRun(store, "run-1", "wf", "sha", engine.State{Status: engine.StatusRunning}, created); err != nil {
		t.Fatal(err)
	}

	updated := time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)
	newState := engine.State{Current: "implement", Status: engine.StatusRunning}
	r, err := store.UpdateState(newState, updated)
	if err != nil {
		t.Fatalf("UpdateState: %v", err)
	}
	if r.CreatedAt != "2026-01-01T00:00:00Z" {
		t.Errorf("CreatedAt should be preserved, got %s", r.CreatedAt)
	}
	if r.UpdatedAt != "2026-01-01T01:00:00Z" {
		t.Errorf("UpdatedAt = %s", r.UpdatedAt)
	}
	if r.State.Current != "implement" {
		t.Errorf("State not updated: %+v", r.State)
	}

	reread, err := store.ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if reread.State.Current != "implement" {
		t.Errorf("reread State = %+v", reread.State)
	}
}

func TestReadRunMissingIsAnError(t *testing.T) {
	store := Open(t.TempDir(), "run-1")
	if _, err := store.ReadRun(); err == nil {
		t.Fatal("expected an error reading a run that was never created")
	}
}

func TestNodeRecordLifecycle(t *testing.T) {
	store := Open(t.TempDir(), "run-1")

	rec, err := store.ReadNode("write-spec")
	if err != nil {
		t.Fatalf("ReadNode (absent): %v", err)
	}
	if rec.NodeID != "write-spec" || len(rec.Attempts) != 0 {
		t.Fatalf("expected an empty record for an unreached node, got %+v", rec)
	}

	if err := store.AppendAttempt("write-spec", AttemptRecord{Attempt: 1, StartedAt: "t1", Effect: json.RawMessage(`{"kind":"RunWork"}`)}); err != nil {
		t.Fatalf("AppendAttempt: %v", err)
	}
	if err := store.AppendAttempt("write-spec", AttemptRecord{Attempt: 1, ResolvedAt: "t2", Event: json.RawMessage(`{"kind":"WorkCompleted"}`)}); err != nil {
		t.Fatalf("AppendAttempt: %v", err)
	}

	rec, err = store.ReadNode("write-spec")
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Attempts) != 2 {
		t.Fatalf("len(Attempts) = %d, want 2", len(rec.Attempts))
	}
}

func TestReadAllNodesSortedAndEmptyWhenAbsent(t *testing.T) {
	store := Open(t.TempDir(), "run-1")

	empty, err := store.ReadAllNodes()
	if err != nil {
		t.Fatalf("ReadAllNodes (no nodes dir): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected no nodes, got %v", empty)
	}

	for _, id := range []string{"tests", "implement", "write-spec"} {
		if err := store.WriteNode(NodeRecord{NodeID: id}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := store.ReadAllNodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("len(all) = %d, want 3", len(all))
	}
	for _, id := range []string{"tests", "implement", "write-spec"} {
		if _, ok := all[id]; !ok {
			t.Errorf("missing node %q", id)
		}
	}
}

func TestNodePathRejectsPathTraversal(t *testing.T) {
	store := Open(t.TempDir(), "run-1")
	if err := store.WriteNode(NodeRecord{NodeID: "../escape"}); err == nil {
		t.Fatal("expected an error for a node id containing a path separator")
	}
	if _, err := store.ReadNode("a/b"); err == nil {
		t.Fatal("expected an error for a node id containing a path separator")
	}
}

// TestConcurrentUpdateStateNeverTearsTheFile runs N goroutines calling
// UpdateState simultaneously (run under -race) and requires run.json to
// always be one complete, valid JSON document afterward: a crash or a
// concurrent writer must never leave a reader with a partial file.
func TestConcurrentUpdateStateNeverTearsTheFile(t *testing.T) {
	root := t.TempDir()
	store := Open(root, "run-1")
	if _, err := NewRun(store, "run-1", "wf", "sha", engine.State{Status: engine.StatusRunning}, time.Now()); err != nil {
		t.Fatal(err)
	}

	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			st := engine.State{Current: "node", Status: engine.StatusRunning, ActiveSeconds: i}
			if _, err := store.UpdateState(st, time.Now()); err != nil {
				t.Errorf("UpdateState: %v", err)
			}
		}(i)
	}
	wg.Wait()

	r, err := store.ReadRun()
	if err != nil {
		t.Fatalf("run.json is not valid JSON after concurrent writers: %v", err)
	}
	if r.RunID != "run-1" {
		t.Errorf("got %+v", r)
	}
}

// TestNoLockFilesLeakedAsData confirms the .lock files writeAtomicJSON/Append
// create sit beside the data files rather than inside anything a reader like
// ReadAllNodes would misinterpret as a node record.
func TestNoLockFilesLeakedAsData(t *testing.T) {
	store := Open(t.TempDir(), "run-1")
	if err := store.WriteNode(NodeRecord{NodeID: "a"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(store.Dir, nodesDir))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 2 { // a.json + a.json.lock
		t.Fatalf("dir entries = %v", names)
	}
	all, err := store.ReadAllNodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("ReadAllNodes should only see a.json, got %v", all)
	}
}

func TestArtifactRoundTrip(t *testing.T) {
	store := Open(t.TempDir(), "run-1")
	if err := store.WriteArtifact("spec.md", []byte("scope: add rate limiting\n")); err != nil {
		t.Fatalf("WriteArtifact: %v", err)
	}
	got, err := store.ReadArtifact("spec.md")
	if err != nil {
		t.Fatalf("ReadArtifact: %v", err)
	}
	if string(got) != "scope: add rate limiting\n" {
		t.Errorf("got %q", got)
	}
}

func TestArtifactNestedPath(t *testing.T) {
	store := Open(t.TempDir(), "run-1")
	if err := store.WriteArtifact("docs/design.md", []byte("design")); err != nil {
		t.Fatalf("WriteArtifact: %v", err)
	}
	got, err := store.ReadArtifact("docs/design.md")
	if err != nil {
		t.Fatalf("ReadArtifact: %v", err)
	}
	if string(got) != "design" {
		t.Errorf("got %q", got)
	}
}

func TestArtifactPathTraversalRejected(t *testing.T) {
	store := Open(t.TempDir(), "run-1")
	for _, bad := range []string{"../escape.md", "../../etc/passwd", ""} {
		if err := store.WriteArtifact(bad, []byte("x")); err == nil {
			t.Errorf("WriteArtifact(%q) should have failed", bad)
		}
	}
}

// TestOpenRejectsRunIDPathTraversal covers runID itself, which frequently
// comes straight from a CLI positional argument (e.g. "jevkit sdlc resume
// <run-id>") with no upstream validation. Every Store method must fail
// closed rather than resolve outside root.
func TestOpenRejectsRunIDPathTraversal(t *testing.T) {
	root := t.TempDir()
	for _, bad := range []string{"..", ".", "../escape", "../../etc/passwd", "a/../../b", "/etc/passwd", ""} {
		store := Open(root, bad)
		if _, err := store.ReadRun(); err == nil {
			t.Errorf("ReadRun(%q): expected error", bad)
		}
		if err := store.WriteRun(Run{RunID: bad}); err == nil {
			t.Errorf("WriteRun(%q): expected error", bad)
		}
		if err := store.WriteArtifact("spec.md", []byte("x")); err == nil {
			t.Errorf("WriteArtifact(%q): expected error", bad)
		}
		if err := store.WithRunLock(func() error { return nil }); err == nil {
			t.Errorf("WithRunLock(%q): expected error", bad)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("root should be untouched by rejected run ids, got %v", entries)
	}
}

// TestOpenAllowsSiblingRunIDsAfterTraversalAttempt confirms the runID
// validation doesn't collaterally break a legitimate run whose ID happens
// to look similar (e.g. contains dots) but stays a single path component.
func TestOpenAllowsSiblingRunIDsAfterTraversalAttempt(t *testing.T) {
	root := t.TempDir()
	Open(root, "..") // rejected; must not disturb anything under root
	store := Open(root, "run-1.legit")
	if _, err := NewRun(store, "run-1.legit", "wf", "sha", engine.State{Status: engine.StatusRunning}, time.Now()); err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	if _, err := store.ReadRun(); err != nil {
		t.Fatalf("ReadRun: %v", err)
	}
}

// TestSafeMkdirAllRejectsPreexistingSymlink guards against a symlink placed
// at a run's directory path before the run is created: os.MkdirAll alone
// treats an existing symlink-to-a-directory as success and would let every
// subsequent write follow it outside root.
func TestSafeMkdirAllRejectsPreexistingSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "run-1")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	store := Open(root, "run-1")
	if err := store.WriteArtifact("spec.md", []byte("x")); err == nil {
		t.Fatal("WriteArtifact through a symlinked run directory should fail")
	}
	if _, err := os.Stat(filepath.Join(outside, "artifacts", "spec.md")); err == nil {
		t.Fatal("artifact must not have been written through the symlink target")
	}
}

// TestRunDirectoryPermissions confirms every directory Store creates is
// private (0700), matching every other jevkit state package.
func TestRunDirectoryPermissions(t *testing.T) {
	root := t.TempDir()
	store := Open(root, "run-1")
	if _, err := NewRun(store, "run-1", "wf", "sha", engine.State{Status: engine.StatusRunning}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteArtifact("spec.md", []byte("x")); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{store.Dir, filepath.Join(store.Dir, artifactsDir)} {
		fi, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if perm := fi.Mode().Perm(); runtime.GOOS != "windows" && perm != 0o700 {
			t.Errorf("%s perm = %o, want 0700", dir, perm)
		}
	}
}

func TestInvocationUsageKeepsOldLedgersReadableAndOmitsAbsentCache(t *testing.T) {
	root := t.TempDir()
	store := Open(root, "run-old")
	old := `{
  "runId": "run-old",
  "workflow": "ship-feature",
  "createdAt": "2026-01-02T03:04:05Z",
  "updatedAt": "2026-01-02T03:04:05Z",
  "state": {"current": "done", "status": "done"},
  "usage": [{
    "invocation": "inv-1",
    "agent": "builder",
    "runtime": "codex",
    "role": "implementer",
    "inputTokens": 40,
    "outputTokens": 5
  }]
}`
	if err := os.MkdirAll(store.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.Dir, "run.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := store.ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Usage) != 1 {
		t.Fatalf("usage entries: %d", len(got.Usage))
	}
	u := got.Usage[0]
	if u.InputTokens == nil || *u.InputTokens != 40 || u.OutputTokens == nil || *u.OutputTokens != 5 {
		t.Fatalf("tokens: %+v", u)
	}
	if u.CacheReadTokens != nil || u.CacheCreationTokens != nil || u.UsageProvenance != "" || u.CostUSD != nil {
		t.Fatalf("absent cache/cost/provenance must stay nil/empty: %+v", u)
	}

	in, out, read, create := int64(10), int64(2), int64(100), int64(0)
	cost := 0.01
	got.Usage = []InvocationUsage{{
		Invocation: "inv-2", Agent: "claude", Runtime: "claude", Role: "planner",
		InputTokens: &in, OutputTokens: &out, CacheReadTokens: &read, CacheCreationTokens: &create,
		CostUSD: &cost, UsageProvenance: "claude.result",
	}}
	if err := store.WriteRun(got); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(store.Dir, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var encoded map[string]any
	if err := json.Unmarshal(raw, &encoded); err != nil {
		t.Fatal(err)
	}
	usage := encoded["usage"].([]any)[0].(map[string]any)
	if usage["cacheReadTokens"] != float64(100) || usage["cacheCreationTokens"] != float64(0) || usage["usageProvenance"] != "claude.result" {
		t.Fatalf("encoded cache fields: %#v", usage)
	}
}
