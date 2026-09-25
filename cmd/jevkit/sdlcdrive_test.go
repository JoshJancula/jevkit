package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OWNER/jevkit/internal/sdlc/adaptive"
	"github.com/OWNER/jevkit/internal/sdlc/ledger"
	"github.com/OWNER/jevkit/internal/sdlc/worker"
)

type fakeSDLCExecutor struct {
	replies  []worker.Reply
	requests []worker.Request
	failures map[string]bool
}

type blockingSDLCExecutor struct{}

func (blockingSDLCExecutor) Execute(ctx context.Context, _ worker.Request) (worker.Reply, error) {
	<-ctx.Done()
	return worker.Reply{}, ctx.Err()
}

func TestSDLCStartAndDriveHelpExplainHandoff(t *testing.T) {
	a := newApp(t)
	code, out, errs := run(a, "", "sdlc", "start", "--help")
	if code != exitOK || !strings.Contains(out, "saves a run") || !strings.Contains(out, "resume RUN_ID") {
		t.Fatalf("start help: %d %q %q", code, out, errs)
	}
	code, out, errs = run(a, "", "sdlc", "drive", "--help")
	if code != exitOK || !strings.Contains(out, "launches its CLI runtime") || !strings.Contains(out, "One call runs one step") {
		t.Fatalf("drive help: %d %q %q", code, out, errs)
	}
}

func (f *fakeSDLCExecutor) Execute(_ context.Context, req worker.Request) (worker.Reply, error) {
	f.requests = append(f.requests, req)
	if f.failures[req.Agent.ID] {
		return worker.Reply{}, &worker.InvocationFailure{Err: fmt.Errorf("cannot launch %s", req.Agent.ID)}
	}
	r := f.replies[0]
	f.replies = f.replies[1:]
	return r, nil
}

func TestResumeRetryFailedAssessorPreservesCompletedWork(t *testing.T) {
	a := newApp(t)
	fakeSDLCReach(a)
	writeFile(t, a.sdlcRosterPath(), `version: 1
agents:
  - {id: planner, roles: [planner], rubric: Plan., via: runtime, runtime: codex, model: p}
  - {id: builder, roles: [implementer], rubric: Build., via: runtime, runtime: codex, model: b}
  - {id: reviewer, roles: [assessor], rubric: Review., via: runtime, runtime: cursor, model: r}
`)
	f := &fakeSDLCExecutor{failures: map[string]bool{"reviewer": true}, replies: []worker.Reply{{Outcome: "planned", Content: "Plan"}, {Outcome: "changed", Content: "diff --git a/a b/a\n+line\n"}}}
	a.SdlcExecutor = f
	code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "add line", "--auto")
	if code != exitOK {
		t.Fatalf("start: %d %s %s", code, out, errs)
	}
	id := strings.Fields(out)[1]
	code, _, errs = run(a, "", "sdlc", "drive", id, "--until-done")
	if code != exitOK {
		t.Fatalf("drive: %d %s", code, errs)
	}
	stored, err := ledger.Open(a.sdlcRunsDir(), id).ReadRun()
	if err != nil || stored.Adaptive.Stage != adaptive.Paused || stored.Adaptive.Outcome != "assessor-invocations-exhausted" {
		t.Fatalf("pause: %+v %v", stored.Adaptive, err)
	}
	delete(f.failures, "reviewer")
	f.replies = append(f.replies, worker.Reply{Outcome: "approved"})
	code, _, errs = run(a, "", "sdlc", "resume", id, "--retry-failed")
	if code != exitOK {
		t.Fatalf("retry: %d %s", code, errs)
	}
	stored, err = ledger.Open(a.sdlcRunsDir(), id).ReadRun()
	if err != nil || stored.Adaptive.Stage != adaptive.Done || len(f.requests) != 4 {
		t.Fatalf("retry state: %+v requests=%d err=%v", stored.Adaptive, len(f.requests), err)
	}
}

func TestResumeRetryFailedPlannerAfterMalformedReply(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	code, out, errs := run(a, "", "sdlc", "start", "bugfix", "--task", "fix a comment", "--auto")
	if code != exitOK {
		t.Fatalf("start: %d %s %s", code, out, errs)
	}
	id := strings.Fields(out)[1]
	store := ledger.Open(a.sdlcRunsDir(), id)
	stored, err := store.ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	stored.Adaptive.Pause("planner-failed")
	if err := store.WriteRun(stored); err != nil {
		t.Fatal(err)
	}
	a.SdlcExecutor = &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "planned", Content: "Remove the comment."}}}
	code, _, errs = run(a, "", "sdlc", "resume", id, "--retry-failed", "--step")
	if code != exitOK {
		t.Fatalf("retry: %d %s", code, errs)
	}
	stored, err = store.ReadRun()
	if err != nil || stored.Adaptive.Stage != adaptive.Implementing {
		t.Fatalf("retry state: %+v %v", stored.Adaptive, err)
	}
}

func TestLegacyBinaryReviewArtifactIsRejected(t *testing.T) {
	for _, artifact := range [][]byte{
		[]byte("diff --git a/tool b/tool\nGIT binary patch\nliteral 1\n"),
		[]byte(strings.Repeat("x", 513*1024)),
	} {
		if err := validateReviewArtifact(artifact); err == nil || !strings.Contains(err.Error(), "start a new SDLC run") {
			t.Fatalf("unsafe artifact accepted: %v", err)
		}
	}
	if err := validateReviewArtifact([]byte("Invocation change report\nChanged paths: 1\n")); err != nil {
		t.Fatalf("new change report rejected: %v", err)
	}
}

func TestUnsafeReviewArtifactPausesWithCause(t *testing.T) {
	a := newApp(t)
	st, err := adaptive.New("feature", "lean", 1, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	st.Stage = adaptive.Assessing
	st.DiffRevision = "rev"
	assignment := adaptive.Assignment{InvocationID: "inv", AgentID: "reviewer", Binding: "runtime:claude:opus::", Runtime: "claude", Role: "assessor", Revision: "rev"}
	if err := st.Assign(assignment); err != nil {
		t.Fatal(err)
	}
	id := "unsafe-review-test"
	store := ledger.Open(a.sdlcRunsDir(), id)
	if err := store.WriteRun(ledger.Run{RunID: id, Workflow: "feature", WorkDir: a.WorkDir, Adaptive: &st}); err != nil {
		t.Fatal(err)
	}
	if err := a.sdlcPauseUnsafeReview(id, assignment, fmt.Errorf("legacy binary patch")); err == nil {
		t.Fatal("expected pause error")
	}
	stored, err := store.ReadRun()
	if err != nil || stored.Adaptive.Stage != adaptive.Paused || stored.Adaptive.Outcome != "unsafe-review-artifact" || stored.Adaptive.PendingReason != "legacy binary patch" {
		t.Fatalf("pause state: %+v %v", stored.Adaptive, err)
	}
}

func TestReviewHandoffIncludesEarlierImplementationReports(t *testing.T) {
	a := newApp(t)
	fakeSDLCReach(a)
	writeFile(t, a.sdlcRosterPath(), `version: 1
agents:
  - {id: planner, roles: [planner], rubric: Plan., via: runtime, runtime: codex, model: p}
  - {id: builder, roles: [implementer], rubric: Build., via: runtime, runtime: codex, model: b}
  - {id: reviewer, roles: [assessor], rubric: Review., via: runtime, runtime: cursor, model: r}
`)
	f := &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "planned", Content: "Plan"}, {Outcome: "changed", Content: "first report"}, {Outcome: "changes-required"}, {Outcome: "changed", Content: "second report"}, {Outcome: "approved"}}}
	a.SdlcExecutor = f
	code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "add line", "--auto")
	if code != exitOK {
		t.Fatalf("start: %d %s %s", code, out, errs)
	}
	id := strings.Fields(out)[1]
	code, _, errs = run(a, "", "sdlc", "drive", id, "--until-done")
	if code != exitOK {
		t.Fatalf("drive: %d %s", code, errs)
	}
	stored, err := ledger.Open(a.sdlcRunsDir(), id).ReadRun()
	if err != nil || stored.Adaptive.Stage != adaptive.Done {
		t.Fatalf("run: %+v %v", stored.Adaptive, err)
	}
	report, err := ledger.Open(a.sdlcRunsDir(), id).ReadArtifact("patch.diff")
	if err != nil || !strings.Contains(string(report), "first report") || !strings.Contains(string(report), "second report") || len(f.requests) != 5 {
		t.Fatalf("report: %q requests=%d err=%v", report, len(f.requests), err)
	}
}

func TestSDLCDriveReroutesInvocationFailuresForEveryRole(t *testing.T) {
	a := newApp(t)
	fakeSDLCReach(a)
	writeFile(t, a.sdlcRosterPath(), `version: 1
agents:
  - {id: a-planner, roles: [planner], rubric: Plan A., via: runtime, runtime: codex, model: pa}
  - {id: b-planner, roles: [planner], rubric: Plan B., via: runtime, runtime: cursor, model: pb}
  - {id: a-implementer, roles: [implementer], rubric: Implement A., via: runtime, runtime: codex, model: ia}
  - {id: b-implementer, roles: [implementer], rubric: Implement B., via: runtime, runtime: cursor, model: ib}
  - {id: a-assessor, roles: [assessor], rubric: Assess A., via: runtime, runtime: codex, model: aa}
  - {id: b-assessor, roles: [assessor], rubric: Assess B., via: runtime, runtime: cursor, model: ab}
`)
	f := &fakeSDLCExecutor{
		failures: map[string]bool{"a-planner": true, "a-implementer": true, "a-assessor": true},
		replies: []worker.Reply{
			{Outcome: "planned", Content: "Plan the change."},
			{Outcome: "changed", Content: "diff --git a/a b/a\n+new line\n"},
			{Outcome: "approved"},
		},
	}
	a.SdlcExecutor = f
	code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "add a line", "--auto")
	if code != exitOK {
		t.Fatalf("start: %d %q %q", code, out, errs)
	}
	runID := strings.Fields(out)[1]
	code, _, errs = run(a, "", "sdlc", "drive", runID, "--until-done")
	if code != exitOK {
		t.Fatalf("drive: %d %q", code, errs)
	}
	r, err := ledger.Open(a.sdlcRunsDir(), runID).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if r.Adaptive.Stage != adaptive.Done || r.Adaptive.Outcome != "approved" {
		t.Fatalf("run: %+v", r.Adaptive)
	}
	want := []string{"a-planner", "b-planner", "a-implementer", "b-implementer", "a-assessor", "b-assessor"}
	if len(f.requests) != len(want) {
		t.Fatalf("requests: %+v", f.requests)
	}
	for i, req := range f.requests {
		if req.Agent.ID != want[i] {
			t.Fatalf("request %d: got %s, want %s", i, req.Agent.ID, want[i])
		}
	}
}

func TestSDLCDriveUsesEnrolledWorkersAndExactRevisions(t *testing.T) {
	a := newApp(t)
	fakeSDLCReach(a)
	writeFile(t, a.sdlcRosterPath(), `version: 1
agents:
  - {id: planner, roles: [planner], rubric: Plan., via: runtime, runtime: codex, model: p}
  - {id: implementer, roles: [implementer], rubric: Implement., via: runtime, runtime: cursor, model: i}
  - {id: assessor, roles: [assessor], rubric: Assess., via: runtime, runtime: opencode, model: a}
`)
	f := &fakeSDLCExecutor{replies: []worker.Reply{
		{Outcome: "planned", Content: "Plan the change."},
		{Outcome: "changed", Content: "diff --git a/a b/a\n+new line\n"},
		{Outcome: "approved", Content: "Looks good."},
	}}
	a.SdlcExecutor = f
	code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "add a line", "--auto")
	if code != exitOK {
		t.Fatalf("start: %d %q %q", code, out, errs)
	}
	runID := strings.Fields(out)[1]
	code, _, errs = run(a, "", "sdlc", "drive", runID, "--until-done")
	if code != exitOK {
		t.Fatalf("drive: %d %q", code, errs)
	}
	r, err := ledger.Open(a.sdlcRunsDir(), runID).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if r.Adaptive.Stage != adaptive.Done || r.Adaptive.Outcome != "approved" {
		t.Fatalf("run: %+v", r.Adaptive)
	}
	if len(f.requests) != 3 || f.requests[0].Agent.ID != "planner" || f.requests[1].Agent.ID != "implementer" || f.requests[2].Agent.ID != "assessor" {
		t.Fatalf("requests: %+v", f.requests)
	}
	planRev := fmt.Sprintf("%x", sha256.Sum256([]byte("Plan the change.")))
	diffRev := fmt.Sprintf("%x", sha256.Sum256([]byte("diff --git a/a b/a\n+new line\n")))
	if f.requests[1].Assignment.Revision != planRev || f.requests[1].Plan != "Plan the change." || f.requests[2].Assignment.Revision != diffRev || f.requests[2].Diff == "" {
		t.Fatalf("revisions: plan=%+v assessment=%+v", f.requests[1], f.requests[2])
	}
}

func TestSDLCDriveAuthFailurePausesWhenNoReplacement(t *testing.T) {
	a := newApp(t)
	fakeSDLCReach(a)
	writeFile(t, a.sdlcRosterPath(), `version: 1
agents:
  - {id: planner, roles: [planner], rubric: Plan., via: runtime, runtime: codex, model: p}
  - {id: implementer, roles: [implementer], rubric: Implement., via: runtime, runtime: cursor, model: i}
  - {id: assessor, roles: [assessor], rubric: Assess., via: runtime, runtime: opencode, model: a}
`)
	a.SdlcExecutor = &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "auth-failed"}}}
	code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "add a line", "--auto")
	if code != exitOK {
		t.Fatalf("start: %d %q %q", code, out, errs)
	}
	runID := strings.Fields(out)[1]
	code, _, errs = run(a, "", "sdlc", "drive", runID)
	if code != exitOK {
		t.Fatalf("drive: %d %q", code, errs)
	}
	r, err := ledger.Open(a.sdlcRunsDir(), runID).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if r.Adaptive.Stage != adaptive.Paused || !r.Adaptive.ExcludedRuntimes["codex"] {
		t.Fatalf("run: %+v", r.Adaptive)
	}
}

func TestSDLCDrivePausesTimedOutInvocation(t *testing.T) {
	a := newApp(t)
	fakeSDLCReach(a)
	writeFile(t, a.sdlcPolicyPath(), "version: 1\nmaxInvocationSeconds: 1\n")
	writeFile(t, a.sdlcRosterPath(), `version: 1
agents:
  - {id: planner, roles: [planner], rubric: Plan., via: runtime, runtime: codex, model: p}
  - {id: implementer, roles: [implementer], rubric: Implement., via: runtime, runtime: cursor, model: i}
  - {id: assessor, roles: [assessor], rubric: Assess., via: runtime, runtime: opencode, model: a}
`)
	a.SdlcExecutor = blockingSDLCExecutor{}
	code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "add a line", "--auto")
	if code != exitOK {
		t.Fatalf("start: %d %q %q", code, out, errs)
	}
	runID := strings.Fields(out)[1]
	code, _, errs = run(a, "", "sdlc", "drive", runID, "--until-done")
	if code == exitOK || !strings.Contains(errs, "invocation-timeout") {
		t.Fatalf("timeout: %d %q", code, errs)
	}
	r, err := ledger.Open(a.sdlcRunsDir(), runID).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if r.Adaptive.Stage != adaptive.Paused || r.Adaptive.Outcome != "invocation-timeout" {
		t.Fatalf("run: %+v", r.Adaptive)
	}
}

func TestSDLCNextPausesExpiredRun(t *testing.T) {
	a := newApp(t)
	fakeSDLCReach(a)
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	a.Now = func() time.Time { return base }
	writeFile(t, a.sdlcPolicyPath(), "version: 1\nmaxRunSeconds: 1\n")
	writeFile(t, a.sdlcRosterPath(), `version: 1
agents:
  - {id: planner, roles: [planner], rubric: Plan., via: runtime, runtime: codex, model: p}
  - {id: implementer, roles: [implementer], rubric: Implement., via: runtime, runtime: cursor, model: i}
  - {id: assessor, roles: [assessor], rubric: Assess., via: runtime, runtime: opencode, model: a}
`)
	code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "add a line", "--auto")
	if code != exitOK {
		t.Fatalf("start: %d %q %q", code, out, errs)
	}
	runID := strings.Fields(out)[1]
	a.Now = func() time.Time { return base.Add(2 * time.Second) }
	code, _, errs = run(a, "", "sdlc", "next", runID)
	if code == exitOK || !strings.Contains(errs, "run-time-budget-exhausted") {
		t.Fatalf("expired next: %d %q", code, errs)
	}
	r, err := ledger.Open(a.sdlcRunsDir(), runID).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if r.Adaptive.Stage != adaptive.Paused || r.Adaptive.Outcome != "run-time-budget-exhausted" {
		t.Fatalf("run: %+v", r.Adaptive)
	}
}

func TestSDLCReportPausesExpiredRun(t *testing.T) {
	a := newApp(t)
	fakeSDLCReach(a)
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	a.Now = func() time.Time { return base }
	writeFile(t, a.sdlcPolicyPath(), "version: 1\nmaxRunSeconds: 1\n")
	writeFile(t, a.sdlcRosterPath(), `version: 1
agents:
  - {id: planner, roles: [planner], rubric: Plan., via: runtime, runtime: codex, model: p}
  - {id: implementer, roles: [implementer], rubric: Implement., via: runtime, runtime: cursor, model: i}
  - {id: assessor, roles: [assessor], rubric: Assess., via: runtime, runtime: opencode, model: a}
`)
	code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "add a line", "--auto")
	if code != exitOK {
		t.Fatalf("start: %d %q %q", code, out, errs)
	}
	runID := strings.Fields(out)[1]
	code, out, errs = run(a, "", "sdlc", "next", runID)
	if code != exitOK {
		t.Fatalf("next: %d %q %q", code, out, errs)
	}
	var assignment adaptive.Assignment
	if err := json.Unmarshal([]byte(out), &assignment); err != nil {
		t.Fatal(err)
	}
	a.Now = func() time.Time { return base.Add(2 * time.Second) }
	code, _, errs = run(a, "", "sdlc", "report", runID, "--invocation", assignment.InvocationID, "--agent", assignment.AgentID, "--outcome", "answer")
	if code == exitOK || !strings.Contains(errs, "run-time-budget-exhausted") {
		t.Fatalf("expired report: %d %q", code, errs)
	}
	r, err := ledger.Open(a.sdlcRunsDir(), runID).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if r.Adaptive.Stage != adaptive.Paused {
		t.Fatalf("run: %+v", r.Adaptive)
	}
}

func TestSDLCConcurrentAssessmentsDoNotOverwriteEachOther(t *testing.T) {
	a := newApp(t)
	a.Stdout = io.Discard
	st, err := adaptive.New("feature", "collaborative", 2, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		assignment adaptive.Assignment
		result     adaptive.Result
	}{
		{adaptive.Assignment{InvocationID: "p", AgentID: "p", Binding: "p", Role: "planner"}, adaptive.Result{InvocationID: "p", AgentID: "p", Outcome: "planned", Revision: "plan"}},
		{adaptive.Assignment{InvocationID: "i", AgentID: "i", Binding: "i", Role: "implementer", Revision: "plan"}, adaptive.Result{InvocationID: "i", AgentID: "i", Outcome: "changed", Revision: "diff"}},
	} {
		if err := st.Assign(step.assignment); err != nil {
			t.Fatal(err)
		}
		if err := st.Apply(step.result); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"a", "b"} {
		if err := st.Assign(adaptive.Assignment{InvocationID: id, AgentID: id, Binding: id, Role: "assessor", Revision: "diff"}); err != nil {
			t.Fatal(err)
		}
	}
	store := ledger.Open(a.sdlcRunsDir(), "concurrent")
	if err := store.WriteRun(ledger.Run{RunID: "concurrent", CreatedAt: a.now().UTC().Format(time.RFC3339), Adaptive: &st}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, id := range []string{"a", "b"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			errs <- a.sdlcRecordResult("concurrent", adaptive.Result{InvocationID: id, AgentID: id, Outcome: "approved", Revision: "diff"}, "", nil)
		}(id)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	run, err := store.ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if run.Adaptive.Stage != adaptive.Done || len(run.Adaptive.Assessments) != 2 {
		t.Fatalf("lost assessment: %+v", run.Adaptive)
	}
}
