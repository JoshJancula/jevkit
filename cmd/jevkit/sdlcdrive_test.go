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
	r := f.replies[0]
	f.replies = f.replies[1:]
	return r, nil
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
	code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "add a line")
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
	code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "add a line")
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
	code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "add a line")
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
	code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "add a line")
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
	code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "add a line")
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
