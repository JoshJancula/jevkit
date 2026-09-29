package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/sdlc/stageflow"
	"github.com/JoshJancula/jevkit/internal/sdlc/worker"
)

const hostContractWorkflow = `version: 1
name: host-contract
description: Plan, implement, and assess with deferred implement routes.
entry: plan
maxSteps: 12
stages:
  - id: plan
    work:
      role: planner
      objective: Plan the change.
      routes: {planned: implement, answer: done, no-change: done}
  - id: implement
    work:
      role: implementer
      objective: Implement the plan.
      routes: {changed: assess, answer: done, no-change: done}
  - id: assess
    work:
      role: assessor
      objective: Assess the candidate.
      routes: {approved: done, changes-required: implement}
  - id: done
    finish: succeeded
`

func TestStageflowRouteDefersThroughVerification(t *testing.T) {
	route, ok := stageflowRouteAfterResult(adaptive.Implementing, adaptive.Verifying, "changed", adaptive.State{})
	if ok || route != "" {
		t.Fatalf("implement→verifying should defer: route=%q ok=%v", route, ok)
	}
	route, ok = stageflowRouteAfterResult(adaptive.Assessing, adaptive.Verifying, "approved", adaptive.State{DiffRevision: "c"})
	if ok {
		t.Fatalf("completion-gate verifying should defer, got %q", route)
	}
	st := adaptive.State{DiffRevision: "c", Assessments: []adaptive.Assessment{{Revision: "c", Approved: true}}}
	route, ok = stageflowRouteAfterResult(adaptive.Assessing, adaptive.Done, "approved", st)
	if !ok || route != "approved" {
		t.Fatalf("assess→done: route=%q ok=%v", route, ok)
	}
	st.Assessments = []adaptive.Assessment{{Revision: "c", Approved: false}}
	route, ok = stageflowRouteAfterResult(adaptive.Assessing, adaptive.Implementing, "changes-required", st)
	if !ok || route != "changes-required" {
		t.Fatalf("assess→implement: route=%q ok=%v", route, ok)
	}
}

func TestCustomWorkflowDefersImplementStageUntilVerification(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	a.SdlcExecutor = &fakeSDLCExecutor{replies: []worker.Reply{
		{Outcome: "planned", Content: "Plan."},
		{Outcome: "changed", Content: "diff --git a/a b/a\n+new\n"},
		{Outcome: "approved"},
	}}
	writeWorkflow(t, a, "host-contract", hostContractWorkflow)
	code, out, errs := run(a, "", "sdlc", "start", "host-contract", "--task", "add a line", "--auto")
	if code != exitOK {
		t.Fatalf("start: %d %q %q", code, out, errs)
	}
	id := strings.Fields(out)[1]
	code, _, errs = run(a, "", "sdlc", "drive", id) // plan
	if code != exitOK {
		t.Fatalf("plan drive: %d %q", code, errs)
	}
	code, _, errs = run(a, "", "sdlc", "drive", id) // implement → verifying
	if code != exitOK {
		t.Fatalf("implement drive: %d %q", code, errs)
	}
	stored, err := ledger.Open(a.sdlcRunsDir(), id).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if stored.Adaptive.Stage != adaptive.Verifying || stored.StageFlow.Current != "implement" {
		t.Fatalf("implement stage must own verifying: stage=%s current=%s", stored.Adaptive.Stage, stored.StageFlow.Current)
	}
	code, out, errs = run(a, "", "sdlc", "drive", id) // verify → assess route
	if code != exitOK {
		t.Fatalf("verify drive: %d %q %q", code, out, errs)
	}
	stored, err = ledger.Open(a.sdlcRunsDir(), id).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if stored.Adaptive.Stage != adaptive.Assessing || stored.StageFlow.Current != "assess" {
		t.Fatalf("after verify: stage=%s current=%s", stored.Adaptive.Stage, stored.StageFlow.Current)
	}
	code, _, errs = run(a, "", "sdlc", "drive", id)
	if code != exitOK {
		t.Fatalf("assess drive: %d %q", code, errs)
	}
	stored, err = ledger.Open(a.sdlcRunsDir(), id).ReadRun()
	if err != nil || stored.Adaptive.Stage != adaptive.Done || stored.StageFlow.Current != "done" {
		t.Fatalf("done: %+v err=%v", stored, err)
	}
}

func TestHostReportCannotForgeSupervisorVerification(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	writeWorkflow(t, a, "host-contract", hostContractWorkflow)
	code, out, errs := run(a, "", "sdlc", "start", "host-contract", "--task", "add a line", "--auto")
	if code != exitOK {
		t.Fatalf("start: %d %q %q", code, out, errs)
	}
	id := strings.Fields(out)[1]
	code, out, errs = run(a, "", "sdlc", "next", id)
	if code != exitOK {
		t.Fatalf("next planner: %d %q %q", code, out, errs)
	}
	var planner adaptive.Assignment
	if err := json.Unmarshal([]byte(out), &planner); err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planPath, []byte("Plan."), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errs = run(a, "", "sdlc", "report", id, "--invocation", planner.InvocationID, "--agent", planner.AgentID, "--outcome", "planned", "--file", planPath)
	if code != exitOK {
		t.Fatalf("report planned: %d %q", code, errs)
	}
	code, out, errs = run(a, "", "sdlc", "next", id)
	if code != exitOK {
		t.Fatalf("next implementer: %d %q %q", code, out, errs)
	}
	var implementer adaptive.Assignment
	if err := json.Unmarshal([]byte(out), &implementer); err != nil {
		t.Fatal(err)
	}
	diffPath := filepath.Join(t.TempDir(), "patch.diff")
	diff := []byte("diff --git a/a b/a\n+new\n")
	if err := os.WriteFile(diffPath, diff, 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errs = run(a, "", "sdlc", "report", id, "--invocation", implementer.InvocationID, "--agent", implementer.AgentID, "--outcome", "changed", "--file", diffPath)
	if code != exitOK {
		t.Fatalf("report changed: %d %q", code, errs)
	}
	stored, err := ledger.Open(a.sdlcRunsDir(), id).ReadRun()
	if err != nil || stored.Adaptive.Stage != adaptive.Verifying || stored.StageFlow.Current != "implement" {
		t.Fatalf("host changed must enter verifying inside implement: %+v err=%v", stored, err)
	}
	code, _, errs = run(a, "", "sdlc", "report", id, "--invocation", "fake", "--agent", "implementer", "--outcome", "verified")
	if code == exitOK || !strings.Contains(errs, "supervisor verification") && !strings.Contains(errs, "agent results only") {
		t.Fatalf("forge verified: %d %q", code, errs)
	}
	code, _, errs = run(a, "", "sdlc", "report", id, "--invocation", "fake", "--agent", "implementer", "--outcome", "approved")
	if code == exitOK || !strings.Contains(errs, "supervisor verification") {
		t.Fatalf("report during verifying: %d %q", code, errs)
	}
}

func TestHostNextRefusesApprovedFanoutBypass(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	code, out, errs := run(a, "", "sdlc", "start", "feature", "--task", "fanout host", "--auto")
	if code != exitOK {
		t.Fatalf("start: %d %q %q", code, out, errs)
	}
	id := strings.Fields(out)[1]
	seedApprovedFanout(t, a, id, true)
	code, _, errs = run(a, "", "sdlc", "next", id)
	if code == exitOK || !strings.Contains(errs, "approved fan-out graph") {
		t.Fatalf("next must refuse fan-out bypass: %d %q", code, errs)
	}
}

func TestChildRunPropagatesPlanArtifactsAndPauseResume(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	a.SdlcExecutor = &fakeSDLCExecutor{replies: []worker.Reply{
		{Outcome: "planned", Content: "Child plan.", Checks: []adaptive.Check{{ID: "manual", Manual: "look"}}, NextSteps: []string{"do it"}, AcceptanceCriteria: []string{"ok"}},
		{Outcome: "changed", Content: "diff --git a/a b/a\n+child\n"},
		{Outcome: "approved"},
	}}
	writeWorkflow(t, a, "choose-fix", directSpawnWorkflow("choose-fix", "bugfix"))
	code, out, errs := run(a, "", "sdlc", "run", "choose-fix", "--task", "child contract", "--auto")
	if code != exitOK {
		t.Fatalf("run: %d %q %q", code, out, errs)
	}
	parentID := strings.Fields(out)[1]
	parent, err := ledger.Open(a.sdlcRunsDir(), parentID).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	childID := parent.StageFlow.Transitions[0].ChildRunID
	if childID == "" && len(parent.StageFlow.Transitions) > 1 {
		childID = parent.StageFlow.Transitions[1].ChildRunID
	}
	if childID == "" {
		for _, tr := range parent.StageFlow.Transitions {
			if tr.ChildRunID != "" {
				childID = tr.ChildRunID
				break
			}
		}
	}
	if childID == "" {
		t.Fatalf("missing child id: %+v", parent.StageFlow)
	}
	store := ledger.Open(a.sdlcRunsDir(), parentID)
	for _, name := range []string{adaptive.ArtifactChecks, adaptive.ArtifactSubtasks} {
		if _, err := store.ReadArtifact(name); err != nil {
			t.Fatalf("parent missing propagated %s: %v", name, err)
		}
	}
	if parent.Adaptive.ChecksRevision == "" || parent.Adaptive.DiffRevision == "" {
		t.Fatalf("parent missing propagated revisions: %+v", parent.Adaptive)
	}
}

func TestCustomWorkflowStaleAssessmentRejected(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	writeWorkflow(t, a, "host-contract", hostContractWorkflow)
	code, out, errs := run(a, "", "sdlc", "start", "host-contract", "--task", "stale", "--auto")
	if code != exitOK {
		t.Fatalf("start: %d %q %q", code, out, errs)
	}
	id := strings.Fields(out)[1]
	planner, err := a.sdlcAssignNext(t.Context(), id)
	if err != nil || planner == nil {
		t.Fatalf("planner: %v", err)
	}
	plan := []byte("Plan.")
	if err := a.sdlcRecordResult(id, adaptive.Result{InvocationID: planner.InvocationID, AgentID: planner.AgentID, Outcome: "planned", Revision: fmt.Sprintf("%x", sha256.Sum256(plan))}, "plan.md", plan); err != nil {
		t.Fatal(err)
	}
	implementer, err := a.sdlcAssignNext(t.Context(), id)
	if err != nil || implementer == nil {
		t.Fatalf("implementer: %v", err)
	}
	diff := []byte("diff --git a/a b/a\n+new\n")
	if err := a.sdlcRecordResult(id, adaptive.Result{InvocationID: implementer.InvocationID, AgentID: implementer.AgentID, Outcome: "changed", Revision: fmt.Sprintf("%x", sha256.Sum256(diff))}, "patch.diff", diff); err != nil {
		t.Fatal(err)
	}
	// Drive verification without assigning an assessor yet.
	if _, err := a.sdlcAssignNext(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	assessor, err := a.sdlcAssignNext(t.Context(), id)
	if err != nil || assessor == nil || assessor.Role != "assessor" {
		t.Fatalf("assessor: %+v %v", assessor, err)
	}
	err = a.sdlcRecordResult(id, adaptive.Result{InvocationID: assessor.InvocationID, AgentID: assessor.AgentID, Outcome: "approved", Revision: "stale-rev"}, "", nil)
	if err == nil || !strings.Contains(err.Error(), "revision") {
		t.Fatalf("stale assessment: %v", err)
	}
}

func TestCustomWorkflowCrashRecoveryKeepsImplementStage(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	writeWorkflow(t, a, "host-contract", hostContractWorkflow)
	code, out, errs := run(a, "", "sdlc", "start", "host-contract", "--task", "crash", "--auto")
	if code != exitOK {
		t.Fatalf("start: %d %q %q", code, out, errs)
	}
	id := strings.Fields(out)[1]
	planner, err := a.sdlcAssignNext(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	plan := []byte("Plan.")
	if err := a.sdlcRecordResult(id, adaptive.Result{InvocationID: planner.InvocationID, AgentID: planner.AgentID, Outcome: "planned", Revision: fmt.Sprintf("%x", sha256.Sum256(plan))}, "plan.md", plan); err != nil {
		t.Fatal(err)
	}
	implementer, err := a.sdlcAssignNext(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	diff := []byte("diff --git a/a b/a\n+new\n")
	if err := a.sdlcRecordResult(id, adaptive.Result{InvocationID: implementer.InvocationID, AgentID: implementer.AgentID, Outcome: "changed", Revision: fmt.Sprintf("%x", sha256.Sum256(diff))}, "patch.diff", diff); err != nil {
		t.Fatal(err)
	}
	store := ledger.Open(a.sdlcRunsDir(), id)
	runRec, err := store.ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a crash mid-verification: receipts absent, stage still verifying,
	// authored Current still implement.
	runRec.Adaptive.Stage = adaptive.Verifying
	runRec.Adaptive.CheckReceipts = nil
	runRec.Verification = nil
	runRec.UpdatedAt = a.now().UTC().Format(time.RFC3339)
	if err := store.WriteRun(runRec); err != nil {
		t.Fatal(err)
	}
	a.SdlcExecutor = &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "approved"}}}
	if err := a.sdlcDriveUntilDone(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	stored, err := store.ReadRun()
	if err != nil || stored.Adaptive.Stage != adaptive.Done || stored.StageFlow.Current != "done" {
		t.Fatalf("crash recovery: %+v err=%v", stored, err)
	}
}

func TestStageflowMigrateSavedReadableOrExplicit(t *testing.T) {
	s := &stageflow.State{}
	s.Workflow.Version = 0
	if err := stageflow.MigrateSaved(s); err != nil || s.Workflow.Version != stageflow.CurrentStageFormatVersion {
		t.Fatalf("v0 migrate: %+v err=%v", s, err)
	}
	s.Workflow.Version = 99
	if err := stageflow.MigrateSaved(s); err == nil || !strings.Contains(err.Error(), "re-author") {
		t.Fatalf("unsupported version: %v", err)
	}
}
