package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/OWNER/jevkit/internal/sdlc/adaptive"
	"github.com/OWNER/jevkit/internal/sdlc/ledger"
	"github.com/OWNER/jevkit/internal/sdlc/worker"
)

func TestSDLCRunRequiresApprovalBeforeImplementation(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	executor := &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "planned", Content: "Change the greeting."}, {Outcome: "changed", Content: "diff --git a/a b/a\n+new\n"}, {Outcome: "approved"}}}
	a.SdlcExecutor = executor
	code, out, errs := run(a, "", "sdlc", "run", "bugfix", "--task", "fix the greeting")
	if code != exitOK || !strings.Contains(out, "PAUSED (plan-approval-required)") || !strings.Contains(out, "--approve-plan") {
		t.Fatalf("run: %d %q %q", code, out, errs)
	}
	id := strings.Fields(out)[1]
	store := ledger.Open(a.sdlcRunsDir(), id)
	saved, err := store.ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if !saved.RequirePlanApproval || saved.Adaptive.Stage != adaptive.Paused || saved.ApprovedPlanRevision != "" || len(executor.requests) != 1 {
		t.Fatalf("approval gate: %+v requests=%d", saved, len(executor.requests))
	}
	if plan, err := store.ReadArtifact("plan.md"); err != nil || string(plan) != "Change the greeting." {
		t.Fatalf("plan: %q %v", plan, err)
	}
	code, _, _ = run(a, "", "sdlc", "resume", id)
	if code == exitOK || len(executor.requests) != 1 {
		t.Fatalf("unapproved resume executed work: code=%d requests=%d", code, len(executor.requests))
	}
	code, out, errs = run(a, "", "sdlc", "resume", id, "--approve-plan")
	if code != exitOK || !strings.Contains(out, "DONE (approved)") {
		t.Fatalf("approved resume: %d %q %q", code, out, errs)
	}
	saved, err = store.ReadRun()
	if err != nil || saved.Adaptive.Stage != adaptive.Done || saved.ApprovedPlanRevision != saved.Adaptive.PlanRevision || len(executor.requests) != 3 {
		t.Fatalf("completed run: %+v requests=%d err=%v", saved, len(executor.requests), err)
	}
}

func TestSDLCRunPlanFileAlsoRequiresApproval(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	planFile := a.WorkDir + "/prepared-plan.md"
	writeFile(t, planFile, "Implement the fix.\n")
	executor := &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "changed", Content: "diff --git a/a b/a\n+new\n"}, {Outcome: "approved"}}}
	a.SdlcExecutor = executor
	code, out, errs := run(a, "", "sdlc", "run", "bugfix", "--plan-file", planFile)
	if code != exitOK || !strings.Contains(out, "PAUSED (plan-approval-required)") || len(executor.requests) != 0 {
		t.Fatalf("prepared plan: %d %q %q requests=%d", code, out, errs, len(executor.requests))
	}
	id := strings.Fields(out)[1]
	code, _, errs = run(a, "", "sdlc", "resume", id, "--approve-plan")
	if code != exitOK || len(executor.requests) != 2 {
		t.Fatalf("approved prepared plan: %d %q requests=%d", code, errs, len(executor.requests))
	}
}

func TestSDLCApprovalRejectsChangedPlanArtifact(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	executor := &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "planned", Content: "Original plan."}, {Outcome: "changed", Content: "diff"}}}
	a.SdlcExecutor = executor
	code, out, errs := run(a, "", "sdlc", "run", "bugfix", "--task", "fix it")
	if code != exitOK {
		t.Fatalf("run: %d %q %q", code, out, errs)
	}
	id := strings.Fields(out)[1]
	if err := ledger.Open(a.sdlcRunsDir(), id).WriteArtifact("plan.md", []byte("Changed plan.")); err != nil {
		t.Fatal(err)
	}
	code, _, errs = run(a, "", "sdlc", "resume", id, "--approve-plan")
	if code == exitOK || !strings.Contains(errs, "no longer matches") || len(executor.requests) != 1 {
		t.Fatalf("changed plan approved: %d %q requests=%d", code, errs, len(executor.requests))
	}
}

func TestSDLCRunBlocksPlanChangedAfterApproval(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	executor := &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "planned", Content: "Original plan."}, {Outcome: "changed", Content: "diff"}}}
	a.SdlcExecutor = executor
	code, out, errs := run(a, "", "sdlc", "run", "bugfix", "--task", "fix it")
	if code != exitOK {
		t.Fatalf("run: %d %q %q", code, out, errs)
	}
	id := strings.Fields(out)[1]
	if err := a.sdlcApprovePlan(id); err != nil {
		t.Fatal(err)
	}
	store := ledger.Open(a.sdlcRunsDir(), id)
	if err := store.WriteArtifact("plan.md", []byte("Changed plan.")); err != nil {
		t.Fatal(err)
	}
	code, _, _ = run(a, "", "sdlc", "resume", id)
	saved, err := store.ReadRun()
	if code != exitOK || err != nil || saved.Adaptive.Outcome != "plan-artifact-mismatch" || len(executor.requests) != 1 {
		t.Fatalf("changed approved plan launched implementation: code=%d run=%+v requests=%d err=%v", code, saved, len(executor.requests), err)
	}
}

func TestSDLCNestedRunWaitsForChildPlanApproval(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	executor := &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "planned", Content: "Child plan."}, {Outcome: "changed", Content: "diff --git a/a b/a\n+fix\n"}, {Outcome: "approved"}}}
	a.SdlcExecutor = executor
	writeWorkflow(t, a, "choose-fix", directSpawnWorkflow("choose-fix", "bugfix"))
	code, out, errs := run(a, "", "sdlc", "run", "choose-fix", "--task", "fix it")
	if code != exitOK || !strings.Contains(out, "child-plan-approval-required") || len(executor.requests) != 1 {
		t.Fatalf("nested gate: %d %q %q requests=%d", code, out, errs, len(executor.requests))
	}
	id := strings.Fields(out)[1]
	code, out, errs = run(a, "", "sdlc", "resume", id, "--approve-plan")
	if code != exitOK || !strings.Contains(out, "DONE (succeeded)") || len(executor.requests) != 3 {
		t.Fatalf("nested approval: %d %q %q requests=%d", code, out, errs, len(executor.requests))
	}
}

func TestSDLCHostAssignmentCannotBypassPlanApproval(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	code, out, errs := run(a, "", "sdlc", "start", "bugfix", "--task", "fix it")
	if code != exitOK {
		t.Fatalf("start: %d %q %q", code, out, errs)
	}
	id := strings.Fields(out)[1]
	planner, err := a.sdlcAssignNext(t.Context(), id)
	if err != nil || planner == nil || planner.Role != "planner" {
		t.Fatalf("planner: %+v %v", planner, err)
	}
	plan := []byte("Fix the behavior.")
	revision := fmt.Sprintf("%x", sha256.Sum256(plan))
	if err := a.sdlcRecordResult(id, adaptive.Result{InvocationID: planner.InvocationID, AgentID: planner.AgentID, Outcome: "planned", Revision: revision}, "plan.md", plan); err != nil {
		t.Fatal(err)
	}
	implementer, err := a.sdlcAssignNext(t.Context(), id)
	if err == nil || implementer != nil || !strings.Contains(err.Error(), "approve the saved plan") {
		t.Fatalf("unapproved assignment: %+v %v", implementer, err)
	}
	saved, err := ledger.Open(a.sdlcRunsDir(), id).ReadRun()
	if err != nil || saved.Adaptive.Stage != adaptive.Paused || saved.Adaptive.Outcome != "plan-approval-required" {
		t.Fatalf("host gate: %+v %v", saved, err)
	}
}

func TestSDLCInteractivePlanReviewRevisesAndContinues(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	executor := &fakeSDLCExecutor{replies: []worker.Reply{
		{Outcome: "planned", Content: "First plan."},
		{Outcome: "planned", Content: "Revised plan with tests."},
		{Outcome: "changed", Content: "diff --git a/a b/a\n+new\n"},
		{Outcome: "approved"},
	}}
	a.SdlcExecutor = executor
	code, out, errs := run(a, "", "sdlc", "run", "bugfix", "--task", "fix it")
	if code != exitOK {
		t.Fatalf("run: %d %q %q", code, out, errs)
	}
	id := strings.Fields(out)[1]
	var display bytes.Buffer
	a.Stdout = &display
	a.Stdin = strings.NewReader("")
	a.sdlcInputBytes = make(chan byte, 128)
	for _, b := range []byte("\x1b[B\rAdd tests for the change.\r\r") {
		a.sdlcInputBytes <- b
	}
	if err := a.sdlcInteractiveDrive(context.Background(), id, func() error { return nil }, false); err != nil {
		t.Fatalf("interactive review: %v\n%s", err, display.String())
	}
	if !strings.Contains(display.String(), "First plan.") || !strings.Contains(display.String(), "Revised plan with tests.") || !strings.Contains(display.String(), "DONE (approved)") || !strings.Contains(display.String(), "↑/↓ to move") || !strings.Contains(display.String(), "┌ Change request ") || strings.Contains(display.String(), "Choice:") {
		t.Fatalf("plan review output: %q", display.String())
	}
	if len(executor.requests) != 4 || executor.requests[1].Assignment.Role != "planner" || !strings.Contains(executor.requests[1].Task, "Add tests for the change.") || executor.requests[1].Plan != "First plan." {
		t.Fatalf("replan requests: %+v", executor.requests)
	}
	saved, err := ledger.Open(a.sdlcRunsDir(), id).ReadRun()
	if err != nil || saved.Adaptive.Stage != adaptive.Done || saved.PlanFeedback != "" || saved.ApprovedPlanRevision != saved.Adaptive.PlanRevision {
		t.Fatalf("saved run: %+v %v", saved, err)
	}
}

func TestSDLCInteractivePlanReviewCanLeavePaused(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	executor := &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "planned", Content: "Plan to review."}, {Outcome: "changed", Content: "diff"}}}
	a.SdlcExecutor = executor
	code, out, errs := run(a, "", "sdlc", "run", "bugfix", "--task", "fix it")
	if code != exitOK {
		t.Fatalf("run: %d %q %q", code, out, errs)
	}
	id := strings.Fields(out)[1]
	var display bytes.Buffer
	a.Stdout = &display
	a.Stdin = strings.NewReader("")
	a.sdlcInputBytes = make(chan byte, 128)
	for _, b := range []byte("\x1b[B\rDraft feedback\x1b\x1b[B\r") {
		a.sdlcInputBytes <- b
	}
	if err := a.sdlcInteractiveDrive(context.Background(), id, func() error { return nil }, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(display.String(), "Plan to review.") || len(executor.requests) != 1 {
		t.Fatalf("run continued without approval: %q requests=%d", display.String(), len(executor.requests))
	}
}

func TestSDLCCustomWorkflowCanRevisePlanBeforeImplementation(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	executor := &fakeSDLCExecutor{replies: []worker.Reply{
		{Outcome: "planned", Content: "Initial plan."},
		{Outcome: "planned", Content: "Revised plan."},
		{Outcome: "changed", Content: "diff --git a/a b/a\n+new\n"},
		{Outcome: "approved"},
	}}
	a.SdlcExecutor = executor
	writeWorkflow(t, a, "custom-review", `version: 1
name: custom-review
description: Review a custom plan.
entry: plan
stages:
  - id: plan
    work:
      role: planner
      objective: Plan the fix.
      routes: {planned: implement, answer: done, no-change: done}
  - id: implement
    work:
      role: implementer
      objective: Implement the fix.
      routes: {changed: assess, answer: done, no-change: done}
  - id: assess
    work:
      role: assessor
      objective: Assess the fix.
      routes: {approved: done, changes-required: implement}
  - id: done
    finish: succeeded
`)
	code, out, errs := run(a, "", "sdlc", "run", "custom-review", "--task", "fix it")
	if code != exitOK {
		t.Fatalf("run: %d %q %q", code, out, errs)
	}
	id := strings.Fields(out)[1]
	if err := a.sdlcRequestPlanChanges(id, "Add a regression test."); err != nil {
		t.Fatal(err)
	}
	if err := a.sdlcDriveUntilDone(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	saved, err := ledger.Open(a.sdlcRunsDir(), id).ReadRun()
	if err != nil || saved.Adaptive.Outcome != "plan-approval-required" || len(executor.requests) != 2 || executor.requests[1].Plan != "Initial plan." {
		t.Fatalf("replanned custom workflow: %+v requests=%+v err=%v", saved, executor.requests, err)
	}
	if err := a.sdlcApprovePlan(id); err != nil {
		t.Fatal(err)
	}
	if err := a.sdlcDriveUntilDone(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	saved, err = ledger.Open(a.sdlcRunsDir(), id).ReadRun()
	if err != nil || saved.Adaptive.Stage != adaptive.Done || len(executor.requests) != 4 {
		t.Fatalf("completed custom workflow: %+v requests=%d err=%v", saved, len(executor.requests), err)
	}
}

func TestSDLCNestedRunCanReviseChildPlan(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	executor := &fakeSDLCExecutor{replies: []worker.Reply{
		{Outcome: "planned", Content: "Initial child plan."},
		{Outcome: "planned", Content: "Revised child plan."},
		{Outcome: "changed", Content: "diff --git a/a b/a\n+new\n"},
		{Outcome: "approved"},
	}}
	a.SdlcExecutor = executor
	writeWorkflow(t, a, "choose-fix", directSpawnWorkflow("choose-fix", "bugfix"))
	code, out, errs := run(a, "", "sdlc", "run", "choose-fix", "--task", "fix it")
	if code != exitOK {
		t.Fatalf("run: %d %q %q", code, out, errs)
	}
	id := strings.Fields(out)[1]
	if err := a.sdlcRequestPlanChanges(id, "Add a compatibility check."); err != nil {
		t.Fatal(err)
	}
	if err := a.sdlcDriveUntilDone(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	parent, err := ledger.Open(a.sdlcRunsDir(), id).ReadRun()
	if err != nil || parent.Adaptive.Outcome != "child-plan-approval-required" || len(executor.requests) != 2 || executor.requests[1].Plan != "Initial child plan." {
		t.Fatalf("nested replan: %+v requests=%+v err=%v", parent, executor.requests, err)
	}
	if err := a.sdlcApprovePlan(id); err != nil {
		t.Fatal(err)
	}
	if err := a.sdlcDriveUntilDone(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	parent, err = ledger.Open(a.sdlcRunsDir(), id).ReadRun()
	if err != nil || parent.Adaptive.Stage != adaptive.Done || len(executor.requests) != 4 {
		t.Fatalf("nested completion: %+v requests=%d err=%v", parent, len(executor.requests), err)
	}
}
