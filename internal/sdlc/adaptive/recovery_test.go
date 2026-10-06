package adaptive

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRepairCannotFinishWithoutNewCandidate(t *testing.T) {
	for _, outcome := range []string{"answer", "no-change", "changed"} {
		t.Run(outcome, func(t *testing.T) {
			s, _ := New("bugfix", "lean", 1, 1, 3)
			s.Stage, s.PlanRevision, s.DiffRevision = Implementing, "plan", "diff"
			s.CheckReceipts = []CheckReceipt{{CheckID: "test", Passed: false}}
			s.RepairFeedback = &RepairFeedback{Revision: "diff", Summary: "Fix the missing guard"}
			for _, id := range []string{"first", "second"} {
				assign(t, &s, id, "implementer", "builder", "plan")
				report(t, &s, id, outcome, "diff")
				if s.Stage == Done || s.RevisionCount != 0 || s.RepairFeedback == nil || s.CheckReceipts[0].Passed {
					t.Fatalf("unchanged attempt lost the outstanding work: %+v", s)
				}
				// The attempt limit must survive restarting the driver.
				raw, err := json.Marshal(s)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(raw, &s); err != nil {
					t.Fatal(err)
				}
			}
			if s.Stage != Paused || s.Outcome != "implementer-failed" || !strings.Contains(s.PendingReason, "two implementation attempts") {
				t.Fatalf("unproductive retries were not bounded: %+v", s)
			}
		})
	}
}

func TestRepairProgressClearsOldFeedback(t *testing.T) {
	s, _ := New("bugfix", "lean", 1, 1, 3)
	s.Stage, s.PlanRevision, s.DiffRevision = Assessing, "plan", "diff"
	assign(t, &s, "review", "assessor", "reviewer", "diff")
	if err := s.Apply(Result{InvocationID: "review", AgentID: "review", Outcome: "changes-required", Revision: "diff", Reason: "Fix the missing guard"}); err != nil {
		t.Fatal(err)
	}
	if s.RepairFeedback == nil || s.RepairFeedback.Summary != "Fix the missing guard" {
		t.Fatalf("lost decisive findings: %+v", s)
	}
	assign(t, &s, "noop", "implementer", "builder", "plan")
	report(t, &s, "noop", "no-change", "")
	assign(t, &s, "fix", "implementer", "builder", "plan")
	report(t, &s, "fix", "changed", "new-diff")
	if s.Stage != Verifying || s.NoProgressCount != 0 || s.RepairFeedback != nil {
		t.Fatalf("new candidate retained stale feedback: %+v", s)
	}
}

func TestInitialImplementationNoChangeStillCompletes(t *testing.T) {
	s, _ := New("bugfix", "lean", 1, 1, 3)
	s.Stage, s.PlanRevision = Implementing, "plan"
	assign(t, &s, "i", "implementer", "builder", "plan")
	report(t, &s, "i", "no-change", "")
	if s.Stage != Done {
		t.Fatalf("initial no-change should remain valid: %+v", s)
	}
}

func TestSpecialistRejectionRetainsRepairEvidence(t *testing.T) {
	s, _ := New("bugfix", "lean", 1, 1, 3)
	s.Stage, s.DiffRevision = Specializing, "diff"
	s.SpecialistQueue = []SpecialistCheck{{Role: "security", Revision: "diff"}}
	assign(t, &s, "review", "security", "reviewer", "diff")
	if err := s.Apply(Result{InvocationID: "review", AgentID: "review", Outcome: "changes-required", Revision: "diff", Reason: "Validate the path before opening it"}); err != nil {
		t.Fatal(err)
	}
	if s.Stage != Implementing || s.RepairFeedback == nil || s.RepairFeedback.Role != "security" || s.RepairFeedback.Summary != "Validate the path before opening it" {
		t.Fatalf("specialist findings lost: %+v", s)
	}
}
