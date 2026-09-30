package sdlc

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/cmd/jevkit/internal/testkit"
	"github.com/JoshJancula/jevkit/internal/jev"
	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/enrollment"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/sdlc/worker"
)

func TestSpecialistAdviceIsRequiredBeforeImplementation(t *testing.T) {
	a, _, fj := cliApp(t)
	testkit.WriteFile(t, a.sdlcPolicyPath(), "version: 1\nspecialistMode: advisory\n")
	a.SdlcSpecialistNeed = nil
	a.Environ = append(a.Environ, "JEVKIT_TRANSPORT=fixture")
	a.NewJev = func(jev.Config, func() (string, error)) app.Asker { return fj }
	a.SdlcReach = func() enrollment.Reach {
		return enrollment.Reach{Driver: "cli", Runtimes: map[string]enrollment.RuntimeCapability{"codex": {Write: true, ReadOnly: true}, "cursor": {Write: true}, "opencode": {Write: true}}}
	}
	testkit.WriteFile(t, a.sdlcRosterPath(), `version: 1
agents:
  - {id: planner, roles: [planner], rubric: Plan., via: runtime, runtime: codex, model: p}
  - {id: implementer, roles: [implementer], rubric: Implement., via: runtime, runtime: cursor, model: i}
  - {id: assessor, roles: [assessor], rubric: Assess., via: runtime, runtime: opencode, model: a}
  - {id: researcher, roles: [research], rubric: Research API compatibility., via: runtime, runtime: codex, model: r, readOnly: true}
`)
	fj.Resp = &jev.Response{Answers: map[string]jev.Answer{"need": jev.ChoiceAnswer{Choice: "needed", Confidence: 0.99}}}
	st, err := adaptive.New("feature", "lean", 1, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	st.Stage = adaptive.Implementing
	st.PlanRevision = "plan-revision"
	a.scheduleSpecialists(context.Background(), ledger.Run{RunID: "test", Task: "check API compatibility", Adaptive: &st}, &st, "plan")
	if st.Stage != adaptive.Specializing || st.Role() != "research" || len(st.SpecialistQueue) != 1 {
		t.Fatalf("research queue: %+v", st)
	}
	as := adaptive.Assignment{InvocationID: "research-inv", AgentID: "researcher", Binding: "runtime:codex:r::", Role: "research", Revision: st.PlanRevision}
	if err := st.Assign(as); err != nil {
		t.Fatal(err)
	}
	if err := st.Apply(adaptive.Result{InvocationID: as.InvocationID, AgentID: as.AgentID, Outcome: "advice", Revision: as.Revision}); err != nil {
		t.Fatal(err)
	}
	if st.Stage != adaptive.Implementing || len(st.SpecialistReviews) != 1 || !st.SpecialistReviews[0].Approved {
		t.Fatalf("after advice: %+v", st)
	}
	if fj.Calls != 1 || !strings.Contains(fj.Req.State, "API compatibility") {
		t.Fatalf("decision calls=%d state=%q", fj.Calls, fj.Req.State)
	}
}

func TestAdvisorySpecialistFallsBackToCoreRole(t *testing.T) {
	a, _, fj := cliApp(t)
	testkit.WriteFile(t, a.sdlcPolicyPath(), "version: 1\nspecialistMode: advisory\n")
	a.SdlcSpecialistNeed = nil
	a.Environ = append(a.Environ, "JEVKIT_TRANSPORT=fixture")
	a.NewJev = func(jev.Config, func() (string, error)) app.Asker { return fj }
	a.SdlcReach = func() enrollment.Reach {
		return enrollment.Reach{Driver: "cli", Runtimes: map[string]enrollment.RuntimeCapability{"codex": {Write: true, ReadOnly: true}}}
	}
	testkit.WriteFile(t, a.sdlcRosterPath(), "version: 1\nagents:\n  - {id: researcher, roles: [research], rubric: Research compatibility., via: runtime, runtime: codex, model: r, readOnly: true}\n")
	fj.Resp = &jev.Response{Answers: map[string]jev.Answer{"need": jev.ChoiceAnswer{Choice: "skip", Confidence: 0.03}}}
	st, err := adaptive.New("feature", "lean", 1, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	st.Stage, st.PlanRevision = adaptive.Implementing, "plan"
	a.scheduleSpecialists(context.Background(), ledger.Run{RunID: "advisory", Task: "implement plan"}, &st, "plan")
	if st.Stage != adaptive.Implementing || st.PendingDecision != "" || len(st.SpecialistDecisions) != 1 || st.SpecialistDecisions[0].Confidence != 0.03 {
		t.Fatalf("advisory decision: %+v", st)
	}
}

func TestAdvisorySkipsAbsentSpecialistWithoutAskingJev(t *testing.T) {
	a, _, fj := cliApp(t)
	testkit.WriteFile(t, a.sdlcPolicyPath(), "version: 1\nspecialistMode: advisory\n")
	a.SdlcSpecialistNeed = nil
	a.Environ = append(a.Environ, "JEVKIT_TRANSPORT=fixture")
	a.NewJev = func(jev.Config, func() (string, error)) app.Asker { return fj }
	st, err := adaptive.New("feature", "lean", 1, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	st.Stage, st.PlanRevision = adaptive.Implementing, "plan"
	a.scheduleSpecialists(context.Background(), ledger.Run{RunID: "advisory", Task: "implement plan"}, &st, "plan")
	if st.Stage != adaptive.Implementing || fj.Calls != 0 || len(st.SpecialistDecisions) != 1 {
		t.Fatalf("absent specialist: %+v calls=%d", st, fj.Calls)
	}
}

func TestBuiltInPausesPendingDecisionAndResumeSkipsCompletedPlan(t *testing.T) {
	a := newApp(t)
	testkit.WriteFile(t, a.sdlcPolicyPath(), "version: 1\nspecialistMode: required\n")
	a.SdlcSpecialistNeed = nil
	stageTestRoster(t, a)
	fake := &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "planned", Content: "Plan"}, {Outcome: "changed", Content: "diff --git a/a b/a\n+new\n"}, {Outcome: "approved"}}}
	a.SdlcExecutor = fake
	_, out, errs := run(a, "", "sdlc", "run", "feature", "--task", "add behavior", "--auto")
	if out == "" {
		t.Fatalf("missing run ID: %q", errs)
	}
	id := strings.Fields(out)[1]
	stored, err := ledger.Open(a.SDLCRunsDir(), id).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if stored.Adaptive.Stage != adaptive.Paused || stored.Adaptive.PendingDecision != "plan" || stored.Adaptive.PendingFocus != "research" || !strings.Contains(stored.Adaptive.PendingReason, "no-key") || len(fake.requests) != 1 {
		t.Fatalf("pending decision: %+v requests=%d", stored.Adaptive, len(fake.requests))
	}
	fj := &testkit.FakeJev{Resp: &jev.Response{Answers: map[string]jev.Answer{"need": jev.ChoiceAnswer{Choice: "skip", Confidence: 0.99}}}}
	a.NewJev = func(jev.Config, func() (string, error)) app.Asker { return fj }
	a.Environ = append(a.Environ, "JEVKIT_TRANSPORT=fixture")
	code, out, errs := run(a, "", "sdlc", "resume", id)
	if code != app.ExitOK {
		t.Fatalf("resume: %d %q %q", code, out, errs)
	}
	stored, err = ledger.Open(a.SDLCRunsDir(), id).ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if stored.Adaptive.Stage != adaptive.Done || len(fake.requests) != 3 || fj.Calls != 4 {
		t.Fatalf("completed worker repeated: %+v requests=%d decisions=%d", stored.Adaptive, len(fake.requests), fj.Calls)
	}
}

func TestResumeLegacySpecialistPauseWithAdvisoryPolicy(t *testing.T) {
	a := newApp(t)
	stageTestRoster(t, a)
	fake := &fakeSDLCExecutor{replies: []worker.Reply{{Outcome: "changed", Content: "diff --git a/a b/a\n+new\n"}, {Outcome: "approved"}}}
	a.SdlcExecutor = fake
	st, err := adaptive.New("feature", "lean", 1, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	st.PlanRevision = adaptive.DigestHex([]byte("Existing plan"))
	st.PendingDecision = "plan"
	st.PendingPhase = adaptive.Implementing
	st.PendingFocus = "research"
	st.PendingReason = "specialist decision unavailable: fallback"
	st.AssignmentCount = 1
	st.Pause("specialist-decision-unavailable")
	now := time.Now().UTC().Format(time.RFC3339)
	stored := ledger.Run{RunID: "legacy-specialist", WorkDir: a.WorkDir, Workflow: "feature", Task: "build feature", CreatedAt: now, UpdatedAt: now, Adaptive: &st, TreeUsage: &ledger.TreeUsage{Assignments: 1}}
	store := ledger.Open(a.SDLCRunsDir(), stored.RunID)
	if err := store.WriteRun(stored); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteArtifact("plan.md", []byte("Existing plan")); err != nil {
		t.Fatal(err)
	}
	code, _, errs := run(a, "", "sdlc", "resume", stored.RunID)
	if code != app.ExitOK {
		t.Fatalf("resume: %d %q", code, errs)
	}
	stored, err = store.ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if stored.Adaptive.Stage != adaptive.Done || len(fake.requests) != 2 || fake.requests[0].Assignment.Role != "implementer" {
		t.Fatalf("legacy resume: %+v requests=%d", stored.Adaptive, len(fake.requests))
	}
}

func TestMissingNeededExpertStaysPendingUntilEnrolled(t *testing.T) {
	a, _, fj := cliApp(t)
	testkit.WriteFile(t, a.sdlcPolicyPath(), "version: 1\nspecialistMode: required\n")
	a.SdlcSpecialistNeed = nil
	a.Environ = append(a.Environ, "JEVKIT_TRANSPORT=fixture")
	a.NewJev = func(jev.Config, func() (string, error)) app.Asker { return fj }
	a.SdlcReach = func() enrollment.Reach {
		return enrollment.Reach{Driver: "cli", Runtimes: map[string]enrollment.RuntimeCapability{"codex": {Write: true, ReadOnly: true}}}
	}
	fj.Resp = &jev.Response{Answers: map[string]jev.Answer{"need": jev.ChoiceAnswer{Choice: "needed", Confidence: 0.99}}}
	testkit.WriteFile(t, a.sdlcRosterPath(), "version: 1\nagents:\n  - {id: qa, roles: [qa], rubric: Test behavior., via: runtime, runtime: codex, model: q, readOnly: true}\n")
	st, err := adaptive.New("feature", "lean", 1, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	st.Stage = adaptive.Implementing
	st.PlanRevision = "plan"
	run := ledger.Run{RunID: "pending", Task: "check schema", Adaptive: &st}
	a.scheduleSpecialists(context.Background(), run, &st, "plan")
	if st.Stage != adaptive.Paused || st.Outcome != "unmet-specialist-research" || st.PendingDecision != "plan" {
		t.Fatalf("missing expert: %+v", st)
	}
	testkit.WriteFile(t, a.sdlcRosterPath(), "version: 1\nagents:\n  - {id: researcher, roles: [research], rubric: Research schema., via: runtime, runtime: codex, model: r, readOnly: true}\n")
	st.Stage = st.PendingPhase
	st.Outcome = ""
	a.scheduleSpecialists(context.Background(), run, &st, "plan")
	if st.Stage != adaptive.Specializing || st.Role() != "research" || st.PendingDecision != "" {
		t.Fatalf("retry: %+v", st)
	}
}

func TestPendingSpecialistDecisionRetriesWithoutRepeatingWorker(t *testing.T) {
	a, _, fj := cliApp(t)
	testkit.WriteFile(t, a.sdlcPolicyPath(), "version: 1\nspecialistMode: advisory\n")
	a.SdlcSpecialistNeed = nil
	a.Environ = append(a.Environ, "JEVKIT_TRANSPORT=fixture")
	a.NewJev = func(jev.Config, func() (string, error)) app.Asker { return fj }
	a.SdlcReach = func() enrollment.Reach {
		return enrollment.Reach{Driver: "cli", Runtimes: map[string]enrollment.RuntimeCapability{"codex": {Write: true, ReadOnly: true}}}
	}
	fj.Resp = &jev.Response{Answers: map[string]jev.Answer{"need": jev.ChoiceAnswer{Choice: "needed", Confidence: 0.99}}}
	testkit.WriteFile(t, a.sdlcRosterPath(), "version: 1\nagents:\n  - {id: researcher, roles: [research], rubric: Research schema., via: runtime, runtime: codex, model: r, readOnly: true}\n")
	st, err := adaptive.New("feature", "lean", 1, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	st.Stage = adaptive.Implementing
	st.PlanRevision = "plan"
	st.PendingDecision = "plan"
	st.PendingPhase = adaptive.Implementing
	st.AssignmentCount = 1
	now := time.Now().UTC().Format(time.RFC3339)
	run := ledger.Run{RunID: "crashed", Workflow: "feature", Task: "check schema", Adaptive: &st, CreatedAt: now, UpdatedAt: now, TreeUsage: &ledger.TreeUsage{Assignments: 1}}
	store := ledger.Open(a.SDLCRunsDir(), run.RunID)
	if err := store.WriteRun(run); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteArtifact("plan.md", []byte("Plan schema compatibility")); err != nil {
		t.Fatal(err)
	}
	handled, err := a.retryActiveSpecialistDecision(context.Background(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("pending decision skipped")
	}
	run, err = store.ReadRun()
	if err != nil {
		t.Fatal(err)
	}
	if run.Adaptive.Stage != adaptive.Specializing || run.Adaptive.AssignmentCount != 1 || run.Adaptive.PendingDecision != "" || fj.Calls != 1 {
		t.Fatalf("retry repeated work: %+v calls=%d", run.Adaptive, fj.Calls)
	}
}
