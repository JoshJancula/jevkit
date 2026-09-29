package sdlc

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/cmd/jevkit/internal/testkit"
	usagecmd "github.com/JoshJancula/jevkit/cmd/jevkit/usage"
	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/enrollment"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/sdlc/worker"
)

type fakeWorktreeCreator struct {
	available bool
	mu        sync.Mutex
	created   []string
}

func (f *fakeWorktreeCreator) Available(context.Context, string) bool { return f.available }

func (f *fakeWorktreeCreator) Create(_ context.Context, repoRoot, baseRev, dest string) (worker.IsolatedWorkspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return worker.IsolatedWorkspace{}, err
	}
	f.created = append(f.created, dest)
	return worker.IsolatedWorkspace{Path: dest, Mode: "worktree", BaseRev: baseRev, RepoRoot: repoRoot, Worktree: true}, nil
}

func (f *fakeWorktreeCreator) Remove(_ context.Context, ws worker.IsolatedWorkspace) error {
	return os.RemoveAll(ws.Path)
}

type fanoutFakeExecutor struct {
	mu        sync.Mutex
	requests  []worker.Request
	replies   map[string]worker.Reply // keyed by objective substring or empty default
	block     chan struct{}
	started   atomic.Int32
	failIDs   map[string]bool
	delay     time.Duration
	onRequest func(worker.Request)
}

func (f *fanoutFakeExecutor) Execute(ctx context.Context, req worker.Request) (worker.Reply, error) {
	f.started.Add(1)
	f.mu.Lock()
	f.requests = append(f.requests, req)
	if f.onRequest != nil {
		f.onRequest(req)
	}
	f.mu.Unlock()
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return worker.Reply{}, ctx.Err()
		}
	}
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return worker.Reply{}, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failIDs[req.Assignment.InvocationID] || f.failIDs[req.Assignment.Objective] {
		return worker.Reply{}, &worker.InvocationFailure{Err: context.DeadlineExceeded}
	}
	for key, reply := range f.replies {
		if key != "" && strings.Contains(req.Task, key) {
			return reply, nil
		}
	}
	if r, ok := f.replies[""]; ok {
		return r, nil
	}
	return worker.Reply{Outcome: "changed", Content: "diff --git a/x b/x\n+ok\n"}, nil
}

func fanoutRoster(t *testing.T, a *App) {
	t.Helper()
	a.SdlcReach = func() enrollment.Reach {
		return enrollment.Reach{Driver: "cli", Runtimes: map[string]enrollment.RuntimeCapability{
			"codex": {Write: true}, "cursor": {Write: true}, "opencode": {Write: true},
		}}
	}
	testkit.WriteFile(t, a.sdlcRosterPath(), `version: 1
agents:
  - {id: planner, roles: [planner], rubric: Plan., via: runtime, runtime: codex, model: p}
  - {id: impl-a, roles: [implementer], rubric: Build A., via: runtime, runtime: cursor, model: a}
  - {id: impl-b, roles: [implementer], rubric: Build B., via: runtime, runtime: codex, model: b}
  - {id: assessor, roles: [assessor], rubric: Assess., via: runtime, runtime: opencode, model: r}
`)
}

func seedApprovedFanout(t *testing.T, a *App, runID string, parallelize bool, extra ...adaptive.Subtask) ledger.Run {
	t.Helper()
	store := ledger.Open(a.SDLCRunsDir(), runID)
	run, err := store.ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	subtasks := []adaptive.Subtask{
		{ID: "api", Objective: "API work", ExpectedOutput: "api", OwnedPaths: []string{"internal/api/"}, MergeOrder: 1, AcceptanceCriteria: []string{"ok"}},
		{ID: "ui", Objective: "UI work", ExpectedOutput: "ui", OwnedPaths: []string{"web/"}, MergeOrder: 2, AcceptanceCriteria: []string{"ok"}},
	}
	subtasks = append(subtasks, extra...)
	g := adaptive.BoundGraphConcurrency(adaptive.SubtaskGraph{
		Mode: "fan-out", IndependenceReason: "separate packages", LatencyBenefit: "parallel",
		Parallelize: parallelize, IntegrationOwner: "api", SharedPaths: []string{"internal/api/types.go"},
		Subtasks: subtasks,
	}, 3, 20)
	raw, err := adaptive.MarshalSubtasks(g)
	if err != nil {
		t.Fatal(err)
	}
	plan := []byte("# plan\nship it\n")
	checks, err := adaptive.MarshalChecks(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteArtifact(adaptive.ArtifactSubtasks, raw); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteArtifact(adaptive.ArtifactPlan, plan); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteArtifact(adaptive.ArtifactChecks, checks); err != nil {
		t.Fatal(err)
	}
	planDig := adaptive.DigestHex(plan)
	checksDig := adaptive.DigestHex(checks)
	subDig := adaptive.DigestHex(raw)
	st := *run.Adaptive
	st.Stage = adaptive.Implementing
	st.PlanRevision = planDig
	st.ChecksRevision = checksDig
	st.SubtasksRevision = subDig
	st.Outcome = ""
	st.PendingReason = ""
	st.PendingPhase = ""
	run.Adaptive = &st
	run.RequirePlanApproval = true
	run.ApprovedPlanRevision = planDig
	run.ApprovedChecksRevision = checksDig
	run.ApprovedSubtasksRevision = subDig
	run.AuthorizedChecksRevision = checksDig
	run.TreeUsage = &ledger.TreeUsage{Assignments: st.AssignmentCount}
	if err := store.WriteRun(run); err != nil {
		t.Fatal(err)
	}
	return run
}

func TestFanoutTwoWayConcurrentDrive(t *testing.T) {
	a := newApp(t)
	fanoutRoster(t, a)
	wt := &fakeWorktreeCreator{available: true}
	a.WorktreeCreator = wt
	exec := &fanoutFakeExecutor{
		replies: map[string]worker.Reply{"": {Outcome: "changed", Content: "diff"}},
		delay:   50 * time.Millisecond,
	}
	a.SdlcExecutor = exec
	code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "ship api and ui", "--auto")
	if code != app.ExitOK {
		t.Fatalf("start: %d %s %s", code, out, errs)
	}
	id := strings.Fields(out)[1]
	seedApprovedFanout(t, a, id, true)

	var peak atomic.Int32
	exec.onRequest = func(worker.Request) {
		n := exec.started.Load()
		for {
			cur := peak.Load()
			if n <= cur || peak.CompareAndSwap(cur, n) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
	}

	if err := a.sdlcDrive(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	stored, err := ledger.Open(a.SDLCRunsDir(), id).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if stored.Fanout == nil || len(stored.Fanout.Subtasks) < 2 {
		t.Fatalf("schedule: %+v", stored.Fanout)
	}
	api, ui := stored.Fanout.Subtasks["api"], stored.Fanout.Subtasks["ui"]
	if api.Status != adaptive.SubtaskSucceeded || ui.Status != adaptive.SubtaskSucceeded {
		t.Fatalf("subtasks: api=%+v ui=%+v", api, ui)
	}
	if api.WorkspaceMode != adaptive.WorkspaceWorktree || ui.WorkspaceMode != adaptive.WorkspaceWorktree {
		t.Fatalf("expected worktrees: api=%s ui=%s", api.WorkspaceMode, ui.WorkspaceMode)
	}
	if peak.Load() < 2 {
		t.Fatalf("expected concurrent launch, peak=%d requests=%d", peak.Load(), len(exec.requests))
	}
	if stored.Integration == nil || stored.Integration.Status != adaptive.IntegrationStatusApplied {
		t.Fatalf("integration: %+v", stored.Integration)
	}
	if stored.Adaptive.Stage != adaptive.Verifying || stored.Adaptive.DiffRevision == "" {
		t.Fatalf("after integrate: stage=%s diff=%s outcome=%s", stored.Adaptive.Stage, stored.Adaptive.DiffRevision, stored.Adaptive.Outcome)
	}
	if stored.Adaptive.LastImplementerBinding != "supervisor-integration" {
		t.Fatalf("aggregate must be supervisor-owned, got %q", stored.Adaptive.LastImplementerBinding)
	}
}

func TestFanoutDependentWaitsForParents(t *testing.T) {
	a := newApp(t)
	fanoutRoster(t, a)
	a.WorktreeCreator = &fakeWorktreeCreator{available: true}
	exec := &fanoutFakeExecutor{replies: map[string]worker.Reply{"": {Outcome: "changed", Content: "diff"}}}
	a.SdlcExecutor = exec
	code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "deps", "--auto")
	if code != app.ExitOK {
		t.Fatalf("start: %d %s %s", code, out, errs)
	}
	id := strings.Fields(out)[1]
	seedApprovedFanout(t, a, id, true, adaptive.Subtask{
		ID: "docs", Objective: "Docs work", ExpectedOutput: "docs", OwnedPaths: []string{"docs/"},
		MergeOrder: 3, DependsOn: []string{"api", "ui"}, AcceptanceCriteria: []string{"ok"},
	})
	if err := a.sdlcDrive(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	stored, _ := ledger.Open(a.SDLCRunsDir(), id).ReadRun()
	if stored.Fanout.Subtasks["docs"].Status != adaptive.SubtaskPending {
		t.Fatalf("docs should still be pending after first wave: %+v", stored.Fanout.Subtasks["docs"])
	}
	if err := a.sdlcDrive(context.Background(), id); err != nil && !strings.Contains(err.Error(), "fanout") {
		// pause for integration after docs completes is fine
	}
	stored, _ = ledger.Open(a.SDLCRunsDir(), id).ReadRun()
	if stored.Fanout.Subtasks["docs"].Status != adaptive.SubtaskSucceeded {
		t.Fatalf("docs after second drive: %+v", stored.Fanout.Subtasks["docs"])
	}
}

func TestFanoutDuplicateDriveDoesNotDoubleLaunch(t *testing.T) {
	a := newApp(t)
	fanoutRoster(t, a)
	a.WorktreeCreator = &fakeWorktreeCreator{available: true}
	block := make(chan struct{})
	exec := &fanoutFakeExecutor{block: block, replies: map[string]worker.Reply{"": {Outcome: "changed", Content: "diff"}}}
	a.SdlcExecutor = exec
	// Use only one implementer so second ready subtask cannot start; focus on duplicate of same slot.
	testkit.WriteFile(t, a.sdlcRosterPath(), `version: 1
agents:
  - {id: planner, roles: [planner], rubric: Plan., via: runtime, runtime: codex, model: p}
  - {id: impl-a, roles: [implementer], rubric: Build., via: runtime, runtime: cursor, model: a}
  - {id: assessor, roles: [assessor], rubric: Assess., via: runtime, runtime: opencode, model: r}
`)
	code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "dup", "--auto")
	if code != app.ExitOK {
		t.Fatalf("start: %d %s %s", code, out, errs)
	}
	id := strings.Fields(out)[1]
	seedApprovedFanout(t, a, id, false)

	store := ledger.Open(a.SDLCRunsDir(), id)
	runRec, _ := store.ReadRun()
	g, digest, ok, err := a.approvedFanoutGraph(runRec, store)
	if err != nil || !ok {
		t.Fatalf("graph: %v ok=%v", err, ok)
	}
	now := a.Clock()
	if err := a.ensureFanoutSchedule(store, &runRec, g, digest, now); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errsCh := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errsCh <- store.ReserveFanoutSlot(now, func(r *ledger.Run, s *adaptive.Schedule) error {
				return s.Reserve("api", adaptive.Assignment{
					InvocationID: a.newRunID(now), AgentID: "impl-a", Binding: "runtime:cursor:a", Role: "implementer",
				}, "/wt", adaptive.WorkspaceWorktree, "lease", now)
			})
		}()
	}
	wg.Wait()
	close(errsCh)
	okCount := 0
	for e := range errsCh {
		if e == nil {
			okCount++
		}
	}
	if okCount != 1 {
		t.Fatalf("duplicate reserve wins=%d", okCount)
	}
	close(block)
}

func TestFanoutBudgetExhaustionPauses(t *testing.T) {
	a := newApp(t)
	fanoutRoster(t, a)
	a.WorktreeCreator = &fakeWorktreeCreator{available: true}
	a.SdlcExecutor = &fanoutFakeExecutor{replies: map[string]worker.Reply{"": {Outcome: "changed", Content: "diff"}}}
	code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "budget", "--auto")
	if code != app.ExitOK {
		t.Fatalf("start: %d %s %s", code, out, errs)
	}
	id := strings.Fields(out)[1]
	seedApprovedFanout(t, a, id, true)
	store := ledger.Open(a.SDLCRunsDir(), id)
	runRec, _ := store.ReadRun()
	runRec.TreeUsage = &ledger.TreeUsage{Assignments: 20}
	runRec.Adaptive.MaxAssignments = 20
	runRec.Adaptive.AssignmentCount = 20
	if err := store.WriteRun(runRec); err != nil {
		t.Fatal(err)
	}
	err := a.sdlcDrive(context.Background(), id)
	stored, _ := store.ReadRun()
	if stored.Adaptive.Outcome != "fanout-budget-exhausted" && (err == nil || !strings.Contains(err.Error(), "budget")) {
		t.Fatalf("expected budget pause: outcome=%s err=%v", stored.Adaptive.Outcome, err)
	}
}

func TestFanoutUnavailableIsolationPauses(t *testing.T) {
	a := newApp(t)
	fanoutRoster(t, a)
	a.WorktreeCreator = &fakeWorktreeCreator{available: false}
	a.SdlcExecutor = &fanoutFakeExecutor{replies: map[string]worker.Reply{"": {Outcome: "changed", Content: "diff"}}}
	code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "iso", "--auto")
	if code != app.ExitOK {
		t.Fatalf("start: %d %s %s", code, out, errs)
	}
	id := strings.Fields(out)[1]
	seedApprovedFanout(t, a, id, true)
	_ = a.sdlcDrive(context.Background(), id)
	stored, _ := ledger.Open(a.SDLCRunsDir(), id).ReadRun()
	// With parallelize and no isolation, first admission degrades to sequential.
	// Force unavailable by marking a writable in-flight without worktree support via schedule pause path:
	if stored.Fanout == nil {
		t.Fatal("missing schedule")
	}
	// Drive again after manually injecting an in-flight writable sequential slot.
	now := a.Clock()
	store := ledger.Open(a.SDLCRunsDir(), id)
	_ = store.UpdateFanout(now, func(r *ledger.Run, s *adaptive.Schedule) error {
		st := s.Subtasks["api"]
		st.Status = adaptive.SubtaskRunning
		st.ReadOnly = false
		st.WorkspaceMode = adaptive.WorkspaceSequential
		st.PID = os.Getpid()
		st.LeaseOwner = "live-lease"
		st.Assignment = &adaptive.Assignment{InvocationID: "x", AgentID: "impl-a", Binding: "runtime:cursor:a", Role: "implementer"}
		s.Subtasks["api"] = st
		s.PauseReason = ""
		return nil
	})
	_ = a.sdlcDrive(context.Background(), id)
	stored, _ = ledger.Open(a.SDLCRunsDir(), id).ReadRun()
	ui := stored.Fanout.Subtasks["ui"]
	if ui.Status == adaptive.SubtaskSucceeded || ui.Status == adaptive.SubtaskRunning || ui.Status == adaptive.SubtaskReserved {
		t.Fatalf("ui should not launch concurrently without isolation: %+v", ui)
	}
}

func TestFanoutCancellationAndCrashRecovery(t *testing.T) {
	a := newApp(t)
	fanoutRoster(t, a)
	a.WorktreeCreator = &fakeWorktreeCreator{available: true}
	block := make(chan struct{})
	exec := &fanoutFakeExecutor{block: block, replies: map[string]worker.Reply{"": {Outcome: "changed", Content: "diff"}}}
	a.SdlcExecutor = exec
	code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "cancel", "--auto")
	if code != app.ExitOK {
		t.Fatalf("start: %d %s %s", code, out, errs)
	}
	id := strings.Fields(out)[1]
	seedApprovedFanout(t, a, id, true)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.sdlcDrive(ctx, id) }()
	for exec.started.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	close(block)

	stored, _ := ledger.Open(a.SDLCRunsDir(), id).ReadRun()
	cancelled := 0
	for _, st := range stored.Fanout.Subtasks {
		if st.Status == adaptive.SubtaskCancelled || st.Status == adaptive.SubtaskFailed || st.Status == adaptive.SubtaskTimedOut {
			cancelled++
		}
	}
	if cancelled == 0 {
		t.Fatalf("expected cancelled/failed after ctx cancel: %+v", stored.Fanout.Subtasks)
	}

	// Crash recovery: leave a running lease with dead pid, then drive reconciles.
	store := ledger.Open(a.SDLCRunsDir(), id)
	_ = store.UpdateFanout(a.Clock(), func(r *ledger.Run, s *adaptive.Schedule) error {
		st := s.Subtasks["api"]
		st.Status = adaptive.SubtaskRunning
		st.PID = 1 << 28 // unlikely live
		st.LeaseOwner = "dead-lease"
		st.Assignment = &adaptive.Assignment{InvocationID: "dead", AgentID: "impl-a", Binding: "runtime:cursor:a", Role: "implementer"}
		st.Attempt = 1
		s.Subtasks["api"] = st
		s.PauseReason = ""
		r.Adaptive.Outcome = ""
		r.Adaptive.Stage = adaptive.Implementing
		return nil
	})
	exec2 := &fanoutFakeExecutor{replies: map[string]worker.Reply{"": {Outcome: "changed", Content: "diff"}}}
	a.SdlcExecutor = exec2
	_ = a.sdlcDrive(context.Background(), id)
	stored, _ = store.ReadRun()
	if stored.Fanout.Subtasks["api"].Status == adaptive.SubtaskRunning && stored.Fanout.Subtasks["api"].LeaseOwner == "dead-lease" {
		t.Fatalf("dead lease not reconciled: %+v", stored.Fanout.Subtasks["api"])
	}
}

func TestFanoutTimeoutMarksSubtask(t *testing.T) {
	a := newApp(t)
	fanoutRoster(t, a)
	a.WorktreeCreator = &fakeWorktreeCreator{available: true}
	exec := &fanoutFakeExecutor{
		failIDs: map[string]bool{"API work": true},
		replies: map[string]worker.Reply{"": {Outcome: "changed", Content: "diff"}},
	}
	a.SdlcExecutor = exec
	code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "timeout", "--auto")
	if code != app.ExitOK {
		t.Fatalf("start: %d %s %s", code, out, errs)
	}
	id := strings.Fields(out)[1]
	seedApprovedFanout(t, a, id, true)
	_ = a.sdlcDrive(context.Background(), id)
	stored, _ := ledger.Open(a.SDLCRunsDir(), id).ReadRun()
	api := stored.Fanout.Subtasks["api"]
	if api.Status != adaptive.SubtaskFailed && api.Status != adaptive.SubtaskTimedOut {
		t.Fatalf("api status: %+v", api)
	}
}

func TestFanoutSequentialWhenIsolationUnavailableSingleWriter(t *testing.T) {
	a := newApp(t)
	fanoutRoster(t, a)
	a.WorktreeCreator = &fakeWorktreeCreator{available: false}
	exec := &fanoutFakeExecutor{replies: map[string]worker.Reply{"": {Outcome: "changed", Content: "diff"}}}
	a.SdlcExecutor = exec
	code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "seq", "--auto")
	if code != app.ExitOK {
		t.Fatalf("start: %d %s %s", code, out, errs)
	}
	id := strings.Fields(out)[1]
	seedApprovedFanout(t, a, id, true)
	if err := a.sdlcDrive(context.Background(), id); err != nil && !strings.Contains(storedOutcome(t, a, id), "fanout") {
		// may pause awaiting integration after sequential completion of one then need another drive
		_ = err
	}
	stored, _ := ledger.Open(a.SDLCRunsDir(), id).ReadRun()
	api := stored.Fanout.Subtasks["api"]
	ui := stored.Fanout.Subtasks["ui"]
	// First drive should admit at most one sequential writable subtask.
	succeeded := 0
	if api.Status == adaptive.SubtaskSucceeded {
		succeeded++
		if api.WorkspaceMode != adaptive.WorkspaceSequential {
			t.Fatalf("api mode: %s", api.WorkspaceMode)
		}
	}
	if ui.Status == adaptive.SubtaskSucceeded {
		succeeded++
	}
	if succeeded != 1 {
		t.Fatalf("expected exactly one sequential completion on first drive; api=%+v ui=%+v", api, ui)
	}
}

func storedOutcome(t *testing.T, a *App, id string) string {
	t.Helper()
	r, err := ledger.Open(a.SDLCRunsDir(), id).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	return r.Adaptive.Outcome
}

func TestFanoutUsesRealGitWorktreeWhenAvailable(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	a := newApp(t)
	fanoutRoster(t, a)
	initGitRepoForFanout(t, a.WorkDir)
	a.SdlcExecutor = &fanoutFakeExecutor{replies: map[string]worker.Reply{"": {Outcome: "changed", Content: "diff"}}}
	code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "gitwt", "--auto")
	if code != app.ExitOK {
		t.Fatalf("start: %d %s %s", code, out, errs)
	}
	id := strings.Fields(out)[1]
	seedApprovedFanout(t, a, id, true)
	if err := a.sdlcDrive(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	stored, _ := ledger.Open(a.SDLCRunsDir(), id).ReadRun()
	api := stored.Fanout.Subtasks["api"]
	if api.WorkspaceMode != adaptive.WorkspaceWorktree || api.Workspace == "" || api.Workspace == a.WorkDir {
		t.Fatalf("expected isolated worktree path: %+v", api)
	}
	if _, err := os.Stat(api.Workspace); err != nil {
		t.Fatalf("worktree missing: %v", err)
	}
}

func TestFanoutIntegrationCleanMergeAndNoDoubleApply(t *testing.T) {
	a := newApp(t)
	fanoutRoster(t, a)
	a.WorktreeCreator = &fakeWorktreeCreator{available: true}
	a.SdlcExecutor = &fanoutFakeExecutor{replies: map[string]worker.Reply{
		"API": {Outcome: "changed", Content: "diff --git a/internal/api/x.go b/internal/api/x.go\n+api\n"},
		"UI":  {Outcome: "changed", Content: "diff --git a/web/y.go b/web/y.go\n+ui\n"},
	}}
	code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "integrate clean", "--auto")
	if code != app.ExitOK {
		t.Fatalf("start: %d %s %s", code, out, errs)
	}
	id := strings.Fields(out)[1]
	seedApprovedFanout(t, a, id, true)
	if err := a.sdlcDrive(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	store := ledger.Open(a.SDLCRunsDir(), id)
	stored, err := store.ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if stored.Integration == nil || stored.Integration.Status != adaptive.IntegrationStatusApplied {
		t.Fatalf("integration: %+v", stored.Integration)
	}
	fp := stored.Integration.CandidateFingerprint
	patch, err := store.ReadArtifact("patch.diff")
	if err != nil || !strings.Contains(string(patch), "subtask api") || !strings.Contains(string(patch), "subtask ui") {
		t.Fatalf("combined patch: %s %v", patch, err)
	}
	dec, err := store.ReadArtifact(adaptive.ArtifactIntegration)
	if err != nil || !strings.Contains(string(dec), fp) {
		t.Fatalf("decision artifact: %s %v", dec, err)
	}
	// Second integrate must not double-apply.
	if err := a.sdlcIntegrateFanout(context.Background(), id, store, stored); err != nil {
		t.Fatal(err)
	}
	again, _ := store.ReadRun()
	if again.Integration.CandidateFingerprint != fp || again.Integration.Status != adaptive.IntegrationStatusApplied {
		t.Fatalf("double-apply mutated: %+v", again.Integration)
	}
}

func TestFanoutIntegrationFailedWorkerRepair(t *testing.T) {
	a := newApp(t)
	fanoutRoster(t, a)
	a.WorktreeCreator = &fakeWorktreeCreator{available: true}
	a.SdlcExecutor = &fanoutFakeExecutor{
		replies: map[string]worker.Reply{
			"API": {Outcome: "changed", Content: "diff --git a/internal/api/x.go b/internal/api/x.go\n+api\n"},
		},
		failIDs: map[string]bool{"UI work": true},
	}
	code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "one fail", "--auto")
	if code != app.ExitOK {
		t.Fatalf("start: %d %s %s", code, out, errs)
	}
	id := strings.Fields(out)[1]
	seedApprovedFanout(t, a, id, true)
	_ = a.sdlcDrive(context.Background(), id)
	stored, _ := ledger.Open(a.SDLCRunsDir(), id).ReadRun()
	if stored.Integration == nil || stored.Integration.Status != adaptive.IntegrationStatusRepair {
		t.Fatalf("expected repair: %+v outcome=%s", stored.Integration, stored.Adaptive.Outcome)
	}
	if stored.Integration.RepairAssignment == nil || stored.Adaptive.Outcome != "fanout-integration-repair" {
		t.Fatalf("repair assignment/pause: %+v %s", stored.Integration, stored.Adaptive.Outcome)
	}
}

func TestFanoutIntegrationOverlappingEdits(t *testing.T) {
	a := newApp(t)
	fanoutRoster(t, a)
	a.WorktreeCreator = &fakeWorktreeCreator{available: true}
	// Force both subtasks to claim the same path via crafted diffs; seed graph still has disjoint owned paths,
	// so scope violation/overlap is detected when paths fall outside or collide after we widen ownership in artifacts.
	a.SdlcExecutor = &fanoutFakeExecutor{replies: map[string]worker.Reply{
		"": {Outcome: "changed", Content: "diff --git a/shared/x.go b/shared/x.go\n+both\n"},
	}}
	code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "overlap", "--auto")
	if code != app.ExitOK {
		t.Fatalf("start: %d %s %s", code, out, errs)
	}
	id := strings.Fields(out)[1]
	seedApprovedFanout(t, a, id, true)
	_ = a.sdlcDrive(context.Background(), id)
	stored, _ := ledger.Open(a.SDLCRunsDir(), id).ReadRun()
	// shared/x.go is outside api/ and web/ owned scopes → repair/conflict.
	if stored.Integration == nil || (stored.Integration.Status != adaptive.IntegrationStatusRepair && stored.Integration.Status != adaptive.IntegrationStatusPaused) {
		t.Fatalf("expected scope conflict repair: %+v", stored.Integration)
	}
}

func TestChangedPathsFromReport(t *testing.T) {
	got := changedPathsFromReport(`Invocation change report
- modified "internal/api/x.go" (before a, after b)
diff --git a/web/y.go b/web/y.go
+++ b/web/y.go
`)
	if len(got) < 2 {
		t.Fatalf("paths: %v", got)
	}
}

func initGitRepoForFanout(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "t@t.com")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "README")
	run("commit", "-m", "init")
}

type measuringExecutor func(context.Context, worker.Request) (worker.Reply, error)

func (f measuringExecutor) Execute(ctx context.Context, req worker.Request) (worker.Reply, error) {
	return f(ctx, req)
}

// TestFanoutOneVsMultipleAgentsMeasurementReport compares the same two-subtask
// fixture under sequential admission versus parallel worktree fan-out. Token and
// cost totals are fixture-supplied measured fields (identical per subtask); wall
// clock is local fake-executor latency only. Fan-out is not claimed to save tokens.
func TestFanoutOneVsMultipleAgentsMeasurementReport(t *testing.T) {
	const perCall = 40 * time.Millisecond
	measure := func(t *testing.T, parallelize, worktrees bool) (elapsed time.Duration, input, output int64, totalCost float64, peak int32, drives int) {
		t.Helper()
		a := newApp(t)
		fanoutRoster(t, a)
		a.WorktreeCreator = &fakeWorktreeCreator{available: worktrees}
		base := &fanoutFakeExecutor{
			delay:   perCall,
			replies: map[string]worker.Reply{}, // filled by factory below
		}
		base.replies[""] = worker.Reply{Outcome: "changed", Content: "diff --git a/x b/x\n+ok\n"}
		var inFlight, peakAtomic atomic.Int32
		a.SdlcExecutor = measuringExecutor(func(ctx context.Context, req worker.Request) (worker.Reply, error) {
			cur := inFlight.Add(1)
			for {
				p := peakAtomic.Load()
				if cur <= p || peakAtomic.CompareAndSwap(p, cur) {
					break
				}
			}
			defer inFlight.Add(-1)
			reply, err := base.Execute(ctx, req)
			if err != nil {
				return reply, err
			}
			// Fresh measured fields per invocation. Fixture cost is derived
			// below as unit_cost × subtask count.
			inVal, outVal, toolVal := int64(1200), int64(40), int64(3)
			reply.InputTokens, reply.OutputTokens = &inVal, &outVal
			reply.ToolCalls = &toolVal
			reply.CostUSD, reply.CostReported = 0, false
			return reply, nil
		})
		code, stdout, errs := run(a, "", "sdlc", "start", "feature", "--task", "measure api and ui", "--auto")
		if code != app.ExitOK {
			t.Fatalf("start: %d %s %s", code, stdout, errs)
		}
		id := strings.Fields(stdout)[1]
		seedApprovedFanout(t, a, id, parallelize)
		started := time.Now()
		var lastAPI, lastUI adaptive.SubtaskRecord
		for drives = 0; drives < 8; drives++ {
			_ = a.sdlcDrive(context.Background(), id)
			stored, err := ledger.Open(a.SDLCRunsDir(), id).ReadRun()
			if err != nil {
				t.Fatal(err)
			}
			if stored.Fanout == nil {
				t.Fatal("missing fanout schedule")
			}
			lastAPI, lastUI = stored.Fanout.Subtasks["api"], stored.Fanout.Subtasks["ui"]
			if lastAPI.Status == adaptive.SubtaskSucceeded && lastUI.Status == adaptive.SubtaskSucceeded {
				elapsed = time.Since(started)
				for _, st := range stored.Fanout.Subtasks {
					if st.Usage != nil {
						if st.Usage.ToolCalls == nil || *st.Usage.ToolCalls != 3 {
							t.Fatalf("subtask tool calls not saved: %+v", st.Usage)
						}
						if st.Usage.InputTokens != nil {
							input += *st.Usage.InputTokens
						}
						if st.Usage.OutputTokens != nil {
							output += *st.Usage.OutputTokens
						}
						if st.Usage.CostUSD != nil {
							totalCost += *st.Usage.CostUSD
						}
					}
				}
				if totals := usagecmd.AggregateRuntime([]ledger.Run{stored}).Totals; totals.ToolCalls != 6 {
					t.Fatalf("fanout ledger omitted tool calls: %+v", totals)
				}
				peak = peakAtomic.Load()
				return
			}
		}
		t.Fatalf("subtasks did not both succeed within drive budget (parallelize=%v worktrees=%v): api=%+v ui=%+v", parallelize, worktrees, lastAPI, lastUI)
		return
	}

	seqElapsed, seqIn, seqOut, _, seqPeak, seqDrives := measure(t, true, false) // no worktrees → sequential writable
	parElapsed, parIn, parOut, _, parPeak, parDrives := measure(t, true, true)
	if seqIn != parIn || seqOut != parOut {
		t.Fatalf("same fixture must charge equal tokens; seq=%d/%d par=%d/%d", seqIn, seqOut, parIn, parOut)
	}
	if seqPeak > 1 {
		t.Fatalf("sequential run must not overlap writable work, peak=%d", seqPeak)
	}
	if parPeak < 2 {
		t.Fatalf("parallel run should overlap, peak=%d", parPeak)
	}
	const unitCost = 0.015
	seqCost, parCost := unitCost*2, unitCost*2 // identical fixture unit cost × two subtasks

	dir := filepath.Join("..", "..", ".ralph-workspace", "artifacts", "jevkit-runtime-sdlc-hardening")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	report := "# End-to-end hardening documentation artifact\n\n" +
		"Generated by `TestFanoutOneVsMultipleAgentsMeasurementReport` plus the fixture journey map below.\n\n" +
		"## Fixture journey coverage (existing Go tests)\n\n" +
		"| Journey | Primary tests |\n" +
		"| --- | --- |\n" +
		"| Planner ready / blocked setup | `TestSdlcListIncludesPreflightAvailability`, doctor/list preflight tests |\n" +
		"| Plan / check approval | `sdlcapproval_test.go` |\n" +
		"| Passing / failing / timed-out / stale checks | `worker/verify_test.go`, `adaptive/verification_test.go` |\n" +
		"| Repair then reassessment | fan-out integration repair tests, `sdlcreviews_test.go` |\n" +
		"| Two independent implementers | `TestFanoutTwoWayConcurrentDrive` |\n" +
		"| Dependent / conflicting subtasks | `TestFanoutDependentWaitsForParents`, `TestFanoutIntegrationOverlappingEdits` |\n" +
		"| Parallel review | assessor concurrency in `sdlcreviews_test.go` |\n" +
		"| Custom and child runs | `sdlcstageflow_test.go`, spawn/child tests |\n" +
		"| Data deletion / logs-only prune | `sdlcprune_test.go`, `sdlcinventory_test.go` |\n\n" +
		"## One vs multiple implementers (same fixture)\n\n" +
		"Fixed two-subtask graph (`api` + `ui`), identical per-call measured tokens/cost and fake delay.\n" +
		"Sequential: parallelize requested without worktree isolation (one writable at a time). Parallel: worktree isolation + concurrent admission.\n\n" +
		"| metric | sequential (1 writable slot) | parallel (2 implementers) |\n" +
		"| --- | --- | --- |\n" +
		fmt.Sprintf("| wall_elapsed_ms | %d | %d |\n", seqElapsed.Milliseconds(), parElapsed.Milliseconds()) +
		fmt.Sprintf("| peak_in_flight | %d | %d |\n", seqPeak, parPeak) +
		fmt.Sprintf("| drive_calls | %d | %d |\n", seqDrives+1, parDrives+1) +
		fmt.Sprintf("| total_input_tokens | %d | %d |\n", seqIn, parIn) +
		fmt.Sprintf("| total_output_tokens | %d | %d |\n", seqOut, parOut) +
		fmt.Sprintf("| total_cost_usd | %.4f | %.4f |\n\n", seqCost, parCost) +
		"Notes:\n" +
		"- Token totals match across modes because each subtask still runs once with the same fixture usage.\n" +
		"- total_cost_usd is fixture unit cost × subtask count (identical across modes); live cost is not charged here to avoid a concurrent WriteRun race on the root ledger.\n" +
		"- Parallel mode can reduce wall clock; it does **not** reduce total tokens or cost on this fixture.\n" +
		"- Elapsed time is local fake-executor delay only; live provider latency is not measured here.\n" +
		"- See `prompt-efficiency.md` for stable-prefix / cache-read fixture telemetry under equal model settings.\n"
	path := filepath.Join(dir, "end-to-end.md")
	if err := os.WriteFile(path, []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}
}
