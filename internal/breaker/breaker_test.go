package breaker

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/OWNER/jevkit/internal/jev"
)

var _ jev.Breaker = (*Breaker)(nil)

const helperEnv = "BREAKER_TEST_HELPER_DIR"

func TestMain(m *testing.M) {
	if dir := os.Getenv(helperEnv); dir != "" {
		b := New(dir)
		for i := 0; i < 30; i++ {
			switch os.Getenv("BREAKER_TEST_OP") {
			case "success":
				b.RecordSuccess()
			default:
				b.IsOpen()
				b.RecordSuccess()
			}
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestStartsClosed(t *testing.T) {
	b := New(t.TempDir())
	if b.IsOpen() {
		t.Fatal("new breaker open")
	}
	if _, err := os.Stat(b.Path()); err == nil {
		t.Fatal("reads must not create the file")
	}
}

func TestTwoConsecutiveFailuresOpen(t *testing.T) {
	b := New(t.TempDir())
	b.RecordFailure("http-500")
	if b.IsOpen() {
		t.Fatal("opened after one failure")
	}
	b.RecordFailure("timeout")
	if !b.IsOpen() {
		t.Fatal("not open after two")
	}
	if s := b.Load(); s.Reason != "timeout" || s.OpenedAt.IsZero() {
		t.Fatalf("%+v", s)
	}
}

func TestSuccessResetsCounterWhileClosed(t *testing.T) {
	b := New(t.TempDir())
	b.RecordFailure("x")
	b.RecordSuccess()
	b.RecordFailure("x")
	if b.IsOpen() {
		t.Fatal("success did not reset the counter")
	}
}

func TestImmediateOpenOn401And422(t *testing.T) {
	for _, reason := range []string{"http-401", "http-422"} {
		b := New(t.TempDir())
		b.Open(reason)
		if !b.IsOpen() || b.Load().Reason != reason {
			t.Fatalf("%s: %+v", reason, b.Load())
		}
	}
}

func TestOpenIsStickyAndKeepsFirstReason(t *testing.T) {
	b := New(t.TempDir())
	b.Open("http-401")
	first := b.Load()
	b.Open("http-422")
	b.RecordFailure("x")
	b.RecordSuccess()
	if got := b.Load(); got != first || !b.IsOpen() {
		t.Fatalf("state changed once open: %+v vs %+v", got, first)
	}
}

func TestResetCloses(t *testing.T) {
	b := New(t.TempDir())
	b.Open("http-401")
	b.Reset()
	if b.IsOpen() || b.Load().Consecutive != 0 {
		t.Fatalf("%+v", b.Load())
	}
}

func TestStateSharedAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	New(dir).Open("http-422")
	if !New(dir).IsOpen() {
		t.Fatal("a fresh instance (new process) must see the open breaker")
	}
}

func TestCorruptFileReadsClosedAndHeals(t *testing.T) {
	b := New(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(b.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, junk := range []string{"garbage", `{"open":true,"consecutive":-3}`, ""} {
		_ = os.WriteFile(b.Path(), []byte(junk), 0o600)
		if junk != `{"open":true,"consecutive":-3}` && b.IsOpen() {
			t.Fatalf("%q read as open", junk)
		}
		if b.IsOpen() {
			t.Fatal("negative counter should read closed")
		}
	}
	b.Open("http-401")
	if !b.IsOpen() {
		t.Fatal("did not heal")
	}
}

func TestUnwritableStateDirIsBestEffort(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(f, nil, 0o600)
	b := New(f) // parent is a regular file: MkdirAll fails
	b.Open("http-401")
	b.RecordFailure("x")
	b.RecordSuccess()
	if b.IsOpen() {
		t.Fatal("cannot persist, must read closed")
	}
}

func TestNoTempFilesLeftBehind(t *testing.T) {
	b := New(t.TempDir())
	for i := 0; i < 5; i++ {
		b.RecordFailure("x")
		b.RecordSuccess()
	}
	ents, _ := os.ReadDir(b.dir)
	for _, e := range ents {
		if e.Name() != fileName && e.Name() != lockName {
			t.Errorf("stray file %s", e.Name())
		}
	}
}

func TestFilePerms(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("unix perms")
	}
	b := New(t.TempDir())
	b.Open("http-401")
	st, err := os.Stat(b.Path())
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("perm %v", st.Mode().Perm())
	}
}

// Concurrent failures must not lose updates: with N goroutines each recording
// one failure the breaker opens with the counter at the threshold, and
// concurrent Open calls leave a single consistent record.
func TestConcurrentGoroutinesNoLostUpdates(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b := New(dir) // separate instances, as separate processes would have
			b.RecordFailure("http-500")
			b.IsOpen()
		}()
	}
	wg.Wait()
	s := New(dir).Load()
	if !s.Open || s.Consecutive != FailureThreshold {
		t.Fatalf("lost update or torn write: %+v", s)
	}
}

func TestConcurrentOpenAndSuccessNeverReopens(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); New(dir).RecordSuccess() }()
		go func() { defer wg.Done(); New(dir).Open("http-401") }()
	}
	wg.Wait()
	if s := New(dir).Load(); !s.Open || s.Reason != "http-401" {
		t.Fatalf("%+v", s)
	}
}

func TestConcurrentProcessesNeverSeeTornState(t *testing.T) {
	dir := t.TempDir()
	New(dir).RecordFailure("x") // consecutive=1, closed
	var cmds []*exec.Cmd
	for i := 0; i < 5; i++ {
		c := exec.Command(os.Args[0])
		c.Env = append(os.Environ(), helperEnv+"="+dir)
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		cmds = append(cmds, c)
	}
	// Meanwhile this process opens the breaker; success calls must not undo it.
	New(dir).Open("http-422")
	for _, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	if s := New(dir).Load(); !s.Open || s.Reason != "http-422" {
		t.Fatalf("%+v", s)
	}
}
