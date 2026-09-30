package ledger

import (
	"sync"
	"testing"
	"time"

	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/engine"
)

func TestReserveFanoutSlotAtomic(t *testing.T) {
	store := Open(t.TempDir(), "run-fan")
	now := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	st, _ := adaptive.New("feature", "lean", 1, 3, 3)
	g := adaptive.BoundGraphConcurrency(adaptive.SubtaskGraph{
		Mode: "fan-out", IndependenceReason: "x", LatencyBenefit: "y", Parallelize: true,
		Subtasks: []adaptive.Subtask{
			{ID: "a", Objective: "A", ExpectedOutput: "a", OwnedPaths: []string{"a/"}, MergeOrder: 1, AcceptanceCriteria: []string{"ok"}},
			{ID: "b", Objective: "B", ExpectedOutput: "b", OwnedPaths: []string{"b/"}, MergeOrder: 2, AcceptanceCriteria: []string{"ok"}},
		},
	}, 2, 5)
	sched := adaptive.NewSchedule(g, "digest", "rev")
	run := Run{RunID: "run-fan", Workflow: "feature", CreatedAt: now.Format(time.RFC3339), UpdatedAt: now.Format(time.RFC3339), State: engine.State{Status: engine.StatusRunning}, Adaptive: &st, Fanout: &sched, TreeUsage: &TreeUsage{}}
	if err := store.WriteRun(run); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wins := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			err := store.ReserveFanoutSlot(now, func(r *Run, s *adaptive.Schedule) error {
				return s.Reserve("a", adaptive.Assignment{
					InvocationID: "inv", AgentID: "agent", Binding: "bind", Role: "implementer",
				}, "/wt", adaptive.WorkspaceWorktree, "lease", now)
			})
			if err == nil {
				wins <- "ok"
			}
		}(i)
	}
	wg.Wait()
	close(wins)
	count := 0
	for range wins {
		count++
	}
	if count != 1 {
		t.Fatalf("wins=%d", count)
	}
	got, err := store.ReadRun()
	if err != nil || got.Fanout.Subtasks["a"].Status != adaptive.SubtaskReserved || got.Fanout.Subtasks["a"].Attempt != 1 {
		t.Fatalf("stored: %+v %v", got.Fanout, err)
	}
}

func TestUpdateIntegrationPersistsWithRun(t *testing.T) {
	store := Open(t.TempDir(), "run-int")
	now := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)
	st, _ := adaptive.New("feature", "lean", 1, 1, 3)
	run := Run{
		RunID: "run-int", Workflow: "feature",
		CreatedAt: now.Format(time.RFC3339), UpdatedAt: now.Format(time.RFC3339),
		State: engine.State{Status: engine.StatusRunning}, Adaptive: &st, TreeUsage: &TreeUsage{},
	}
	if err := store.WriteRun(run); err != nil {
		t.Fatal(err)
	}
	err := store.UpdateIntegration(now, func(r *Run, integration *adaptive.IntegrationRecord) error {
		*integration = adaptive.IntegrationRecord{
			Status:               adaptive.IntegrationStatusApplied,
			CandidateFingerprint: "abc",
			Provenance: []adaptive.SubtaskProvenance{{
				SubtaskID: "a", Artifact: "fanout/a.txt", Status: adaptive.SubtaskSucceeded,
			}},
			ApplyPolicy:    adaptive.ApplyPreserveDirty,
			RollbackPolicy: adaptive.RollbackExplicit,
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.ReadRun()
	if err != nil || got.Integration == nil || got.Integration.CandidateFingerprint != "abc" {
		t.Fatalf("integration: %+v %v", got.Integration, err)
	}
	if len(got.Integration.Provenance) != 1 || got.Integration.Provenance[0].Artifact != "fanout/a.txt" {
		t.Fatalf("provenance: %+v", got.Integration.Provenance)
	}
}

func TestUpdateVerificationPersistsReceipts(t *testing.T) {
	store := Open(t.TempDir(), "run-ver")
	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	st, _ := adaptive.New("feature", "lean", 1, 1, 3)
	run := Run{RunID: "run-ver", Workflow: "feature", CreatedAt: now.Format(time.RFC3339), UpdatedAt: now.Format(time.RFC3339), State: engine.State{Status: engine.StatusRunning}, Adaptive: &st}
	if err := store.WriteRun(run); err != nil {
		t.Fatal(err)
	}
	err := store.UpdateVerification(now, func(r *Run, verification *adaptive.VerificationRecord) error {
		*verification = adaptive.VerificationRecord{
			Status:               adaptive.VerificationStatusPassed,
			CandidateFingerprint: "patch",
			WorktreeIdentity:     "tree-abc",
			AllPassed:            true,
			Receipts: []adaptive.CheckReceipt{{
				CheckID: "unit", Passed: true, WorktreeIdentity: "tree-abc",
				CandidateFingerprint: "patch", CommandIdentity: "cmd",
				PlanDigest: "plan", OutputPath: "logs/verification/unit.log", OutputDigest: "deadbeef",
			}},
		}
		r.Adaptive.CheckReceipts = append([]adaptive.CheckReceipt(nil), verification.Receipts...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.ReadRun()
	if err != nil || got.Verification == nil || got.Verification.WorktreeIdentity != "tree-abc" {
		t.Fatalf("verification: %+v %v", got.Verification, err)
	}
	if len(got.Adaptive.CheckReceipts) != 1 || got.Adaptive.CheckReceipts[0].OutputPath == "" {
		t.Fatalf("adaptive receipts: %+v", got.Adaptive.CheckReceipts)
	}
}
