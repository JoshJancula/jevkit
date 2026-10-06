package sdlc

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/cmd/jevkit/internal/testkit"
	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/enrollment"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/sdlc/spec"
	"github.com/JoshJancula/jevkit/internal/sdlc/stageflow"
	"github.com/JoshJancula/jevkit/internal/sdlc/worker"
)

func TestBudgetResumeSameRunPreservesProgressAndSnapshot(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	testkit.WriteFile(t, a.sdlcPolicyPath(), "version: 1\nmaxAssignments: 1\n")
	f := &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "planned", Content: "Saved plan.", SessionID: "planner-session"}, {Outcome: "changed", Content: "diff --git a/a b/a\n+new\n"}, {Outcome: "approved"}}}
	a.SdlcExecutor = f
	code, out, errs := run(a, "", "sdlc", "run", "feature", "--task", "implement", "--auto")
	if code != app.ExitOK {
		t.Fatalf("start: %d %s %s", code, out, errs)
	}
	id := strings.Fields(out)[1]
	store := ledger.Open(a.SDLCRunsDir(), id)
	before, err := store.ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	plan, _ := store.ReadArtifact("plan.md")
	if before.Adaptive.Outcome != "assignment-budget-exhausted" || len(before.Sessions) != 1 {
		t.Fatalf("checkpoint: %+v", before)
	}
	code, out, errs = run(a, "", "sdlc", "resume", id)
	if code == app.ExitOK || !strings.Contains(out+errs, "--add-assignments 1") {
		t.Fatalf("plain resume approved budget: %d %s %s", code, out, errs)
	}
	// Project edits affect new runs, not this run's allowance.
	testkit.WriteFile(t, a.sdlcPolicyPath(), "version: 1\nmaxAssignments: 99\n")
	code, _, _ = run(a, "", "sdlc", "resume", id)
	if code == app.ExitOK {
		t.Fatal("project edit silently extended existing run")
	}
	code, out, errs = run(a, "", "sdlc", "resume", id, "--add-assignments", "2")
	if code != app.ExitOK {
		t.Fatalf("extend: %d %s %s", code, out, errs)
	}
	after, _ := store.ReadRun()
	savedPlan, _ := store.ReadArtifact("plan.md")
	b, _ := store.ReadBudget()
	if after.Adaptive.Stage != adaptive.Done || string(plan) != string(savedPlan) || len(after.Sessions) < 1 || b.Usage.Assignments != 3 || b.Original.Assignments != 1 || b.Limits.Assignments != 3 || len(b.Extensions) != 1 {
		t.Fatalf("continuation: %+v budget=%+v", after, b)
	}
	if len(f.requests) != 3 {
		t.Fatalf("repeated successful work: %d", len(f.requests))
	}
}

func TestBudgetAdmittedParallelResultsDrainAndComplete(t *testing.T) {
	a := newApp(t)
	a.Stdout = io.Discard
	collaborativeReviewRoster(t, a)
	r := seedAssessingRun(t, a, "budget-drain", 2, 2)
	p := enrollment.DefaultPolicy()
	p.MaxAssignments = 4
	p.MaxEstimatedCostUSD = 1
	r.Adaptive.MaxAssignments = 4
	r.Adaptive.MaxEstimatedCostUSD = 1
	snapshotBudget(&r, p)
	store := ledger.Open(a.SDLCRunsDir(), r.RunID)
	if err := store.WriteRun(r); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"one", "two"} {
		x := adaptive.Assignment{InvocationID: id, AgentID: id, Binding: id, Role: "assessor", Revision: "diff"}
		if err := a.reserveAssignment(&r, p, x, false); err != nil {
			t.Fatal(err)
		}
		if err := r.Adaptive.Assign(x); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.WriteRun(r); err != nil {
		t.Fatal(err)
	}
	if err := a.sdlcRecordResult(r.RunID, adaptive.Result{InvocationID: "one", AgentID: "one", Outcome: "approved", Revision: "diff", CostUSD: 1.5}, "responses/one.txt", []byte("review one")); err != nil {
		t.Fatal(err)
	}
	r, _ = store.ReadRun()
	if r.Adaptive.Stage != adaptive.Draining || len(r.Adaptive.Assignments) != 1 || len(r.Adaptive.Assessments) != 1 {
		t.Fatalf("lost admitted work: %+v", r.Adaptive)
	}
	if err := a.sdlcRecordResult(r.RunID, adaptive.Result{InvocationID: "two", AgentID: "two", Outcome: "approved", Revision: "diff"}, "responses/two.txt", []byte("review two")); err != nil {
		t.Fatal(err)
	}
	r, _ = store.ReadRun()
	if r.Adaptive.Stage != adaptive.Done || len(r.Adaptive.Assessments) != 2 {
		t.Fatalf("valid completion lost: %+v", r.Adaptive)
	}
	b, _ := store.ReadBudget()
	if b.Usage.CostUSD != 1.5 || b.Usage.Assignments != 4 {
		t.Fatalf("usage: %+v", b.Usage)
	}
}

func TestBudgetAuthoredTransitionContinuesWithoutRepeatingQuestion(t *testing.T) {
	a := newApp(t)
	a.Stdout = io.Discard
	s, _ := adaptive.New("authored", "lean", 1, 1, 3)
	w := spec.Workflow{Version: 1, Name: "authored", Description: "Budget checkpoint", Entry: "ask", MaxSteps: 1, Stages: []spec.Stage{{ID: "ask", Question: &spec.StageQuestion{Prompt: "Finish?", Options: map[string]string{"yes": "Finish", "no": "Stay"}, Routes: map[string]string{"yes": "done", "no": "ask"}, Fallback: "done"}}, {ID: "done", Finish: "succeeded"}}}
	f, err := stageflow.New(w, &s)
	if err != nil {
		t.Fatal(err)
	}
	f.Steps = 1
	r := ledger.Run{RunID: "authored-budget", Adaptive: &s, StageFlow: &f, CreatedAt: a.Clock().Format(time.RFC3339), TreeUsage: &ledger.TreeUsage{StageSteps: 1}}
	snapshotBudget(&r, enrollment.DefaultPolicy())
	store := ledger.Open(a.SDLCRunsDir(), r.RunID)
	if err := store.WriteRun(r); err != nil {
		t.Fatal(err)
	}
	if err := a.advanceBudgetFlow(&r, &s, "yes"); err != nil {
		t.Fatal(err)
	}
	if s.Stage != adaptive.Paused || f.PendingAnswer != "yes" || f.Current != "ask" || len(f.Transitions) != 0 {
		t.Fatalf("checkpoint: %+v %+v", s, f)
	}
	if err := a.extendBudget(r.RunID, ledger.Allowances{Steps: 1}, 0, "operator"); err != nil {
		t.Fatal(err)
	}
	if err := a.continueBudgetTree(r.RunID, false); err != nil {
		t.Fatal(err)
	}
	r, _ = store.ReadRun()
	if r.Adaptive.Stage != adaptive.Done || r.StageFlow.PendingAnswer != "" || r.StageFlow.Steps != 2 || len(r.StageFlow.Transitions) != 1 {
		t.Fatalf("replayed/skipped transition: %+v", r)
	}
}

func TestBudgetInvalidGrantsAndInteractiveCancel(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	for _, args := range [][]string{{"--add-assignments", "0"}, {"--add-revisions", "-1"}, {"--add-cost-usd", "NaN"}, {"--add-cost-usd", "+Inf"}, {"--add-time", "0s"}, {"--add-steps", "-1"}, {"--add-children", "0"}, {"--invocation-timeout", "-1s"}} {
		code, _, _ := run(a, "", append([]string{"sdlc", "resume", "missing"}, args...)...)
		if code != app.ExitUsage {
			t.Fatalf("invalid values: %v code=%d", args, code)
		}
	}
	s, _ := adaptive.New("feature", "lean", 1, 1, 3)
	s.MaxAssignments = 1
	s.AssignmentCount = 1
	s.Pause("assignment-budget-exhausted")
	r := ledger.Run{RunID: "cancel-budget", Adaptive: &s, CreatedAt: a.Clock().Format(time.RFC3339)}
	snapshotBudget(&r, enrollment.DefaultPolicy())
	store := ledger.Open(a.SDLCRunsDir(), r.RunID)
	if err := store.WriteRun(r); err != nil {
		t.Fatal(err)
	}
	a.Stdin = strings.NewReader("\nno\n2\nyes\n")
	a.Stdout = io.Discard
	ok, err := a.askBudgetExtension(context.Background(), r.RunID)
	if err != nil || ok {
		t.Fatalf("cancel: %v %v", ok, err)
	}
	if _, err := store.ReadBudget(); !os.IsNotExist(err) {
		t.Fatalf("cancel mutated budget: %v", err)
	}
	ok, err = a.askBudgetExtension(context.Background(), r.RunID)
	if err != nil || !ok {
		t.Fatalf("approve: %v %v", ok, err)
	}
	b, _ := store.ReadBudget()
	if b.Limits.Assignments != 3 || len(b.Extensions) != 1 {
		t.Fatalf("edited grant: %+v", b)
	}
}

func TestBudgetSuggestionsCoverOverageAndEveryBlocker(t *testing.T) {
	b := ledger.NewBudget(ledger.Allowances{Assignments: 20, Revisions: 3, Seconds: 21600, CostUSD: 1, Steps: 20, Children: 8}, time.Now(), 1800)
	b.Usage = ledger.Allowances{Assignments: 26, Revisions: 5, Seconds: 27001, CostUSD: 1.99, Steps: 25, Children: 11}
	add := extensionSuggestion(b, []string{"assignments", "revisions", "time", "cost-usd", "steps", "children"})
	if add.Assignments != 7 || add.Revisions != 3 || add.Seconds != 5520 || add.CostUSD != 1 || add.Steps != 6 || add.Children != 4 {
		t.Fatalf("suggestion: %+v", add)
	}
}

func TestBudgetDriverActivityAndSnapshotTimeout(t *testing.T) {
	a := newApp(t)
	a.Stdout = io.Discard
	now := time.Unix(100, 0)
	a.Now = func() time.Time { return now }
	s, _ := adaptive.New("feature", "lean", 1, 1, 3)
	s.MaxAssignments = 20
	r := ledger.Run{RunID: "owned-budget", Adaptive: &s, CreatedAt: now.Format(time.RFC3339)}
	p := enrollment.DefaultPolicy()
	p.MaxRunSeconds = 1
	snapshotBudget(&r, p)
	store := ledger.Open(a.SDLCRunsDir(), r.RunID)
	if err := store.WriteRun(r); err != nil {
		t.Fatal(err)
	}
	stop, err := a.budgetActivity(context.Background(), r, "supervisor")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.budgetActivity(context.Background(), r, "supervisor"); err == nil {
		t.Fatal("second driver stole activity")
	}
	now = now.Add(time.Hour)
	stop()
	b, _ := store.ReadBudget()
	if b.Usage.Seconds != 3600 {
		t.Fatalf("observed active work lost: %v", b.Usage.Seconds)
	}
	now = now.Add(24 * time.Hour)
	b, err = a.budgetView(r, p)
	if err != nil || b.Usage.Seconds != 3600 {
		t.Fatalf("offline charged: %+v %v", b, err)
	}
	if got := a.invocationTimeout(r, p); got != 30*time.Minute {
		t.Fatalf("run remaining shortened invocation: %s", got)
	}
	if err := a.extendBudget(r.RunID, ledger.Allowances{}, 45*time.Minute, "override"); err != nil {
		t.Fatal(err)
	}
	if got := a.invocationTimeout(r, p); got != 45*time.Minute {
		t.Fatalf("override: %s", got)
	}
}

func TestBudgetChildExtensionKeepsApprovalsSessionsAndUsage(t *testing.T) {
	a := newApp(t)
	a.Stdout = io.Discard
	s, _ := adaptive.New("feature", "lean", 1, 1, 3)
	s.MaxAssignments = 1
	r := ledger.Run{RunID: "root-extension", Adaptive: &s, CreatedAt: a.Clock().Format(time.RFC3339), TreeUsage: &ledger.TreeUsage{Assignments: 1}}
	snapshotBudget(&r, enrollment.DefaultPolicy())
	root := ledger.Open(a.SDLCRunsDir(), r.RunID)
	if err := root.WriteRun(r); err != nil {
		t.Fatal(err)
	}
	childState := s
	childState.Stage = adaptive.Implementing
	childState.Pause("assignment-budget-exhausted")
	child := ledger.Run{RunID: "child-extension", ParentRunID: r.RunID, Adaptive: &childState, CreatedAt: r.CreatedAt, ApprovedPlanRevision: "plan", ApprovedChecksRevision: "checks", AuthorizedChecksRevision: "checks", ApprovedSubtasksRevision: "graph", Sessions: map[string]string{"binding/implementer": "session"}, OperatorGuidance: "saved guidance"}
	childStore := ledger.Open(a.SDLCRunsDir(), child.RunID)
	if err := childStore.WriteRun(child); err != nil {
		t.Fatal(err)
	}
	if err := a.extendBudget(child.RunID, ledger.Allowances{Assignments: 5}, 0, "operator child grant"); err != nil {
		t.Fatal(err)
	}
	if err := a.continueBudgetTree(child.RunID, false); err != nil {
		t.Fatal(err)
	}
	got, _ := childStore.ReadRun()
	b, _ := root.ReadBudget()
	if got.Adaptive.Stage != adaptive.Implementing || got.ApprovedPlanRevision != "plan" || got.AuthorizedChecksRevision != "checks" || got.Sessions["binding/implementer"] != "session" || got.OperatorGuidance != "saved guidance" || b.Usage.Assignments != 1 || b.Limits.Assignments != 6 || b.Extensions[0].RunID != child.RunID {
		t.Fatalf("lost state: %+v %+v", got, b)
	}
	if _, err := childStore.ReadBudget(); !os.IsNotExist(err) {
		t.Fatal("child created a competing budget")
	}
}

func TestBudgetLegacyTimeAndObservationalGuidance(t *testing.T) {
	a := newApp(t)
	a.Stdout = io.Discard
	s, _ := adaptive.New("feature", "lean", 1, 1, 3)
	s.MaxAssignments = 20
	s.AssignmentCount = 20
	s.Pause("assignment-budget-exhausted")
	r := ledger.Run{RunID: "legacy-budget", Adaptive: &s, CreatedAt: a.Clock().Add(-24 * time.Hour).Format(time.RFC3339)}
	store := ledger.Open(a.SDLCRunsDir(), r.RunID)
	if err := store.WriteRun(r); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(store.Dir + "/run.json")
	text := a.sdlcRecoveryNext(r, r.RunID)
	if !strings.Contains(text, "--add-assignments 5") || !strings.Contains(text, "--add-time 90m") || !strings.Contains(text, "estimated") || strings.Contains(text, "start a new") {
		t.Fatalf("guidance: %s", text)
	}
	after, _ := os.ReadFile(store.Dir + "/run.json")
	if string(before) != string(after) {
		t.Fatal("show changed run")
	}
	if _, err := store.ReadBudget(); !os.IsNotExist(err) {
		t.Fatal("show initialized ledger")
	}
	if _, err := a.updateBudget(r, enrollment.DefaultPolicy(), func(*ledger.Budget) error { return nil }); err != nil {
		t.Fatal(err)
	}
	b, _ := store.ReadBudget()
	if !b.TimeEstimated || b.Usage.Seconds != 21600 || b.Usage.Assignments != 20 {
		t.Fatalf("migration: %+v", b)
	}
}
