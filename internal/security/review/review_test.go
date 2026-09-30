package review

import (
	"os"
	"runtime"
	"sync"
	"testing"
)

func TestCreatePendingResolveAndAllowlist(t *testing.T) {
	state := t.TempDir()
	hash := Hash("untrusted result")
	r, err := Create(state, Record{SessionKey: "session-a", ContentSHA256: hash, Runtime: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := Pending(state, "session-a"); !ok {
		t.Fatal("missing pending latch")
	}
	info, err := os.Stat(dir(state) + "/" + r.ID + ".json")
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("permissions: %v", info.Mode())
	}
	if _, err := Resolve(state, r.ID, "allow", "reviewed"); err != nil {
		t.Fatal(err)
	}
	if _, ok := Pending(state, "session-a"); ok {
		t.Fatal("latch remained")
	}
	if !Allowed(state, hash) {
		t.Fatal("hash not allowlisted")
	}
}

func TestConcurrentCreate(t *testing.T) {
	state := t.TempDir()
	const count = 20
	var wg sync.WaitGroup
	wg.Add(count)
	for i := 0; i < count; i++ {
		go func() {
			defer wg.Done()
			if _, err := Create(state, Record{SessionKey: "same"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	all, err := List(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != count {
		t.Fatalf("got %d reviews", len(all))
	}
	for i := 0; i < count; i++ {
		pending, ok := Pending(state, "same")
		if !ok {
			t.Fatalf("latch cleared with %d reviews remaining", count-i)
		}
		if _, err := Resolve(state, pending.ID, "deny", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := Pending(state, "same"); ok {
		t.Fatal("latch remained after all resolutions")
	}
}
