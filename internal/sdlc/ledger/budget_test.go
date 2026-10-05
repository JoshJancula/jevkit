package ledger

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBudgetAllLimitsAndExplicitExtensions(t *testing.T) {
	now := time.Unix(100, 0)
	for _, tc := range []struct {
		name, kind string
		used, add  Allowances
	}{
		{"assignments", "assignment", Allowances{Assignments: 20}, Allowances{Assignments: 5}},
		{"revisions", "revision", Allowances{Revisions: 3}, Allowances{Revisions: 1}},
		{"time", "work", Allowances{Seconds: 21600}, Allowances{Seconds: 5400}},
		{"cost", "work", Allowances{CostUSD: 10.5}, Allowances{CostUSD: 1}},
		{"steps", "step", Allowances{Steps: 20}, Allowances{Steps: 5}},
		{"children", "child", Allowances{Children: 8}, Allowances{Children: 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := NewBudget(Allowances{Assignments: 20, Revisions: 3, Seconds: 21600, CostUSD: 10, Steps: 20, Children: 8}, now, 1800)
			b.Usage = tc.used
			if len(b.Blockers(tc.kind)) != 1 {
				t.Fatalf("blockers: %v", b.Blockers(tc.kind))
			}
			before := b.Usage
			if err := b.Extend("child", "operator flags", tc.add, now); err != nil {
				t.Fatal(err)
			}
			if len(b.Blockers(tc.kind)) != 0 || b.Usage != before || len(b.Extensions) != 1 {
				t.Fatalf("grant: %+v", b)
			}
			if b.Extensions[0].Before != b.Original || b.Extensions[0].After != b.Limits {
				t.Fatal("missing audit")
			}
		})
	}
}

func TestBudgetReservationsOverageAndDuplicates(t *testing.T) {
	b := NewBudget(Allowances{Assignments: 2, Revisions: 1, Seconds: 60, CostUSD: 1}, time.Unix(100, 0), 1800)
	if err := b.Reserve("one", "root", "revision"); err != nil {
		t.Fatal(err)
	}
	if err := b.Reserve("two", "child", "revision"); err == nil {
		t.Fatal("unreserved revision race")
	}
	if err := b.Complete("one", true, 1.25); err != nil {
		t.Fatal(err)
	}
	if err := b.Complete("one", true, 1.25); err != nil {
		t.Fatal(err)
	}
	if b.Usage.Assignments != 1 || b.Usage.Revisions != 1 || b.Usage.CostUSD != 1.25 || b.ReservedRevisions() != 0 {
		t.Fatalf("double charged: %+v", b)
	}
	if len(b.Blockers("revision")) != 2 {
		t.Fatal("must expose simultaneous blockers")
	}
	if err := b.Extend("root", "operator", Allowances{CostUSD: .1}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(b.Blockers("revision")) != 2 {
		t.Fatal("insufficient grant admitted work")
	}
	for _, bad := range []float64{math.NaN(), math.Inf(1), -1} {
		if err := b.Complete("bad", false, bad); err == nil {
			t.Fatal("invalid cost")
		}
	}
}

func TestBudgetActiveUnionIdleCrashAndHostExpiry(t *testing.T) {
	now := time.Unix(100, 0)
	b := NewBudget(Allowances{Seconds: 60}, now, 30)
	b.Tick(now.Add(time.Hour))
	if b.Usage.Seconds != 0 {
		t.Fatal("operator wait charged")
	}
	now = now.Add(time.Hour)
	b.Activities["host1"] = Activity{Host: true, Seen: now, Until: now.Add(30 * time.Second)}
	b.Tick(now.Add(10 * time.Second))
	b.Activities["host2"] = Activity{Host: true, Seen: now.Add(10 * time.Second), Until: now.Add(40 * time.Second)}
	b.Tick(now.Add(time.Hour))
	if b.Usage.Seconds != 40 {
		t.Fatalf("overlap/expiry: %v", b.Usage.Seconds)
	}
	now = now.Add(time.Hour)
	b.Activities["driver"] = Activity{Seen: now, Until: now.Add(15 * time.Second)}
	b.Tick(now.Add(5 * time.Second))
	b.Activities["driver"] = Activity{Seen: now.Add(5 * time.Second), Until: now.Add(20 * time.Second)}
	b.Tick(now.Add(24 * time.Hour))
	if b.Usage.Seconds != 45 {
		t.Fatalf("crash charged offline time: %v", b.Usage.Seconds)
	}
}

func TestBudgetAtomicConcurrentReservationsExtensionsAndFailedWrite(t *testing.T) {
	store := Open(t.TempDir(), "root")
	initial := func() (Budget, error) {
		return NewBudget(Allowances{Assignments: 2, Revisions: 2, Seconds: 60}, time.Now(), 1800), nil
	}
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for i := range 30 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := store.UpdateBudget(initial, func(b *Budget) error { return b.Reserve(fmt.Sprintf("inv-%d", i), "child", "assignment") })
			if err == nil {
				admitted.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if admitted.Load() != 2 {
		t.Fatalf("admitted %d", admitted.Load())
	}
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := store.UpdateBudget(initial, func(b *Budget) error { return b.Extend("root", "operator", Allowances{Assignments: 1}, time.Now()) }); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	b, err := store.ReadBudget()
	if err != nil {
		t.Fatal(err)
	}
	if b.Limits.Assignments != 10 || b.Usage.Assignments != 2 || len(b.Extensions) != 8 {
		t.Fatalf("lost update: %+v", b)
	}
	if err := store.UpdateBudget(nil, func(b *Budget) error { b.Usage.Assignments = 999; return errors.New("interrupted") }); err == nil {
		t.Fatal("expected failed transaction")
	}
	b, _ = store.ReadBudget()
	if b.Usage.Assignments != 2 {
		t.Fatal("failed update committed")
	}
	if err := os.WriteFile(filepath.Join(store.Dir, "budget.json.tmp-interrupted"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadBudget(); err != nil {
		t.Fatal("temporary file replaced committed record")
	}
}

func TestBudgetWarningOnceAfterPersistence(t *testing.T) {
	store := Open(t.TempDir(), "root")
	initial := func() (Budget, error) { return NewBudget(Allowances{Assignments: 20}, time.Now(), 1800), nil }
	if err := store.UpdateBudget(initial, func(b *Budget) error {
		b.Usage.Assignments = 16
		if len(b.Warnings()) != 1 {
			t.Fatal("missing warning")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateBudget(nil, func(b *Budget) error {
		if len(b.Warnings()) != 0 {
			t.Fatal("repeated warning")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
