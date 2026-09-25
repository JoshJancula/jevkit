package adaptive

import "testing"

func TestHandoffKeepsPhaseAndStopsAfterTwo(t *testing.T) {
	s, err := New("feature", "lean", 1, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	for i, bind := range []string{"one", "two", "three"} {
		a := Assignment{InvocationID: bind, AgentID: bind, Binding: bind, Role: "planner"}
		if err := s.Assign(a); err != nil {
			t.Fatal(err)
		}
		if err := s.Apply(Result{InvocationID: bind, AgentID: bind, Outcome: "handoff", Focus: "database", Reason: "needs schema review"}); err != nil {
			t.Fatal(err)
		}
		if i < 2 && (s.Stage != Planning || !s.ExcludedBindings[bind]) {
			t.Fatalf("handoff %d: %+v", i, s)
		}
	}
	if s.Stage != Paused || s.Outcome != "handoff-budget-exhausted" {
		t.Fatalf("limit: %+v", s)
	}
}

func TestSpecialistReviewsApproveExactDiffBeforeAssessment(t *testing.T) {
	s, err := New("feature", "lean", 1, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	s.Stage = Specializing
	s.DiffRevision = "diff-1"
	s.LastImplementerBinding = "writer"
	s.SpecialistQueue = []SpecialistCheck{{Role: "qa", Revision: "diff-1"}, {Role: "security", Revision: "diff-1"}}
	s.AfterSpecialists = Assessing
	qa := Assignment{InvocationID: "q", AgentID: "qa", Binding: "writer", Role: "qa", Revision: "diff-1"}
	if err := s.Assign(qa); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(Result{InvocationID: "q", AgentID: "qa", Outcome: "approved", Revision: "wrong"}); err == nil {
		t.Fatal("stale diff approved")
	}
	if err := s.Apply(Result{InvocationID: "q", AgentID: "qa", Outcome: "approved", Revision: "diff-1"}); err != nil {
		t.Fatal(err)
	}
	if s.Stage != Specializing || len(s.SpecialistReviews) != 1 || !s.SpecialistReviews[0].Overlap {
		t.Fatalf("first review: %+v", s)
	}
	security := Assignment{InvocationID: "s", AgentID: "security", Binding: "independent", Role: "security", Revision: "diff-1"}
	if err := s.Assign(security); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(Result{InvocationID: "s", AgentID: "security", Outcome: "approved", Revision: "diff-1"}); err != nil {
		t.Fatal(err)
	}
	if s.Stage != Assessing || len(s.SpecialistQueue) != 0 || len(s.SpecialistReviews) != 2 {
		t.Fatalf("after approvals: %+v", s)
	}
}
