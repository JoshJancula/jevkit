package sdlc

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/JoshJancula/jevkit/cmd/jevkit/internal/testkit"
	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/sdlc/worker"
)

func TestReviewAdmissionExhaustionPausesWithoutSpinning(t *testing.T) {
	for _, budget := range []string{"time", "cost", "assignments"} {
		t.Run(budget, func(t *testing.T) {
			a := newApp(t)
			a.Stdout = io.Discard
			collaborativeReviewRoster(t, a)
			testkit.WriteFile(t, a.sdlcPolicyPath(), "version: 1\nmaxConcurrent: 3\nmaxEstimatedCostUsd: 10\n")
			id := "exhausted-" + budget
			r := seedAssessingRun(t, a, id, 2, 2)
			policy, _, err := a.sdlcEnrollment()
			if err != nil {
				t.Fatal(err)
			}
			switch budget {
			case "time":
				r.CreatedAt = a.Clock().Add(-time.Duration(policy.MaxRunSeconds+1) * time.Second).UTC().Format(time.RFC3339)
			case "cost":
				if policy.MaxEstimatedCostUSD <= 0 {
					t.Fatal("fixture needs a finite cost budget")
				}
				r.TreeUsage.EstimatedCostUSD = policy.MaxEstimatedCostUSD
			case "assignments":
				r.TreeUsage.Assignments = policy.MaxAssignments
			}
			store := ledger.Open(a.SDLCRunsDir(), id)
			if err := store.WriteRun(r); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := a.sdlcDriveUntilDone(ctx, id); err == nil || ctx.Err() != nil {
				t.Fatalf("expected a concrete blocker before deadline, got %v", err)
			}
			saved, err := store.ReadRun()
			if err != nil || saved.Adaptive.Stage != adaptive.Paused || !strings.Contains(saved.Adaptive.PendingReason, "budget exhausted") {
				t.Fatalf("blocker not persisted: %+v, %v", saved.Adaptive, err)
			}
		})
	}
}

func TestReviewAdmissionDoesNotSubtractReservationsTwice(t *testing.T) {
	a := newApp(t)
	collaborativeReviewRoster(t, a)
	r := seedAssessingRun(t, a, "review-reservations", 3, 3)
	r.Adaptive.Assignments["first"] = adaptive.Assignment{InvocationID: "first", AgentID: "reviewer-a", Binding: "runtime:cursor:ra::", Role: "assessor", Revision: "diff"}
	policy, _, err := a.sdlcEnrollment()
	if err != nil {
		t.Fatal(err)
	}
	r.Adaptive.MaxAssignments = r.Adaptive.AssignmentCount + 2
	got, err := a.reviewAdmitCap(r, policy)
	if err != nil || got != 2 {
		t.Fatalf("two eligible reviewers and two remaining assignments should admit two: %d, %v", got, err)
	}
	r.Adaptive.MaxAssignments = r.Adaptive.AssignmentCount
	got, err = a.reviewAdmitCap(r, policy)
	if err != nil || got != 0 {
		t.Fatalf("already charged reservation must remain executable: %d, %v", got, err)
	}
}

func TestDecisiveFeedbackSurvivesLateReviewAndRestart(t *testing.T) {
	a := newApp(t)
	a.Stdout = io.Discard
	collaborativeReviewRoster(t, a)
	id := "decisive-feedback"
	r := seedAssessingRun(t, a, id, 2, 2)
	for _, name := range []string{"reject", "late"} {
		if err := r.Adaptive.Assign(adaptive.Assignment{InvocationID: name, AgentID: name, Binding: name, Role: "assessor", Revision: "diff"}); err != nil {
			t.Fatal(err)
		}
	}
	store := ledger.Open(a.SDLCRunsDir(), id)
	if err := store.WriteRun(r); err != nil {
		t.Fatal(err)
	}
	findings := "Fix the parser guard.\n" + strings.Repeat("details\n", 2000)
	if err := a.sdlcRecordResult(id, adaptive.Result{InvocationID: "reject", AgentID: "reject", Outcome: "changes-required", Revision: "diff"}, "responses/reject.txt", []byte(findings)); err != nil {
		t.Fatal(err)
	}
	if err := a.sdlcRecordResult(id, adaptive.Result{InvocationID: "late", AgentID: "late", Outcome: "approved", Revision: "diff"}, "responses/late.txt", []byte("looks fine")); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteArtifact("last-assessment.md", []byte("looks fine")); err != nil {
		t.Fatal(err)
	}
	saved, err := store.ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	note := sdlcRepairContext(store, *saved.Adaptive)
	if !strings.Contains(note, "Fix the parser guard") || !strings.Contains(note, "responses/reject.txt") || strings.Contains(note, "looks fine") {
		t.Fatalf("wrong decisive feedback: %s", note)
	}
	if len(saved.Adaptive.RepairFeedback.Summary) > worker.MaxVerificationSummaryBytes {
		t.Fatal("feedback was not bounded")
	}
	saved.Adaptive.DiffRevision = "new-diff"
	if strings.Contains(sdlcRepairContext(store, *saved.Adaptive), "Fix the parser guard") {
		t.Fatal("stale feedback supplied to a new candidate")
	}
}

func TestUnchangedHostReportDoesNotChargeRevisionOrScheduleSpecialists(t *testing.T) {
	a := newApp(t)
	a.Stdout = io.Discard
	collaborativeReviewRoster(t, a)
	id := "unchanged-host-report"
	r := seedAssessingRun(t, a, id, 2, 2)
	r.Adaptive.Stage = adaptive.Implementing
	if err := r.Adaptive.Assign(adaptive.Assignment{InvocationID: "repair", AgentID: "builder", Binding: "builder", Role: "implementer", Revision: "plan"}); err != nil {
		t.Fatal(err)
	}
	store := ledger.Open(a.SDLCRunsDir(), id)
	if err := store.WriteRun(r); err != nil {
		t.Fatal(err)
	}
	if err := a.sdlcRecordResult(id, adaptive.Result{InvocationID: "repair", AgentID: "builder", Outcome: "changed", Revision: "diff"}, "", nil); err != nil {
		t.Fatal(err)
	}
	saved, err := store.ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if saved.TreeUsage.Revisions != r.TreeUsage.Revisions || saved.Adaptive.Stage != adaptive.Implementing || saved.Adaptive.PendingDecision != "" || saved.Adaptive.NoProgressCount != 1 || saved.Adaptive.PendingReason == "" {
		t.Fatalf("unchanged report was treated as a new candidate: %+v, usage=%+v", saved.Adaptive, saved.TreeUsage)
	}
}
