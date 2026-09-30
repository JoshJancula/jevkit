package adaptive

import "testing"

func assign(t *testing.T, s *State, id, role, binding, revision string) {
	t.Helper()
	if err := s.Assign(Assignment{InvocationID: id, AgentID: id, Role: role, Binding: binding, Revision: revision}); err != nil {
		t.Fatal(err)
	}
}
func report(t *testing.T, s *State, id, outcome, revision string) {
	t.Helper()
	if err := s.Apply(Result{InvocationID: id, AgentID: id, Outcome: outcome, Revision: revision}); err != nil {
		t.Fatal(err)
	}
}

func TestAdaptiveAnswerAndNoChangeAreValidEndings(t *testing.T) {
	for _, outcome := range []string{"answer", "no-change"} {
		s, _ := New("feature", "lean", 1, 2, 3)
		assign(t, &s, "p", "planner", "native:p", "")
		report(t, &s, "p", outcome, "")
		if s.Stage != Done || s.Outcome != outcome {
			t.Fatalf("%s: %+v", outcome, s)
		}
	}
}

func TestAdaptiveThreeIndependentAssessmentsOnExactRevision(t *testing.T) {
	s, _ := New("feature", "assured", 3, 2, 3)
	assign(t, &s, "p", "planner", "native:p", "")
	report(t, &s, "p", "planned", "plan-1")
	assign(t, &s, "i", "implementer", "runtime:cursor:m", "plan-1")
	report(t, &s, "i", "changed", "diff-1")
	if s.Stage != Verifying {
		t.Fatalf("changed should enter verifying: %+v", s)
	}
	passVerify(t, &s)
	if err := s.Assign(Assignment{InvocationID: "stale", AgentID: "stale", Role: "assessor", Binding: "native:stale", Revision: "diff-0"}); err == nil {
		t.Fatal("stale assessment allowed")
	}
	if err := s.Assign(Assignment{InvocationID: "self", AgentID: "self", Role: "assessor", Binding: "runtime:cursor:m", Revision: "diff-1"}); err == nil {
		t.Fatal("implementer binding allowed as assessor")
	}
	assign(t, &s, "a1", "assessor", "runtime:cursor:one", "diff-1")
	if err := s.Assign(Assignment{InvocationID: "alias", AgentID: "alias", Role: "assessor", Binding: "runtime:cursor:one", Revision: "diff-1"}); err == nil {
		t.Fatal("alias counted as independent")
	}
	assign(t, &s, "a2", "assessor", "runtime:cursor:two", "diff-1")
	if err := s.Assign(Assignment{InvocationID: "a3", AgentID: "a3", Role: "assessor", Binding: "runtime:cursor:three", Revision: "diff-1"}); err == nil {
		t.Fatal("parallel limit ignored")
	}
	before := len(s.Assignments)
	if err := s.Apply(Result{InvocationID: "a1", AgentID: "a1", Outcome: "approved", Revision: "wrong"}); err == nil || len(s.Assignments) != before {
		t.Fatal("stale result mutated state")
	}
	report(t, &s, "a1", "approved", "diff-1")
	assign(t, &s, "a3", "assessor", "runtime:cursor:three", "diff-1")
	report(t, &s, "a2", "approved", "diff-1")
	if s.Stage != Assessing {
		t.Fatalf("early completion: %+v", s)
	}
	report(t, &s, "a3", "approved", "diff-1")
	if s.Stage != Done || s.Outcome != "approved" {
		t.Fatalf("quorum failed: %+v", s)
	}
}

func TestAdaptiveDissentRequestsNewRevisionAndAuthFailureExcludesBinding(t *testing.T) {
	s, _ := New("bugfix", "collaborative", 2, 2, 2)
	assign(t, &s, "p", "planner", "native:p", "")
	report(t, &s, "p", "planned", "plan")
	if err := s.Assign(Assignment{InvocationID: "bad", AgentID: "bad", Role: "implementer", Binding: "runtime:cursor:m", Via: "runtime", Runtime: "cursor", Revision: "plan"}); err != nil {
		t.Fatal(err)
	}
	report(t, &s, "bad", "auth-failed", "")
	if !s.ExcludedBindings["runtime:cursor:m"] {
		t.Fatal("auth failure did not remove binding")
	}
	if !s.ExcludedRuntimes["cursor"] {
		t.Fatal("auth failure did not remove runtime")
	}
	if err := s.Assign(Assignment{InvocationID: "alias", AgentID: "alias", Role: "implementer", Binding: "runtime:cursor:m", Revision: "plan"}); err == nil {
		t.Fatal("failed runtime alias allowed")
	}
	if err := s.Assign(Assignment{InvocationID: "different-model", AgentID: "different-model", Role: "implementer", Binding: "runtime:cursor:other", Runtime: "cursor", Revision: "plan"}); err == nil {
		t.Fatal("failed runtime's other model allowed")
	}
	assign(t, &s, "good", "implementer", "runtime:codex:m", "plan")
	report(t, &s, "good", "changed", "diff-1")
	passVerify(t, &s)
	assign(t, &s, "r1", "assessor", "runtime:a", "diff-1")
	assign(t, &s, "r2", "assessor", "runtime:b", "diff-1")
	report(t, &s, "r1", "approved", "diff-1")
	report(t, &s, "r2", "changes-required", "diff-1")
	if s.Stage != Implementing {
		t.Fatalf("dissent did not request revision: %+v", s)
	}
	assign(t, &s, "good2", "implementer", "runtime:codex:m", "plan")
	report(t, &s, "good2", "changed", "diff-2")
	if s.Stage != Verifying || len(s.Assessments) != 0 {
		t.Fatalf("old assessments counted for new revision: %+v", s)
	}
}

func TestInvocationFailureExcludesBindingAndKeepsRole(t *testing.T) {
	s, _ := New("feature", "lean", 1, 1, 2)
	if err := s.Assign(Assignment{InvocationID: "first", AgentID: "first", Role: "planner", Binding: "runtime:codex:model", Runtime: "codex"}); err != nil {
		t.Fatal(err)
	}
	report(t, &s, "first", "invocation-failed", "")
	if s.Stage != Planning || !s.Excluded["first"] || !s.ExcludedBindings["runtime:codex:model"] || s.ExcludedRuntimes["codex"] {
		t.Fatalf("invocation failure state: %+v", s)
	}
	if err := s.Assign(Assignment{InvocationID: "alias", AgentID: "alias", Role: "planner", Binding: "runtime:codex:model", Runtime: "codex"}); err == nil {
		t.Fatal("failed binding alias allowed")
	}
	if err := s.Assign(Assignment{InvocationID: "other", AgentID: "other", Role: "planner", Binding: "runtime:codex:other", Runtime: "codex"}); err != nil {
		t.Fatalf("healthy binding on same runtime rejected: %v", err)
	}
}

func TestAdaptiveAssignmentAndCostBudgets(t *testing.T) {
	s, _ := New("feature", "lean", 1, 1, 2)
	s.MaxAssignments = 1
	assign(t, &s, "p", "planner", "runtime:p", "")
	report(t, &s, "p", "planned", "plan")
	if !s.BudgetExhausted() {
		t.Fatal("assignment budget ignored")
	}
	if err := s.Assign(Assignment{InvocationID: "i", AgentID: "i", Binding: "runtime:i", Role: "implementer", Revision: "plan"}); err == nil {
		t.Fatal("assignment allowed after budget")
	}
	s2, _ := New("feature", "lean", 1, 1, 2)
	s2.MaxEstimatedCostUSD = 1
	assign(t, &s2, "p", "planner", "runtime:p", "")
	if err := s2.Apply(Result{InvocationID: "p", AgentID: "p", Outcome: "planned", Revision: "plan", CostUSD: 1.1}); err != nil {
		t.Fatal(err)
	}
	if s2.Stage != Paused || s2.Outcome != "cost-budget-exhausted" {
		t.Fatalf("cost budget: %+v", s2)
	}
}

func TestAdaptiveRevisionLoopPausesAtConfiguredLimit(t *testing.T) {
	s, err := New("feature", "lean", 1, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	assign(t, &s, "p", "planner", "planner", "")
	report(t, &s, "p", "planned", "plan")
	assign(t, &s, "i1", "implementer", "impl", "plan")
	report(t, &s, "i1", "changed", "diff-1")
	passVerify(t, &s)
	assign(t, &s, "a1", "assessor", "reviewer", "diff-1")
	report(t, &s, "a1", "changes-required", "diff-1")
	if s.Stage != Paused || s.Outcome != "revision-budget-exhausted" {
		t.Fatalf("loop did not stop: %+v", s)
	}
	if err := s.Assign(Assignment{InvocationID: "i2", AgentID: "i2", Binding: "impl", Role: "implementer", Revision: "plan"}); err == nil {
		t.Fatal("extra implementation assigned")
	}
}

func TestAdaptiveTimeoutPauses(t *testing.T) {
	for outcome, reason := range map[string]string{"timed-out": "invocation-timeout", "run-time-exhausted": "run-time-budget-exhausted"} {
		s, _ := New("feature", "lean", 1, 1, 1)
		assign(t, &s, "p", "planner", "planner", "")
		report(t, &s, "p", outcome, "")
		if s.Stage != Paused || s.Outcome != reason {
			t.Fatalf("%s: %+v", outcome, s)
		}
	}
}

func TestAdaptiveDecisiveRejectionCancelsExcessAssessors(t *testing.T) {
	s, _ := New("feature", "collaborative", 2, 2, 3)
	assign(t, &s, "p", "planner", "p", "")
	report(t, &s, "p", "planned", "plan")
	assign(t, &s, "i", "implementer", "impl", "plan")
	report(t, &s, "i", "changed", "diff-1")
	passVerify(t, &s)
	assign(t, &s, "a1", "assessor", "r1", "diff-1")
	assign(t, &s, "a2", "assessor", "r2", "diff-1")
	report(t, &s, "a1", "changes-required", "diff-1")
	if s.Stage != Implementing || len(s.Assignments) != 0 {
		t.Fatalf("decisive rejection did not cancel excess: %+v", s)
	}
	if s.ReviewSlotsNeeded() != 0 || s.ParallelReviews() {
		t.Fatalf("review helpers after rejection: needed=%d parallel=%v", s.ReviewSlotsNeeded(), s.ParallelReviews())
	}
}

func TestAdaptiveConcurrentSpecialistsMatchByRole(t *testing.T) {
	s, _ := New("feature", "lean", 1, 2, 3)
	s.Stage = Specializing
	s.DiffRevision = "diff-1"
	s.LastImplementerBinding = "writer"
	s.SpecialistQueue = []SpecialistCheck{{Role: "qa", Revision: "diff-1"}, {Role: "security", Revision: "diff-1"}}
	s.AfterSpecialists = Verifying
	if s.Role() != "qa" {
		t.Fatalf("first role: %q", s.Role())
	}
	assign(t, &s, "q", "qa", "qa-bind", "diff-1")
	if s.Role() != "security" {
		t.Fatalf("next role while qa pending: %q", s.Role())
	}
	assign(t, &s, "s", "security", "sec-bind", "diff-1")
	if s.Role() != "" || s.ReviewSlotsNeeded() != 0 {
		t.Fatalf("all specialists reserved: role=%q needed=%d", s.Role(), s.ReviewSlotsNeeded())
	}
	report(t, &s, "s", "approved", "diff-1")
	if s.Stage != Specializing || len(s.Assignments) != 1 || len(s.SpecialistQueue) != 1 {
		t.Fatalf("security first: %+v", s)
	}
	report(t, &s, "q", "approved", "diff-1")
	if s.Stage != Verifying || len(s.SpecialistQueue) != 0 || len(s.Assignments) != 0 {
		t.Fatalf("after both: %+v", s)
	}
}
