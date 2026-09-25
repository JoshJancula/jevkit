package main

import (
	"testing"
	"time"

	"github.com/OWNER/jevkit/internal/sdlc/adaptive"
	"github.com/OWNER/jevkit/internal/sdlc/enrollment"
	"github.com/OWNER/jevkit/internal/sdlc/ledger"
	"github.com/OWNER/jevkit/internal/sdlc/spec"
	"github.com/OWNER/jevkit/internal/sdlc/stageflow"
)

func TestDirectChildChargesRootAndUsesRootDeadline(t *testing.T) {
	a := newApp(t)
	p := enrollment.DefaultPolicy()
	p.MaxAssignments = 1
	p.MaxRevisions = 1
	p.MaxEstimatedCostUSD = 0.5
	p.MaxRunSeconds = 60
	st, err := adaptive.New("feature", "lean", 1, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	root := ledger.Run{RunID: "root", Workflow: "feature", CreatedAt: created.Format(time.RFC3339), Adaptive: &st, TreeUsage: &ledger.TreeUsage{}, StageFlow: &stageflow.State{Workflow: spec.Workflow{MaxSteps: 1}}}
	child := ledger.Run{RunID: "child", ParentRunID: "root", Depth: 1, Workflow: "bugfix", CreatedAt: created.Add(50 * time.Second).Format(time.RFC3339), Adaptive: &st}
	if err := ledger.Open(a.sdlcRunsDir(), "root").WriteRun(root); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Open(a.sdlcRunsDir(), "child").WriteRun(child); err != nil {
		t.Fatal(err)
	}
	if err := a.chargeTree(&child, p, "assignment"); err != nil {
		t.Fatal(err)
	}
	if err := a.chargeTree(&child, p, "assignment"); err == nil {
		t.Fatal("second child assignment exceeded root limit")
	}
	if err := a.chargeTree(&child, p, "revision"); err != nil {
		t.Fatal(err)
	}
	if err := a.chargeTree(&child, p, "revision"); err == nil {
		t.Fatal("second child revision exceeded root limit")
	}
	if err := a.chargeTree(&child, p, "step"); err != nil {
		t.Fatal(err)
	}
	if err := a.chargeTree(&child, p, "step"); err == nil {
		t.Fatal("second child stage step exceeded root limit")
	}
	if err := a.chargeTreeCost(&child, p, 0.3); err != nil {
		t.Fatal(err)
	}
	if err := a.chargeTreeCost(&child, p, 0.3); err == nil {
		t.Fatal("reported child cost exceeded root limit")
	}
	root, err = ledger.Open(a.sdlcRunsDir(), "root").ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if root.TreeUsage.Assignments != 1 || root.TreeUsage.Revisions != 1 || root.TreeUsage.EstimatedCostUSD < 0.6 {
		t.Fatalf("root usage: %+v", root.TreeUsage)
	}
	a.Now = func() time.Time { return created.Add(61 * time.Second) }
	remaining, err := a.treeRemaining(child, p)
	if err != nil {
		t.Fatal(err)
	}
	if remaining >= 0 {
		t.Fatalf("child escaped root deadline: %s", remaining)
	}
}
