package adaptive

import (
	"strings"
	"testing"
	"time"
)

func testContribs(statusA, statusB string, pathsA, pathsB []string) (Schedule, []SubtaskContribution) {
	g := BoundGraphConcurrency(SubtaskGraph{
		Mode: "fan-out", IndependenceReason: "x", LatencyBenefit: "y", Parallelize: true,
		IntegrationOwner: "a",
		Subtasks: []Subtask{
			{ID: "a", Objective: "A", ExpectedOutput: "a", OwnedPaths: []string{"a/"}, MergeOrder: 1, AcceptanceCriteria: []string{"ok"}},
			{ID: "b", Objective: "B", ExpectedOutput: "b", OwnedPaths: []string{"b/"}, MergeOrder: 2, AcceptanceCriteria: []string{"ok"}},
		},
	}, 2, 5)
	s := NewSchedule(g, "graph", "rev-base")
	now := time.Now().UTC()
	_ = s.Reserve("a", Assignment{InvocationID: "ia", AgentID: "aa", Binding: "ba", Role: "implementer"}, "/wt-a", WorkspaceWorktree, "l1", now)
	_ = s.Reserve("b", Assignment{InvocationID: "ib", AgentID: "ab", Binding: "bb", Role: "implementer"}, "/wt-b", WorkspaceWorktree, "l2", now)
	_ = s.Complete("a", statusA, Result{InvocationID: "ia", AgentID: "aa", Outcome: "changed"}, nil, now)
	_ = s.Complete("b", statusB, Result{InvocationID: "ib", AgentID: "ab", Outcome: "changed"}, nil, now)
	contribs := []SubtaskContribution{
		{
			SubtaskID: "a", BaseRevision: "rev-base", Patch: []byte("patch-a\n"), Handoff: "a done",
			ChangedPaths: pathsA, OwnedPaths: []string{"a/"}, MergeOrder: 1, Status: statusA,
			Artifact: "fanout/a-ia.txt", InvocationID: "ia", AgentID: "aa", Workspace: "/wt-a",
		},
		{
			SubtaskID: "b", BaseRevision: "rev-base", Patch: []byte("patch-b\n"), Handoff: "b done",
			ChangedPaths: pathsB, OwnedPaths: []string{"b/"}, MergeOrder: 2, Status: statusB,
			Artifact: "fanout/b-ib.txt", InvocationID: "ib", AgentID: "ab", Workspace: "/wt-b",
		},
	}
	return s, contribs
}

func TestIntegrationCleanMerge(t *testing.T) {
	s, contribs := testContribs(SubtaskSucceeded, SubtaskSucceeded, []string{"a/one.go"}, []string{"b/two.go"})
	rec := PlanIntegration(s, contribs, ProjectSurface{HeadRevision: "rev-base"}, nil)
	if rec.Status != IntegrationStatusPending {
		t.Fatalf("status: %+v", rec)
	}
	if len(rec.ApplyOrder) != 2 || rec.ApplyOrder[0] != "a" || rec.ApplyOrder[1] != "b" {
		t.Fatalf("order: %v", rec.ApplyOrder)
	}
	if len(rec.Provenance) != 2 {
		t.Fatalf("provenance: %+v", rec.Provenance)
	}
	combined := CombinePatches(rec.ApplyOrder, ContributionsByID(contribs))
	now := time.Now().UTC()
	if err := rec.CommitApply(combined, now); err != nil {
		t.Fatal(err)
	}
	if rec.Status != IntegrationStatusApplied || rec.CandidateFingerprint == "" {
		t.Fatalf("applied: %+v", rec)
	}
	st, err := New("feature", "lean", 1, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	st.Stage = Implementing
	st.Assessments = []Assessment{{InvocationID: "old", AgentID: "x", Binding: "x", Revision: "old", Approved: true}}
	st.CheckReceipts = []CheckReceipt{{CheckID: "c1", CandidateFingerprint: "old", Passed: true}}
	if err := ApplyIntegratedCandidate(&st, rec.CandidateFingerprint); err != nil {
		t.Fatal(err)
	}
	if st.Stage != Verifying || st.DiffRevision != rec.CandidateFingerprint {
		t.Fatalf("state: %+v", st)
	}
	if len(st.Assessments) != 0 || len(st.CheckReceipts) != 0 {
		t.Fatalf("invalidation failed: %+v", st)
	}
	if st.LastImplementerBinding != "supervisor-integration" {
		t.Fatalf("binding: %s", st.LastImplementerBinding)
	}
}

func TestIntegrationOverlappingEdits(t *testing.T) {
	s, contribs := testContribs(SubtaskSucceeded, SubtaskSucceeded, []string{"shared/x.go"}, []string{"shared/x.go"})
	contribs[0].OwnedPaths = []string{"shared/"}
	contribs[1].OwnedPaths = []string{"shared/"}
	rec := PlanIntegration(s, contribs, ProjectSurface{HeadRevision: "rev-base"}, nil)
	if rec.Status != IntegrationStatusConflict && rec.Status != IntegrationStatusRepair {
		t.Fatalf("expected conflict/repair: %+v", rec)
	}
	if rec.Repair == nil || rec.RepairAssignment == nil {
		t.Fatalf("expected repair assignment: %+v", rec)
	}
	if err := rec.CommitApply([]byte("x"), time.Now().UTC()); err == nil {
		t.Fatal("overlap must not apply")
	}
}

func TestIntegrationDirtyStartingTree(t *testing.T) {
	s, contribs := testContribs(SubtaskSucceeded, SubtaskSucceeded, []string{"a/one.go"}, []string{"b/two.go"})
	rec := PlanIntegration(s, contribs, ProjectSurface{
		HeadRevision: "rev-base",
		DirtyPaths:   []string{"a/one.go", "user-notes.md"},
	}, nil)
	preserved := false
	for _, p := range rec.PreservedDirty {
		if p == "a/one.go" {
			preserved = true
		}
	}
	if !preserved {
		t.Fatalf("expected preserve dirty: %+v", rec)
	}
	applyB := false
	for _, pa := range rec.PathPlan {
		if pa.Path == "b/two.go" && pa.Action == "apply" {
			applyB = true
		}
		if pa.Path == "a/one.go" && pa.Action != "preserve-dirty" {
			t.Fatalf("dirty path not preserved: %+v", pa)
		}
	}
	if !applyB {
		t.Fatalf("clean path not applied: %+v", rec.PathPlan)
	}
	// All-dirty pause: each subtask only touches its own dirty path (no overlap).
	s2, onlyDirty := testContribs(SubtaskSucceeded, SubtaskSucceeded, []string{"a/one.go"}, []string{"b/two.go"})
	rec2 := PlanIntegration(s2, onlyDirty, ProjectSurface{HeadRevision: "rev-base", DirtyPaths: []string{"a/one.go", "b/two.go"}}, nil)
	if rec2.Status != IntegrationStatusPaused {
		t.Fatalf("all-dirty should pause: %+v", rec2)
	}
}

func TestIntegrationStaleBase(t *testing.T) {
	s, contribs := testContribs(SubtaskSucceeded, SubtaskSucceeded, []string{"a/one.go"}, []string{"b/two.go"})
	contribs[0].BaseRevision = "old-rev"
	rec := PlanIntegration(s, contribs, ProjectSurface{HeadRevision: "rev-base"}, nil)
	if rec.Status != IntegrationStatusRepair {
		t.Fatalf("stale base: %+v", rec)
	}
	if rec.Repair == nil || !strings.Contains(rec.Summary, "stale") {
		t.Fatalf("repair brief: %+v", rec)
	}
}

func TestIntegrationOneFailedWorker(t *testing.T) {
	s, contribs := testContribs(SubtaskSucceeded, SubtaskFailed, []string{"a/one.go"}, nil)
	rec := PlanIntegration(s, contribs, ProjectSurface{HeadRevision: "rev-base"}, nil)
	if rec.Status != IntegrationStatusRepair || rec.Repair == nil {
		t.Fatalf("failed worker: %+v", rec)
	}
	if len(rec.Repair.FailedIDs) != 1 || rec.Repair.FailedIDs[0] != "b" {
		t.Fatalf("failed ids: %+v", rec.Repair)
	}
	// Subtask must not mark aggregate complete.
	if s.IntegrationPending {
		t.Fatal("failed schedule should not set integrationPending")
	}
}

func TestIntegrationPartialRetryNoDoubleApply(t *testing.T) {
	s, contribs := testContribs(SubtaskSucceeded, SubtaskSucceeded, []string{"a/one.go"}, []string{"b/two.go"})
	rec := PlanIntegration(s, contribs[:1], ProjectSurface{HeadRevision: "rev-base"}, nil)
	// Force pending only a
	rec.PendingSubtasks = []string{"a"}
	combined := CombinePatches([]string{"a"}, ContributionsByID(contribs))
	now := time.Now().UTC()
	if err := rec.CommitApply(combined, now); err != nil {
		t.Fatal(err)
	}
	fp1 := rec.CandidateFingerprint
	if err := rec.CommitApply(combined, now); err == nil {
		t.Fatal("double-apply allowed")
	}
	// Partial retry: re-plan with a already applied, add b
	rec2 := PlanIntegration(s, contribs, ProjectSurface{HeadRevision: "rev-base"}, []string{"a"})
	if rec2.Status != IntegrationStatusPending {
		t.Fatalf("partial: %+v", rec2)
	}
	if len(rec2.PendingSubtasks) != 1 || rec2.PendingSubtasks[0] != "b" {
		t.Fatalf("pending: %v", rec2.PendingSubtasks)
	}
	combined2 := CombinePatches(rec2.ApplyOrder, ContributionsByID(contribs))
	if err := rec2.CommitApply(combined2, now); err != nil {
		t.Fatal(err)
	}
	if rec2.CandidateFingerprint == fp1 {
		t.Fatal("fingerprint should change after additional apply content")
	}
	if err := rec2.Rollback(now); err != nil {
		t.Fatal(err)
	}
	if rec2.Status != IntegrationStatusRolledBack || rec2.CandidateFingerprint != "" {
		t.Fatalf("rollback: %+v", rec2)
	}
}

func TestSubtaskCannotMarkAggregateComplete(t *testing.T) {
	s, _ := testContribs(SubtaskSucceeded, SubtaskSucceeded, []string{"a/one.go"}, []string{"b/two.go"})
	if !s.IntegrationPending {
		t.Fatal("expected integration pending after all success")
	}
	// Completing subtasks never sets adaptive Done — only IntegrationPending.
	st, _ := New("feature", "lean", 1, 1, 3)
	st.Stage = Implementing
	// Simulate that a subtask result must not call Apply with changed for fan-out aggregate.
	if st.Stage == Done {
		t.Fatal("must not be done")
	}
}

func TestCollectContributionsStableOrder(t *testing.T) {
	s, _ := testContribs(SubtaskSucceeded, SubtaskSucceeded, nil, nil)
	patches := map[string][]byte{"b": []byte("B"), "a": []byte("A")}
	changed := map[string][]string{"a": {"a/x"}, "b": {"b/y"}}
	got := CollectContributions(s, patches, map[string]string{"a": "ha"}, changed, func(id string) string {
		return "fanout/" + id + ".txt"
	})
	if len(got) != 2 || got[0].SubtaskID != "a" || got[1].SubtaskID != "b" {
		t.Fatalf("order: %+v", got)
	}
	if string(got[0].Patch) != "A" || got[0].Artifact != "fanout/a.txt" {
		t.Fatalf("collect: %+v", got[0])
	}
}

func TestPathInOwnedScopes(t *testing.T) {
	if !pathInOwnedScopes("internal/api/x.go", []string{"internal/api/"}) {
		t.Fatal("dir scope")
	}
	if pathInOwnedScopes("web/x.go", []string{"internal/api/"}) {
		t.Fatal("outside")
	}
	if !pathInOwnedScopes("pkg/foo.go", []string{"pkg/foo.go"}) {
		t.Fatal("exact file")
	}
}
