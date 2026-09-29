package main

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/enrollment"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/sdlc/worker"
)

type concurrentReviewExecutor struct {
	mu        sync.Mutex
	started   atomic.Int32
	maxSeen   atomic.Int32
	gate      chan struct{}
	release   chan struct{}
	byAgent   map[string]worker.Reply
	defaultR  worker.Reply
	requests  []worker.Request
	blockOn   map[string]bool
	costByAgent map[string]float64
}

func (f *concurrentReviewExecutor) Execute(ctx context.Context, req worker.Request) (worker.Reply, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()

	n := f.started.Add(1)
	for {
		cur := f.maxSeen.Load()
		if n <= cur || f.maxSeen.CompareAndSwap(cur, n) {
			break
		}
	}
	if f.gate != nil {
		select {
		case f.gate <- struct{}{}:
		case <-ctx.Done():
			return worker.Reply{}, ctx.Err()
		}
	}
	if f.blockOn[req.Agent.ID] {
		select {
		case <-f.release:
		case <-ctx.Done():
			cost := f.costByAgent[req.Agent.ID]
			return worker.Reply{CostUSD: cost, CostReported: cost > 0}, ctx.Err()
		}
	}
	f.mu.Lock()
	reply, ok := f.byAgent[req.Agent.ID]
	if !ok {
		reply = f.defaultR
	}
	if cost, exists := f.costByAgent[req.Agent.ID]; exists && reply.CostUSD == 0 {
		reply.CostUSD = cost
		reply.CostReported = true
	}
	f.mu.Unlock()
	if reply.Outcome == "planned" {
		if len(reply.NextSteps) == 0 {
			reply.NextSteps = []string{"implement the plan"}
		}
		if len(reply.AcceptanceCriteria) == 0 {
			reply.AcceptanceCriteria = []string{"acceptance covered"}
		}
		if reply.Checks == nil {
			reply.Checks = []adaptive.Check{}
		}
	}
	return reply, nil
}

func collaborativeReviewRoster(t *testing.T, a *App) {
	t.Helper()
	fakeSDLCReach(a)
	writeFile(t, a.sdlcPolicyPath(), "version: 1\nmaxConcurrent: 3\n")
	writeFile(t, a.sdlcRosterPath(), `version: 1
agents:
  - {id: planner, roles: [planner], rubric: Plan., via: runtime, runtime: codex, model: p}
  - {id: builder, roles: [implementer], rubric: Build., via: runtime, runtime: codex, model: b}
  - {id: reviewer-a, roles: [assessor], rubric: Review A., via: runtime, runtime: cursor, model: ra}
  - {id: reviewer-b, roles: [assessor], rubric: Review B., via: runtime, runtime: opencode, model: rb}
  - {id: reviewer-c, roles: [assessor], rubric: Review C., via: runtime, runtime: codex, model: rc}
`)
}

func seedAssessingRun(t *testing.T, a *App, runID string, quorum, maxConcurrent int) ledger.Run {
	t.Helper()
	st, err := adaptive.New("feature", "collaborative", quorum, maxConcurrent, 3)
	if err != nil {
		t.Fatal(err)
	}
	now := a.now().UTC().Format(time.RFC3339)
	for _, step := range []struct {
		assignment adaptive.Assignment
		result     adaptive.Result
	}{
		{adaptive.Assignment{InvocationID: "p", AgentID: "planner", Binding: "runtime:codex:p::", Role: "planner", Runtime: "codex"}, adaptive.Result{InvocationID: "p", AgentID: "planner", Outcome: "planned", Revision: "plan"}},
		{adaptive.Assignment{InvocationID: "i", AgentID: "builder", Binding: "runtime:codex:b::", Role: "implementer", Runtime: "codex", Revision: "plan"}, adaptive.Result{InvocationID: "i", AgentID: "builder", Outcome: "changed", Revision: "diff"}},
	} {
		if err := st.Assign(step.assignment); err != nil {
			t.Fatal(err)
		}
		if err := st.Apply(step.result); err != nil {
			t.Fatal(err)
		}
	}
	if err := adaptive.ApplyVerificationResult(&st, adaptive.VerificationRecord{
		Status: adaptive.VerificationStatusPassed, AllPassed: true,
		CandidateFingerprint: "diff",
		Receipts: []adaptive.CheckReceipt{{
			CheckID: "ok", Passed: true, CandidateFingerprint: "diff", WorktreeIdentity: "wt",
		}},
	}, a.now()); err != nil {
		t.Fatal(err)
	}
	store := ledger.Open(a.sdlcRunsDir(), runID)
	run := ledger.Run{
		RunID: runID, WorkDir: a.WorkDir, Workflow: "feature", Task: "review concurrently",
		CreatedAt: now, UpdatedAt: now, Adaptive: &st,
		TreeUsage: &ledger.TreeUsage{Assignments: st.AssignmentCount, Revisions: st.RevisionCount},
	}
	if err := store.WriteRun(run); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteArtifact(adaptive.ArtifactPlan, []byte("# Plan\n")); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteArtifact("patch.diff", []byte("diff --git a/a b/a\n+line\n")); err != nil {
		t.Fatal(err)
	}
	return run
}

func TestParallelAssessorsLaunchConcurrently(t *testing.T) {
	a := newApp(t)
	a.Stdout = io.Discard
	collaborativeReviewRoster(t, a)
	id := "parallel-assess"
	_ = seedAssessingRun(t, a, id, 2, 2)
	gate := make(chan struct{}, 2)
	exec := &concurrentReviewExecutor{
		gate: gate,
		byAgent: map[string]worker.Reply{
			"reviewer-a": {Outcome: "approved"},
			"reviewer-b": {Outcome: "approved"},
			"reviewer-c": {Outcome: "approved"},
		},
		defaultR: worker.Reply{Outcome: "approved"},
	}
	a.SdlcExecutor = exec

	done := make(chan error, 1)
	go func() { done <- a.sdlcDrive(context.Background(), id) }()

	for i := 0; i < 2; i++ {
		select {
		case <-gate:
		case <-time.After(2 * time.Second):
			t.Fatalf("assessors did not overlap; started=%d max=%d", exec.started.Load(), exec.maxSeen.Load())
		}
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("drive hung")
	}
	if exec.maxSeen.Load() < 2 {
		t.Fatalf("expected concurrent assessors, max in flight=%d", exec.maxSeen.Load())
	}
	stored, err := ledger.Open(a.sdlcRunsDir(), id).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if stored.Adaptive.Stage != adaptive.Done || len(stored.Adaptive.Assessments) != 2 {
		t.Fatalf("quorum: %+v", stored.Adaptive)
	}
}

func TestParallelAssessorEarlyCancellationChargesUsage(t *testing.T) {
	a := newApp(t)
	a.Stdout = io.Discard
	collaborativeReviewRoster(t, a)
	id := "early-cancel"
	_ = seedAssessingRun(t, a, id, 2, 2)
	release := make(chan struct{})
	var started atomic.Int32
	exec := &firstRejectBlocksExecutor{release: release, started: &started}
	a.SdlcExecutor = exec

	done := make(chan error, 1)
	go func() { done <- a.sdlcDrive(context.Background(), id) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("drive hung waiting for cancelled reviewer")
	}
	close(release)
	if started.Load() < 2 {
		t.Fatalf("expected concurrent launch before cancel, started=%d", started.Load())
	}

	stored, err := ledger.Open(a.sdlcRunsDir(), id).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if stored.Adaptive.Stage != adaptive.Implementing || len(stored.Adaptive.Assignments) != 0 {
		t.Fatalf("expected implementing after rejection: %+v", stored.Adaptive)
	}
	decisions, err := ledger.Open(a.sdlcRunsDir(), id).ReadDecisions()
	if err != nil {
		t.Fatal(err)
	}
	cancelled := 0
	for _, d := range decisions {
		if d.Kind == "invocation-outcome" && d.Choice == "cancelled" {
			cancelled++
		}
	}
	if cancelled == 0 {
		t.Fatalf("expected cancelled review decision, got %+v", decisions)
	}
}

type firstRejectBlocksExecutor struct {
	mu       sync.Mutex
	started  *atomic.Int32
	release  chan struct{}
	requests []worker.Request
}

func (f *firstRejectBlocksExecutor) Execute(ctx context.Context, req worker.Request) (worker.Reply, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	n := f.started.Add(1)
	if n == 1 {
		return worker.Reply{Outcome: "changes-required", Content: "needs work", CostUSD: 0.1, CostReported: true}, nil
	}
	select {
	case <-f.release:
		return worker.Reply{Outcome: "approved", CostUSD: 0.2, CostReported: true}, nil
	case <-ctx.Done():
		return worker.Reply{CostUSD: 0.2, CostReported: true}, ctx.Err()
	}
}

func TestParallelAssessorRestartLaunchesPending(t *testing.T) {
	a := newApp(t)
	a.Stdout = io.Discard
	collaborativeReviewRoster(t, a)
	id := "restart-pending"
	runRec := seedAssessingRun(t, a, id, 2, 2)
	st := *runRec.Adaptive
	for _, asg := range []adaptive.Assignment{
		{InvocationID: "ra", AgentID: "reviewer-a", Binding: "runtime:cursor:ra::", Role: "assessor", Runtime: "cursor", Revision: "diff"},
		{InvocationID: "rb", AgentID: "reviewer-b", Binding: "runtime:opencode:rb::", Role: "assessor", Runtime: "opencode", Revision: "diff"},
	} {
		if err := st.Assign(asg); err != nil {
			t.Fatal(err)
		}
	}
	runRec.Adaptive = &st
	runRec.TreeUsage = &ledger.TreeUsage{Assignments: st.AssignmentCount, Revisions: st.RevisionCount}
	if err := ledger.Open(a.sdlcRunsDir(), id).WriteRun(runRec); err != nil {
		t.Fatal(err)
	}
	exec := &concurrentReviewExecutor{
		byAgent: map[string]worker.Reply{
			"reviewer-a": {Outcome: "approved"},
			"reviewer-b": {Outcome: "approved"},
		},
		defaultR: worker.Reply{Outcome: "approved"},
	}
	a.SdlcExecutor = exec
	if err := a.sdlcDrive(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if exec.started.Load() != 2 {
		t.Fatalf("restart should launch pending reviews, started=%d", exec.started.Load())
	}
	stored, err := ledger.Open(a.sdlcRunsDir(), id).ReadRun()
	if err != nil || stored.Adaptive.Stage != adaptive.Done {
		t.Fatalf("restart: %+v %v", stored.Adaptive, err)
	}
}

func TestParallelAssessorCandidateInvalidationDropsStaleReviews(t *testing.T) {
	a := newApp(t)
	a.Stdout = io.Discard
	collaborativeReviewRoster(t, a)
	id := "invalidate-candidate"
	runRec := seedAssessingRun(t, a, id, 2, 2)
	st := *runRec.Adaptive
	if err := st.Assign(adaptive.Assignment{InvocationID: "ra", AgentID: "reviewer-a", Binding: "runtime:cursor:ra::", Role: "assessor", Runtime: "cursor", Revision: "diff"}); err != nil {
		t.Fatal(err)
	}
	adaptive.InvalidateOnCandidateChange(&st)
	st.DiffRevision = "diff-2"
	st.Stage = adaptive.Assessing
	st.CheckReceipts = []adaptive.CheckReceipt{{
		CheckID: "ok", Passed: true, CandidateFingerprint: "diff-2", WorktreeIdentity: "wt",
	}}
	runRec.Adaptive = &st
	if err := ledger.Open(a.sdlcRunsDir(), id).WriteRun(runRec); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Open(a.sdlcRunsDir(), id).WriteArtifact("patch.diff", []byte("diff --git a/a b/a\n+new\n")); err != nil {
		t.Fatal(err)
	}
	err := a.sdlcRecordResult(id, adaptive.Result{
		InvocationID: "ra", AgentID: "reviewer-a", Outcome: "approved", Revision: "diff", CostUSD: 0.05,
	}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	decisions, err := ledger.Open(a.sdlcRunsDir(), id).ReadDecisions()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range decisions {
		if d.Invocation == "ra" && d.Choice == "cancelled" {
			found = true
		}
	}
	if !found {
		t.Fatalf("stale review should be cancelled after invalidation: %+v", decisions)
	}
	stored, err := ledger.Open(a.sdlcRunsDir(), id).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Adaptive.Assessments) != 0 {
		t.Fatalf("stale assessment counted: %+v", stored.Adaptive.Assessments)
	}
}

func TestParallelSpecialistsLaunchTogether(t *testing.T) {
	a := newApp(t)
	a.Stdout = io.Discard
	a.SdlcReach = func() enrollment.Reach {
		return enrollment.Reach{Driver: "cli", Runtimes: map[string]enrollment.RuntimeCapability{
			"codex": {Write: true, ReadOnly: true}, "cursor": {Write: true}, "opencode": {Write: true, ReadOnly: true},
		}}
	}
	writeFile(t, a.sdlcPolicyPath(), "version: 1\nmaxConcurrent: 3\nspecialistMode: advisory\n")
	writeFile(t, a.sdlcRosterPath(), `version: 1
agents:
  - {id: planner, roles: [planner], rubric: Plan., via: runtime, runtime: codex, model: p}
  - {id: builder, roles: [implementer], rubric: Build., via: runtime, runtime: cursor, model: b}
  - {id: reviewer, roles: [assessor], rubric: Review., via: runtime, runtime: cursor, model: r}
  - {id: qa-agent, roles: [qa], rubric: QA., via: runtime, runtime: opencode, model: q, readOnly: true}
  - {id: sec-agent, roles: [security], rubric: Security., via: runtime, runtime: codex, model: s, readOnly: true}
`)
	st, err := adaptive.New("feature", "lean", 1, 3, 3)
	if err != nil {
		t.Fatal(err)
	}
	st.Stage = adaptive.Specializing
	st.DiffRevision = "diff"
	st.PlanRevision = "plan"
	st.LastImplementerBinding = "runtime:cursor:b::"
	st.SpecialistQueue = []adaptive.SpecialistCheck{
		{Role: "qa", Revision: "diff"},
		{Role: "security", Revision: "diff"},
	}
	st.AfterSpecialists = adaptive.Verifying
	now := a.now().UTC().Format(time.RFC3339)
	id := "parallel-specialists"
	store := ledger.Open(a.sdlcRunsDir(), id)
	if err := store.WriteRun(ledger.Run{
		RunID: id, WorkDir: a.WorkDir, Workflow: "feature", Task: "specialists",
		CreatedAt: now, UpdatedAt: now, Adaptive: &st, TreeUsage: &ledger.TreeUsage{Assignments: 1},
	}); err != nil {
		t.Fatal(err)
	}
	_ = store.WriteArtifact(adaptive.ArtifactPlan, []byte("# Plan\n"))
	_ = store.WriteArtifact("patch.diff", []byte("diff --git a/a b/a\n+line\n"))

	gate := make(chan struct{}, 2)
	exec := &concurrentReviewExecutor{
		gate: gate,
		byAgent: map[string]worker.Reply{
			"qa-agent":  {Outcome: "approved"},
			"sec-agent": {Outcome: "approved"},
		},
		defaultR: worker.Reply{Outcome: "approved"},
	}
	a.SdlcExecutor = exec
	done := make(chan error, 1)
	go func() { done <- a.sdlcDrive(context.Background(), id) }()
	for i := 0; i < 2; i++ {
		select {
		case <-gate:
		case err := <-done:
			t.Fatalf("drive ended early: %v started=%d", err, exec.started.Load())
		case <-time.After(2 * time.Second):
			t.Fatalf("specialists did not overlap; started=%d", exec.started.Load())
		}
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("drive hung")
	}
	if exec.maxSeen.Load() < 2 {
		t.Fatalf("expected concurrent specialists, max=%d reqs=%v", exec.maxSeen.Load(), agentIDs(exec))
	}
	stored, err := store.ReadRun()
	if err != nil || stored.Adaptive.Stage != adaptive.Verifying {
		t.Fatalf("specialists: %+v %v", stored.Adaptive, err)
	}
}

func agentIDs(exec *concurrentReviewExecutor) []string {
	exec.mu.Lock()
	defer exec.mu.Unlock()
	out := make([]string, 0, len(exec.requests))
	for _, r := range exec.requests {
		out = append(out, r.Agent.ID)
	}
	return out
}

func TestParallelReviewsDoNotAdvanceWhilePending(t *testing.T) {
	a := newApp(t)
	a.Stdout = io.Discard
	collaborativeReviewRoster(t, a)
	id := "pending-gate"
	_ = seedAssessingRun(t, a, id, 2, 2)
	release := make(chan struct{})
	exec := &concurrentReviewExecutor{
		release: release,
		blockOn: map[string]bool{"reviewer-a": true, "reviewer-b": true, "reviewer-c": true},
		byAgent: map[string]worker.Reply{
			"reviewer-a": {Outcome: "approved"},
			"reviewer-b": {Outcome: "approved"},
		},
		defaultR: worker.Reply{Outcome: "approved"},
	}
	a.SdlcExecutor = exec
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.sdlcDrive(ctx, id) }()
	time.Sleep(50 * time.Millisecond)
	stored, err := ledger.Open(a.sdlcRunsDir(), id).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if stored.Adaptive.Stage != adaptive.Assessing || len(stored.Adaptive.Assignments) == 0 {
		t.Fatalf("should stay assessing while reviews pending: %+v", stored.Adaptive)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("drive did not stop")
	}
	close(release)
}
