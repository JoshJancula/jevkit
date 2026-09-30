package adaptive

import (
	"strings"
	"testing"
	"time"
)

func passVerify(t *testing.T, s *State) {
	t.Helper()
	if s.Stage != Verifying {
		t.Fatalf("passVerify expected verifying, got %s", s.Stage)
	}
	rec := VerificationRecord{
		Status: VerificationStatusPassed, AllPassed: true,
		CandidateFingerprint: s.DiffRevision,
		Receipts: []CheckReceipt{{
			CheckID: "ok", Passed: true,
			CandidateFingerprint: s.DiffRevision, WorktreeIdentity: "wt",
		}},
	}
	if err := ApplyVerificationResult(s, rec, time.Now()); err != nil {
		t.Fatal(err)
	}
	if s.Stage != Assessing {
		t.Fatalf("passVerify should reach assessing: %+v", s)
	}
}

func TestApplyVerificationResultReturnsToImplementingOnFailure(t *testing.T) {
	st, err := New("feature", "lean", 1, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	st.Stage = Verifying
	st.DiffRevision = "cand"
	st.RevisionCount = 1
	rec := VerificationRecord{
		Status: VerificationStatusFailed, AllPassed: false,
		Receipts: []CheckReceipt{{CheckID: "c", Passed: false, TimedOut: true, WorktreeIdentity: "wt"}},
		Summary: "boom", FailureSummaryPath: ArtifactVerificationSummary,
	}
	if err := ApplyVerificationResult(&st, rec, time.Now()); err != nil {
		t.Fatal(err)
	}
	if st.Stage != Implementing {
		t.Fatalf("stage: %s", st.Stage)
	}
	if !strings.Contains(st.PendingReason, ArtifactVerificationSummary) {
		t.Fatalf("pending reason should cite summary path: %q", st.PendingReason)
	}
	if len(st.CheckReceipts) != 1 {
		t.Fatalf("receipts: %d", len(st.CheckReceipts))
	}
}

func TestApplyVerificationResultPausesWhenRevisionBudgetExhausted(t *testing.T) {
	st, err := New("feature", "lean", 1, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	st.Stage = Verifying
	st.DiffRevision = "cand"
	st.RevisionCount = 1
	rec := VerificationRecord{
		Status: VerificationStatusFailed, AllPassed: false,
		Receipts: []CheckReceipt{{CheckID: "c", Passed: false, WorktreeIdentity: "wt"}},
		Summary: "still failing", FailureSummaryPath: ArtifactVerificationSummary,
	}
	if err := ApplyVerificationResult(&st, rec, time.Now()); err != nil {
		t.Fatal(err)
	}
	if st.Stage != Paused || st.Outcome != "revision-budget-exhausted" {
		t.Fatalf("want pause on exhausted revisions: %+v", st)
	}
	if !strings.Contains(st.PendingReason, "revision budget exhausted") {
		t.Fatalf("recovery message: %q", st.PendingReason)
	}
}

func TestAssessorQuorumCannotOverrideFailedChecks(t *testing.T) {
	s, _ := New("feature", "lean", 1, 1, 3)
	assign(t, &s, "p", "planner", "native:p", "")
	report(t, &s, "p", "planned", "plan")
	assign(t, &s, "i", "implementer", "runtime:impl", "plan")
	report(t, &s, "i", "changed", "diff-1")
	passVerify(t, &s)
	s.CheckReceipts = []CheckReceipt{{
		CheckID: "unit", Passed: false, CandidateFingerprint: "diff-1", WorktreeIdentity: "wt",
	}}
	assign(t, &s, "a1", "assessor", "runtime:a", "diff-1")
	report(t, &s, "a1", "approved", "diff-1")
	if s.Stage != Verifying || s.Outcome == "approved" {
		t.Fatalf("failed receipts must block Done despite assessor prose: %+v", s)
	}
}

func TestChecksAllowCompletionRejectsFailedOrWrongRevision(t *testing.T) {
	if ChecksAllowCompletion([]CheckReceipt{{Passed: false}}, "c") {
		t.Fatal("failed receipt must block")
	}
	if ChecksAllowCompletion([]CheckReceipt{{Passed: true, TimedOut: true}}, "c") {
		t.Fatal("timed-out receipt must block")
	}
	if ChecksAllowCompletion([]CheckReceipt{{Passed: true, CandidateFingerprint: "other"}}, "c") {
		t.Fatal("wrong revision must block")
	}
	if !ChecksAllowCompletion(nil, "c") {
		t.Fatal("empty skip receipts allow completion")
	}
	if !ChecksAllowCompletion([]CheckReceipt{{Passed: true, CandidateFingerprint: "c"}}, "c") {
		t.Fatal("matching pass should allow")
	}
}
