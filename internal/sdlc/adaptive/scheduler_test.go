package adaptive

import (
	"sync"
	"testing"
	"time"
)

func testFanoutGraph() SubtaskGraph {
	return BoundGraphConcurrency(SubtaskGraph{
		Mode:               "fan-out",
		IndependenceReason: "separate packages",
		LatencyBenefit:     "parallel builds",
		Parallelize:        true,
		IntegrationOwner:   "api",
		SharedPaths:        []string{"internal/api/types.go"},
		Subtasks: []Subtask{
			{ID: "api", Objective: "API", ExpectedOutput: "api patch", OwnedPaths: []string{"internal/api/"}, MergeOrder: 1, AcceptanceCriteria: []string{"ok"}},
			{ID: "ui", Objective: "UI", ExpectedOutput: "ui patch", OwnedPaths: []string{"web/"}, MergeOrder: 2, AcceptanceCriteria: []string{"ok"}},
			{ID: "docs", Objective: "Docs", ExpectedOutput: "docs", OwnedPaths: []string{"docs/"}, MergeOrder: 3, DependsOn: []string{"api", "ui"}, AcceptanceCriteria: []string{"ok"}},
		},
	}, 3, 10)
}

func TestScheduleTwoWayFanoutAndDependentTask(t *testing.T) {
	s := NewSchedule(testFanoutGraph(), "graph-1", "rev-abc")
	ready := s.ReadyIDs()
	if len(ready) != 2 || ready[0] != "api" || ready[1] != "ui" {
		t.Fatalf("ready: %v", ready)
	}
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	if err := s.Reserve("api", Assignment{InvocationID: "i1", AgentID: "a1", Binding: "b1", Role: "implementer"}, "/tmp/wt-api", WorkspaceWorktree, "lease-1", now); err != nil {
		t.Fatal(err)
	}
	if err := s.Reserve("ui", Assignment{InvocationID: "i2", AgentID: "a2", Binding: "b2", Role: "implementer"}, "/tmp/wt-ui", WorkspaceWorktree, "lease-2", now); err != nil {
		t.Fatal(err)
	}
	if err := s.Reserve("docs", Assignment{InvocationID: "i3", AgentID: "a3", Binding: "b3", Role: "implementer"}, "/tmp/wt-docs", WorkspaceWorktree, "lease-3", now); err == nil {
		t.Fatal("dependent reserved before parents finished")
	}
	if err := s.MarkRunning("api", 11, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete("api", SubtaskSucceeded, Result{InvocationID: "i1", AgentID: "a1", Outcome: "changed"}, nil, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete("ui", SubtaskSucceeded, Result{InvocationID: "i2", AgentID: "a2", Outcome: "changed"}, nil, now); err != nil {
		t.Fatal(err)
	}
	ready = s.ReadyIDs()
	if len(ready) != 1 || ready[0] != "docs" {
		t.Fatalf("dependent ready: %v", ready)
	}
}

func TestScheduleDuplicateReserveRejected(t *testing.T) {
	s := NewSchedule(testFanoutGraph(), "g", "rev")
	now := time.Now().UTC()
	a := Assignment{InvocationID: "i1", AgentID: "a1", Binding: "b1", Role: "implementer"}
	if err := s.Reserve("api", a, "/wt", WorkspaceWorktree, "lease", now); err != nil {
		t.Fatal(err)
	}
	if err := s.Reserve("api", Assignment{InvocationID: "i2", AgentID: "a2", Binding: "b2", Role: "implementer"}, "/wt2", WorkspaceWorktree, "lease2", now); err == nil {
		t.Fatal("duplicate reserve allowed")
	}
}

func TestAdmitCapUsesMinimumBudget(t *testing.T) {
	cap := AdmitCap(AdmitBudgets{
		PolicyMax: 5, RunTreeLimit: 4, EligibleBindings: 3,
		RemainingAssignments: 2, RemainingChildRuns: 8,
		TimeRemaining: time.Minute, CostRemainingOK: true,
	}, 10, 0)
	if cap != 2 {
		t.Fatalf("cap=%d", cap)
	}
	if AdmitCap(AdmitBudgets{PolicyMax: 5, TimeRemaining: 0, CostRemainingOK: true}, 3, 0) != 0 {
		t.Fatal("time exhausted still admitted")
	}
	if AdmitCap(AdmitBudgets{PolicyMax: 5, TimeRemaining: time.Minute, CostRemainingOK: false}, 3, 0) != 0 {
		t.Fatal("cost exhausted still admitted")
	}
	if AdmitCap(AdmitBudgets{
		PolicyMax: 5, RemainingAssignments: 5, EligibleBindings: 2,
		TimeRemaining: time.Minute, CostRemainingOK: true,
	}, 3, 1) != 1 {
		t.Fatal("in-flight not subtracted")
	}
}

func TestIsolationNeverSharesLiveWorktreeConcurrently(t *testing.T) {
	d := DecideIsolation(true, true, false, true, false)
	if d.Mode != WorkspaceWorktree {
		t.Fatalf("parallel writable: %+v", d)
	}
	d = DecideIsolation(true, true, false, false, false)
	if d.Mode != WorkspaceSequential {
		t.Fatalf("no isolation degrade: %+v", d)
	}
	d = DecideIsolation(true, true, false, false, true)
	if d.Mode != WorkspaceUnavailable {
		t.Fatalf("concurrent without isolation: %+v", d)
	}
	d = DecideIsolation(false, false, true, false, false)
	if d.Mode != WorkspaceSharedReadOnly {
		t.Fatalf("readonly share: %+v", d)
	}
	d = DecideIsolation(false, false, false, false, false)
	if d.Mode != WorkspaceUnavailable {
		t.Fatalf("readonly without enforcement: %+v", d)
	}
}

func TestScheduleReconcileDeadLeaseAndRetryConsumesAttempt(t *testing.T) {
	s := NewSchedule(testFanoutGraph(), "g", "rev")
	now := time.Now().UTC()
	if err := s.Reserve("api", Assignment{InvocationID: "i1", AgentID: "a1", Binding: "b1", Role: "implementer"}, "/wt", WorkspaceWorktree, "lease-1", now); err != nil {
		t.Fatal(err)
	}
	_ = s.MarkRunning("api", 99, now)
	recovered := s.Reconcile(nil, now.Add(time.Second))
	if len(recovered) != 1 || s.Subtasks["api"].Status != SubtaskFailed || s.Subtasks["api"].Attempt != 1 {
		t.Fatalf("reconcile: %v %+v", recovered, s.Subtasks["api"])
	}
	if err := s.RetryFailed("api"); err != nil {
		t.Fatal(err)
	}
	if err := s.Reserve("api", Assignment{InvocationID: "i2", AgentID: "a1", Binding: "b1", Role: "implementer"}, "/wt", WorkspaceWorktree, "lease-2", now); err != nil {
		t.Fatal(err)
	}
	if s.Subtasks["api"].Attempt != 2 {
		t.Fatalf("retry did not consume another attempt: %+v", s.Subtasks["api"])
	}
}

func TestConcurrentReserveUnderMutex(t *testing.T) {
	s := NewSchedule(testFanoutGraph(), "g", "rev")
	now := time.Now().UTC()
	var mu sync.Mutex
	var wg sync.WaitGroup
	wins := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			mu.Lock()
			defer mu.Unlock()
			err := s.Reserve("api", Assignment{
				InvocationID: "i", AgentID: "a", Binding: "b", Role: "implementer",
			}, "/wt", WorkspaceWorktree, "lease", now)
			if err == nil {
				wins++
			}
		}(i)
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("wins=%d", wins)
	}
}

func TestFailedSubtaskBlocksIntegrationPending(t *testing.T) {
	g := BoundGraphConcurrency(SubtaskGraph{
		Mode: "fan-out", IndependenceReason: "x", LatencyBenefit: "y", Parallelize: true,
		Subtasks: []Subtask{
			{ID: "a", Objective: "A", ExpectedOutput: "a", OwnedPaths: []string{"a/"}, MergeOrder: 1, AcceptanceCriteria: []string{"ok"}},
			{ID: "b", Objective: "B", ExpectedOutput: "b", OwnedPaths: []string{"b/"}, MergeOrder: 2, AcceptanceCriteria: []string{"ok"}},
		},
	}, 2, 5)
	s := NewSchedule(g, "g", "rev")
	now := time.Now().UTC()
	_ = s.Reserve("a", Assignment{InvocationID: "1", AgentID: "a", Binding: "ba", Role: "implementer"}, "/a", WorkspaceWorktree, "l1", now)
	_ = s.Reserve("b", Assignment{InvocationID: "2", AgentID: "b", Binding: "bb", Role: "implementer"}, "/b", WorkspaceWorktree, "l2", now)
	_ = s.Complete("a", SubtaskSucceeded, Result{InvocationID: "1", AgentID: "a", Outcome: "changed"}, nil, now)
	_ = s.Complete("b", SubtaskFailed, Result{InvocationID: "2", AgentID: "b", Outcome: "failed"}, nil, now)
	if s.IntegrationPending || !s.AllTerminal() || !s.HasFailures() {
		t.Fatalf("schedule: %+v", s)
	}
}
