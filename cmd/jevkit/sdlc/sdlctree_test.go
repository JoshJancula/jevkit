package sdlc

import (
	"testing"
	"time"

	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/enrollment"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/sdlc/spec"
	"github.com/JoshJancula/jevkit/internal/sdlc/stageflow"
)

func TestDirectChildChargesAuthoritativeRoot(t *testing.T) {
	a := newApp(t)
	p := enrollment.DefaultPolicy()
	p.MaxAssignments = 1
	p.MaxRevisions = 1
	p.MaxEstimatedCostUSD = .5
	p.MaxRunSeconds = 60
	st, _ := adaptive.New("feature", "lean", 1, 1, 1)
	st.MaxAssignments = 1
	st.MaxEstimatedCostUSD = .5
	created := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	a.Now = func() time.Time { return created }
	root := ledger.Run{RunID: "root", Workflow: "feature", CreatedAt: created.Format(time.RFC3339), Adaptive: &st, StageFlow: &stageflow.State{Workflow: spec.Workflow{MaxSteps: 1}}}
	snapshotBudget(&root, p)
	child := ledger.Run{RunID: "child", ParentRunID: "root", Adaptive: &st}
	if err := ledger.Open(a.SDLCRunsDir(), "root").WriteRun(root); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Open(a.SDLCRunsDir(), "child").WriteRun(child); err != nil {
		t.Fatal(err)
	}
	first := adaptive.Assignment{InvocationID: "one", Role: "implementer"}
	if err := a.reserveAssignment(&child, p, first, false); err != nil {
		t.Fatal(err)
	}
	if err := a.reserveAssignment(&child, p, adaptive.Assignment{InvocationID: "two", Role: "implementer"}, false); err == nil {
		t.Fatal("overspent root")
	}
	if err := a.completeBudget(&child, "one", true, .6); err != nil {
		t.Fatal(err)
	}
	if err := a.completeBudget(&child, "one", true, .6); err != nil {
		t.Fatal(err)
	}
	b, err := a.budgetView(child, p)
	if err != nil {
		t.Fatal(err)
	}
	if b.Usage.Assignments != 1 || b.Usage.Revisions != 1 || b.Usage.CostUSD != .6 {
		t.Fatalf("usage: %+v", b.Usage)
	}
	a.Now = func() time.Time { return created.Add(time.Hour) }
	remaining, err := a.treeRemaining(child, p)
	if err != nil || remaining != time.Minute {
		t.Fatalf("idle charged: %s %v", remaining, err)
	}
}
