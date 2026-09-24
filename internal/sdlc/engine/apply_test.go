package engine

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/OWNER/jevkit/internal/registry"
	"github.com/OWNER/jevkit/internal/sdlc/compile"
	"github.com/OWNER/jevkit/internal/sdlc/graph"
	"github.com/OWNER/jevkit/internal/sdlc/spec"
)

func buildGraph(t *testing.T, y string) *graph.Graph {
	t.Helper()
	w, err := spec.Load([]byte(y))
	if err != nil {
		t.Fatalf("spec.Load: %v", err)
	}
	g, err := compile.Compile(w)
	if err != nil {
		t.Fatalf("compile.Compile: %v", err)
	}
	return g
}

func ptr(s string) *string { return &s }

const simpleWorkGraph = `
version: 1
name: simple
description: d
budgets:
  maxNodeAttempts: 2
nodes:
  - id: a
    kind: work
    agent: self
    produces: [{path: out.txt, required: true}]
    next: done
  - id: done
    kind: terminal
    outcome: succeeded
`

func TestNewRunEntersFirstEffect(t *testing.T) {
	g := buildGraph(t, simpleWorkGraph)
	st, effs, err := NewRun(g)
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	if st.Current != "a" || st.Status != StatusRunning {
		t.Fatalf("state = %+v", st)
	}
	if len(effs) != 1 {
		t.Fatalf("effects = %v", effs)
	}
	rw, ok := effs[0].(RunWork)
	if !ok || rw.NodeID != "a" || rw.Attempt != 1 || !rw.Assignment.Self {
		t.Fatalf("effect = %+v", effs[0])
	}
}

func TestWorkCompletesToNext(t *testing.T) {
	g := buildGraph(t, simpleWorkGraph)
	st, _, err := NewRun(g)
	if err != nil {
		t.Fatal(err)
	}
	st, effs, err := Apply(g, st, WorkCompleted{NodeID: "a", ProducedPaths: []string{"out.txt"}, CostReported: true})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if st.Status != StatusTerminal || st.Outcome != "succeeded" {
		t.Fatalf("state = %+v", st)
	}
	if !st.Satisfied["out.txt"] {
		t.Error("expected out.txt marked satisfied")
	}
	if len(effs) != 1 {
		t.Fatalf("effects = %v", effs)
	}
	if fin, ok := effs[0].(RunFinished); !ok || fin.Outcome != "succeeded" {
		t.Fatalf("effect = %+v", effs[0])
	}
}

func TestWorkMissingRequiredArtifactRetriesThenFails(t *testing.T) {
	g := buildGraph(t, simpleWorkGraph) // maxNodeAttempts: 2
	st, _, err := NewRun(g)
	if err != nil {
		t.Fatal(err)
	}

	st, effs, err := Apply(g, st, WorkCompleted{NodeID: "a"}) // no produced paths
	if err != nil {
		t.Fatalf("Apply (attempt 1 fail): %v", err)
	}
	if st.Status != StatusRunning {
		t.Fatalf("expected still running after first failure, got %+v", st)
	}
	rw, ok := effs[0].(RunWork)
	if !ok || rw.Attempt != 2 {
		t.Fatalf("expected a retry at attempt 2, got %+v", effs[0])
	}

	st, effs, err = Apply(g, st, WorkCompleted{NodeID: "a"}) // attempt 2 also fails
	if err != nil {
		t.Fatalf("Apply (attempt 2 fail): %v", err)
	}
	if st.Status != StatusFailed || st.Outcome != "node-attempts-exhausted:a" {
		t.Fatalf("expected exhausted failure, got %+v", st)
	}
	if fin, ok := effs[0].(RunFinished); !ok || fin.Status != StatusFailed {
		t.Fatalf("effect = %+v", effs[0])
	}
}

func TestWorkExecutionFailedConsumesAttemptSameAsMissingArtifact(t *testing.T) {
	g := buildGraph(t, simpleWorkGraph)
	st, _, err := NewRun(g)
	if err != nil {
		t.Fatal(err)
	}
	st, effs, err := Apply(g, st, WorkCompleted{NodeID: "a", ExecutionFailed: true})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	rw, ok := effs[0].(RunWork)
	if !ok || rw.Attempt != 2 || st.Status != StatusRunning {
		t.Fatalf("expected a same-node retry, got state=%+v effect=%+v", st, effs[0])
	}
}

const checkGraph = `
version: 1
name: checkflow
description: d
budgets:
  maxNodeAttempts: 2
nodes:
  - id: t
    kind: check
    command: "go test ./..."
    routes: {pass: done, fail: aborted}
  - id: done
    kind: terminal
    outcome: succeeded
  - id: aborted
    kind: terminal
    outcome: aborted
`

func TestCheckRoutesOnPass(t *testing.T) {
	g := buildGraph(t, checkGraph)
	st, _, err := NewRun(g)
	if err != nil {
		t.Fatal(err)
	}
	st, _, err = Apply(g, st, CommandCompleted{NodeID: "t", Route: "pass"})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if st.Outcome != "succeeded" {
		t.Fatalf("state = %+v", st)
	}
}

func TestCheckRoutesOnFail(t *testing.T) {
	g := buildGraph(t, checkGraph)
	st, _, err := NewRun(g)
	if err != nil {
		t.Fatal(err)
	}
	st, _, err = Apply(g, st, CommandCompleted{NodeID: "t", Route: "fail"})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if st.Outcome != "aborted" {
		t.Fatalf("state = %+v", st)
	}
}

func TestCheckUnknownRouteIsAnError(t *testing.T) {
	g := buildGraph(t, checkGraph)
	st, _, err := NewRun(g)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Apply(g, st, CommandCompleted{NodeID: "t", Route: "nonexistent"}); err == nil {
		t.Fatal("expected an error for an undeclared route")
	}
}

func TestCheckExecutionFailedRetriesThenFails(t *testing.T) {
	g := buildGraph(t, checkGraph)
	st, _, err := NewRun(g)
	if err != nil {
		t.Fatal(err)
	}
	st, effs, err := Apply(g, st, CommandCompleted{NodeID: "t", ExecutionFailed: true})
	if err != nil {
		t.Fatal(err)
	}
	if rc, ok := effs[0].(RunCommand); !ok || rc.Attempt != 2 || st.Status != StatusRunning {
		t.Fatalf("expected retry attempt 2, got state=%+v effect=%+v", st, effs[0])
	}
	st, effs, err = Apply(g, st, CommandCompleted{NodeID: "t", ExecutionFailed: true})
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != StatusFailed || st.Outcome != "node-attempts-exhausted:t" {
		t.Fatalf("state = %+v", st)
	}
	_ = effs
}

const selectGraph = `
version: 1
name: selectflow
description: d
budgets:
  maxNodeAttempts: 3
nodes:
  - id: pick
    kind: select
    questionSet: sdlc.agent-selection
    candidates: [codex-implementer, cursor-planner]
    assignTo: implement
    default: cursor-planner
  - id: implement
    kind: work
    produces: [{path: patch.diff, required: true}]
    objective: "implement it"
    next: done
  - id: done
    kind: terminal
    outcome: succeeded
`

func TestSelectActAssignsChosenCandidate(t *testing.T) {
	g := buildGraph(t, selectGraph)
	st, _, err := NewRun(g)
	if err != nil {
		t.Fatal(err)
	}
	dec := registry.Decision{Decision: registry.Act, Chosen: ptr("codex-implementer"), Confidence: 0.9}
	st, effs, err := Apply(g, st, JevDecided{NodeID: "pick", Decision: dec, Available: true})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if st.Assigned["implement"] != "codex-implementer" {
		t.Errorf("Assigned = %v", st.Assigned)
	}
	if st.Supervised["implement"] {
		t.Error("act should not mark the node supervised")
	}
	rw, ok := effs[0].(RunWork)
	if !ok || rw.Assignment.AgentID != "codex-implementer" || rw.Assignment.Self {
		t.Fatalf("effect = %+v", effs[0])
	}
}

func TestSelectFallbackAssignsDefault(t *testing.T) {
	g := buildGraph(t, selectGraph)
	st, _, err := NewRun(g)
	if err != nil {
		t.Fatal(err)
	}
	dec := registry.Decision{Decision: registry.Fallback, Confidence: 0.2}
	st, _, err = Apply(g, st, JevDecided{NodeID: "pick", Decision: dec, Available: false})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if st.Assigned["implement"] != "cursor-planner" {
		t.Errorf("Assigned = %v, want the declared default", st.Assigned)
	}
	if st.JevAvailable["pick"] {
		t.Error("expected JevAvailable[pick] = false")
	}
}

func TestSelectGatherSupervisesAndCapsAttemptsAtOne(t *testing.T) {
	g := buildGraph(t, selectGraph) // maxNodeAttempts: 3 normally
	st, _, err := NewRun(g)
	if err != nil {
		t.Fatal(err)
	}
	dec := registry.Decision{Decision: registry.Gather, Chosen: ptr("codex-implementer"), Confidence: 0.7}
	st, _, err = Apply(g, st, JevDecided{NodeID: "pick", Decision: dec, Available: true})
	if err != nil {
		t.Fatal(err)
	}
	if !st.Supervised["implement"] {
		t.Fatal("gather should mark the assigned node supervised")
	}
	// A single failure on a supervised node must exhaust immediately, not
	// retry up to the workflow's normal maxNodeAttempts of 3.
	st, effs, err := Apply(g, st, WorkCompleted{NodeID: "implement"})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if st.Status != StatusFailed || st.Outcome != "node-attempts-exhausted:implement" {
		t.Fatalf("expected a supervised node to fail after one attempt, got %+v", st)
	}
	_ = effs
}

func TestSelectActWithNoChosenIsAnError(t *testing.T) {
	g := buildGraph(t, selectGraph)
	st, _, err := NewRun(g)
	if err != nil {
		t.Fatal(err)
	}
	dec := registry.Decision{Decision: registry.Act, Confidence: 0.9}
	if _, _, err := Apply(g, st, JevDecided{NodeID: "pick", Decision: dec}); err == nil {
		t.Fatal("expected an error for an act decision with no chosen candidate")
	}
}

const gateGraph = `
version: 1
name: gateflow
description: d
nodes:
  - id: g
    kind: gate
    questionSet: sdlc.needs-delegation
    routes: {ready: yesdone, notready: nodone}
    trueRoute: ready
    default: nodone
  - id: yesdone
    kind: terminal
    outcome: succeeded
  - id: nodone
    kind: terminal
    outcome: aborted
`

func TestGateActTrueRoutesToTrueRoute(t *testing.T) {
	g := buildGraph(t, gateGraph)
	st, _, err := NewRun(g)
	if err != nil {
		t.Fatal(err)
	}
	dec := registry.Decision{Decision: registry.Act, Confidence: 0.95}
	st, _, err = Apply(g, st, JevDecided{NodeID: "g", Decision: dec, Available: true})
	if err != nil {
		t.Fatal(err)
	}
	if st.Outcome != "succeeded" {
		t.Fatalf("state = %+v", st)
	}
}

func TestGateGatherFalseValueRoutesToOtherRoute(t *testing.T) {
	g := buildGraph(t, gateGraph)
	st, _, err := NewRun(g)
	if err != nil {
		t.Fatal(err)
	}
	// Confidence below 0.5 while still classified "gather" (an unusual but
	// legal shape for a caller-constructed Decision in tests) must route to
	// the non-true route.
	dec := registry.Decision{Decision: registry.Gather, Confidence: 0.3}
	st, _, err = Apply(g, st, JevDecided{NodeID: "g", Decision: dec, Available: true})
	if err != nil {
		t.Fatal(err)
	}
	if st.Outcome != "aborted" {
		t.Fatalf("state = %+v", st)
	}
}

func TestGateFallbackRoutesToDefault(t *testing.T) {
	g := buildGraph(t, gateGraph)
	st, _, err := NewRun(g)
	if err != nil {
		t.Fatal(err)
	}
	// Even a high-looking confidence value must not matter once the decision
	// itself is fallback (e.g. option-not-offered, or Jev unavailable).
	dec := registry.Decision{Decision: registry.Fallback, Confidence: 0.9}
	st, _, err = Apply(g, st, JevDecided{NodeID: "g", Decision: dec, Available: false})
	if err != nil {
		t.Fatal(err)
	}
	if st.Outcome != "aborted" { // default: nodone
		t.Fatalf("state = %+v", st)
	}
}

const decideGraph = `
version: 1
name: decideflow
description: d
nodes:
  - id: d
    kind: decide
    questionSet: graph.failure-class
    routes: {"transient-runtime": retry, "agent-correctable": escalate}
    default: escalate
  - id: retry
    kind: terminal
    outcome: succeeded
  - id: escalate
    kind: terminal
    outcome: aborted
`

func TestDecideRoutesOnChosenLabel(t *testing.T) {
	g := buildGraph(t, decideGraph)
	st, _, err := NewRun(g)
	if err != nil {
		t.Fatal(err)
	}
	dec := registry.Decision{Decision: registry.Act, Chosen: ptr("transient-runtime"), Confidence: 0.95}
	st, _, err = Apply(g, st, JevDecided{NodeID: "d", Decision: dec})
	if err != nil {
		t.Fatal(err)
	}
	if st.Outcome != "succeeded" {
		t.Fatalf("state = %+v", st)
	}
}

func TestDecideFallbackRoutesToDefault(t *testing.T) {
	g := buildGraph(t, decideGraph)
	st, _, err := NewRun(g)
	if err != nil {
		t.Fatal(err)
	}
	dec := registry.Decision{Decision: registry.Fallback, Confidence: 0.2}
	st, _, err = Apply(g, st, JevDecided{NodeID: "d", Decision: dec})
	if err != nil {
		t.Fatal(err)
	}
	if st.Outcome != "aborted" {
		t.Fatalf("state = %+v", st)
	}
}

const humanGraph = `
version: 1
name: humanflow
description: d
nodes:
  - id: h
    kind: human
    prompt: "Continue?"
    routes: {continue: done, abort: aborted}
  - id: done
    kind: terminal
    outcome: succeeded
  - id: aborted
    kind: terminal
    outcome: aborted
`

func TestHumanRoutesOnResponse(t *testing.T) {
	g := buildGraph(t, humanGraph)
	st, effs, err := NewRun(g)
	if err != nil {
		t.Fatal(err)
	}
	wfh, ok := effs[0].(WaitForHuman)
	if !ok || wfh.Prompt != "Continue?" || len(wfh.Routes) != 2 {
		t.Fatalf("effect = %+v", effs[0])
	}
	st, _, err = Apply(g, st, HumanResponded{NodeID: "h", Route: "abort"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Outcome != "aborted" {
		t.Fatalf("state = %+v", st)
	}
}

const joinGraph = `
version: 1
name: joinflow
description: d
nodes:
  - id: a
    kind: work
    agent: self
    objective: "do a"
    next: j
  - id: j
    kind: join
    next: done
  - id: done
    kind: terminal
    outcome: succeeded
`

func TestJoinPassesThroughWithNoEffect(t *testing.T) {
	g := buildGraph(t, joinGraph)
	st, _, err := NewRun(g)
	if err != nil {
		t.Fatal(err)
	}
	st, effs, err := Apply(g, st, WorkCompleted{NodeID: "a", CostReported: true})
	if err != nil {
		t.Fatal(err)
	}
	// The join must not appear as its own external effect: entering it lands
	// straight on the terminal past it.
	if st.Current != "done" || st.Outcome != "succeeded" {
		t.Fatalf("state = %+v", st)
	}
	if fin, ok := effs[0].(RunFinished); !ok || fin.Outcome != "succeeded" {
		t.Fatalf("effect = %+v", effs[0])
	}
}

func TestBudgetActiveSecondsExceeded(t *testing.T) {
	g := buildGraph(t, simpleWorkGraph) // maxRunActiveSeconds defaults to 21600
	st, _, err := NewRun(g)
	if err != nil {
		t.Fatal(err)
	}
	st, effs, err := Apply(g, st, WorkCompleted{
		NodeID: "a", ProducedPaths: []string{"out.txt"}, CostReported: true,
		P: Progress{ActiveSeconds: 999999},
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != StatusFailed || st.Outcome != "budget-exceeded:maxRunActiveSeconds" {
		t.Fatalf("state = %+v", st)
	}
	if fin, ok := effs[0].(RunFinished); !ok || fin.Status != StatusFailed {
		t.Fatalf("effect = %+v", effs[0])
	}
}

func TestBudgetEstimatedCostExceeded(t *testing.T) {
	y := strings.Replace(simpleWorkGraph, "maxNodeAttempts: 2", "maxNodeAttempts: 2\n  maxEstimatedCostUsd: 1.0", 1)
	g := buildGraph(t, y)
	st, _, err := NewRun(g)
	if err != nil {
		t.Fatal(err)
	}
	st, _, err = Apply(g, st, WorkCompleted{
		NodeID: "a", ProducedPaths: []string{"out.txt"}, CostReported: true,
		P: Progress{EstimatedCostUsd: 5.0},
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != StatusFailed || st.Outcome != "budget-exceeded:maxEstimatedCostUsd" {
		t.Fatalf("state = %+v", st)
	}
}

func TestEventForWrongNodeIsRejected(t *testing.T) {
	g := buildGraph(t, simpleWorkGraph)
	st, _, err := NewRun(g)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Apply(g, st, WorkCompleted{NodeID: "done"}); err == nil {
		t.Fatal("expected an error for an event naming a node the run isn't waiting on")
	}
}

func TestApplyAfterTerminalIsRejected(t *testing.T) {
	g := buildGraph(t, simpleWorkGraph)
	st, _, err := NewRun(g)
	if err != nil {
		t.Fatal(err)
	}
	st, _, err = Apply(g, st, WorkCompleted{NodeID: "a", ProducedPaths: []string{"out.txt"}, CostReported: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Apply(g, st, WorkCompleted{NodeID: "a"}); err == nil {
		t.Fatal("expected an error for an event against a finished run")
	}
}

func TestMissingUsageWarnRecordsWarning(t *testing.T) {
	g := buildGraph(t, simpleWorkGraph) // missingUsage defaults to warn
	st, _, err := NewRun(g)
	if err != nil {
		t.Fatal(err)
	}
	st, _, err = Apply(g, st, WorkCompleted{NodeID: "a", ProducedPaths: []string{"out.txt"}, CostReported: false})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Warnings) != 1 {
		t.Fatalf("Warnings = %v", st.Warnings)
	}
}

func TestMissingUsageBlockFailsRun(t *testing.T) {
	y := strings.Replace(simpleWorkGraph, "maxNodeAttempts: 2", "maxNodeAttempts: 2\n  missingUsage: block", 1)
	g := buildGraph(t, y)
	st, _, err := NewRun(g)
	if err != nil {
		t.Fatal(err)
	}
	st, effs, err := Apply(g, st, WorkCompleted{NodeID: "a", ProducedPaths: []string{"out.txt"}, CostReported: false})
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != StatusFailed || st.Outcome != "missing-usage:a" {
		t.Fatalf("state = %+v", st)
	}
	if fin, ok := effs[0].(RunFinished); !ok || fin.Status != StatusFailed {
		t.Fatalf("effect = %+v", effs[0])
	}
}

// TestDeterminism replays the exact same (graph, state, event) sequence
// twice and requires byte-identical states at every step — what proves a
// resume can rebuild state purely by replaying a journal, with no second
// execution path.
func TestDeterminism(t *testing.T) {
	g := buildGraph(t, selectGraph)
	events := []Event{
		JevDecided{NodeID: "pick", Decision: registry.Decision{Decision: registry.Act, Chosen: ptr("codex-implementer"), Confidence: 0.9}, Available: true},
		WorkCompleted{NodeID: "implement", ProducedPaths: []string{"patch.diff"}, CostReported: true},
	}
	run := func() []State {
		st, _, err := NewRun(g)
		if err != nil {
			t.Fatal(err)
		}
		states := []State{st}
		for _, ev := range events {
			var err error
			st, _, err = Apply(g, st, ev)
			if err != nil {
				t.Fatal(err)
			}
			states = append(states, st)
		}
		return states
	}
	a, b := run(), run()
	for i := range a {
		aj, _ := json.Marshal(a[i])
		bj, _ := json.Marshal(b[i])
		if string(aj) != string(bj) {
			t.Fatalf("step %d diverged:\n%s\nvs\n%s", i, aj, bj)
		}
	}
}

// TestFullShipFeatureRun drives the plan's own real workflow end to end
// through every node kind it uses (work/self, gate, select, work/assigned,
// check, decide, human is left untouched on this particular path) to a
// successful terminal, exercising the reroute-unrolled graph exactly as
// compile produced it.
func TestFullShipFeatureRun(t *testing.T) {
	raw, err := os.ReadFile("../compile/testdata/ship-feature.yaml")
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	g := buildGraph(t, string(raw))

	st, effs, err := NewRun(g)
	if err != nil {
		t.Fatal(err)
	}
	mustWork := func(nodeID string) {
		t.Helper()
		rw, ok := effs[0].(RunWork)
		if !ok || rw.NodeID != nodeID {
			t.Fatalf("expected RunWork for %q, got %+v", nodeID, effs[0])
		}
	}
	apply := func(ev Event) {
		t.Helper()
		var err error
		st, effs, err = Apply(g, st, ev)
		if err != nil {
			t.Fatalf("Apply(%T): %v", ev, err)
		}
	}

	mustWork("write-spec")
	apply(WorkCompleted{NodeID: "write-spec", ProducedPaths: []string{"spec.md"}, CostReported: true})

	if _, ok := effs[0].(AskJev); !ok {
		t.Fatalf("expected AskJev for needs-agent gate, got %+v", effs[0])
	}
	apply(JevDecided{NodeID: "needs-agent", Decision: registry.Decision{Decision: registry.Act, Confidence: 0.95}, Available: true})

	if _, ok := effs[0].(AskJev); !ok {
		t.Fatalf("expected AskJev for pick-implementer select, got %+v", effs[0])
	}
	apply(JevDecided{NodeID: "pick-implementer", Decision: registry.Decision{Decision: registry.Act, Chosen: ptr("codex-implementer"), Confidence: 0.9}, Available: true})

	mustWork("implement")
	if st.Assigned["implement"] != "codex-implementer" {
		t.Fatalf("Assigned = %v", st.Assigned)
	}
	apply(WorkCompleted{NodeID: "implement", ProducedPaths: []string{"patch.diff"}, CostReported: false})
	if len(st.Warnings) != 1 {
		t.Fatalf("expected a missing-usage warning for the non-self implementer, got %v", st.Warnings)
	}

	apply(JevDecided{NodeID: "handoff-gate", Decision: registry.Decision{Decision: registry.Act, Confidence: 0.95}, Available: true})

	if rc, ok := effs[0].(RunCommand); !ok || rc.NodeID != "tests" {
		t.Fatalf("expected RunCommand for tests, got %+v", effs[0])
	}
	apply(CommandCompleted{NodeID: "tests", Route: "pass"})

	if _, ok := effs[0].(AskJev); !ok {
		t.Fatalf("expected AskJev for pick-reviewer, got %+v", effs[0])
	}
	apply(JevDecided{NodeID: "pick-reviewer", Decision: registry.Decision{Decision: registry.Fallback, Confidence: 0.2}, Available: false})

	mustWork("review")
	if st.Assigned["review"] != "opencode-reviewer" { // pick-reviewer's declared default
		t.Fatalf("Assigned = %v", st.Assigned)
	}
	apply(WorkCompleted{NodeID: "review", ProducedPaths: []string{"review.md"}, CostReported: false})

	apply(JevDecided{NodeID: "disposition", Decision: registry.Decision{Decision: registry.Act, Chosen: ptr("no-finding"), Confidence: 0.95}})

	if st.Status != StatusTerminal || st.Outcome != "succeeded" {
		t.Fatalf("expected the run to finish succeeded, got %+v", st)
	}
	if fin, ok := effs[0].(RunFinished); !ok || fin.Outcome != "succeeded" {
		t.Fatalf("effect = %+v", effs[0])
	}
}

func TestStateCloneIsIndependent(t *testing.T) {
	g := buildGraph(t, simpleWorkGraph)
	st, _, err := NewRun(g)
	if err != nil {
		t.Fatal(err)
	}
	clone := st.clone()
	clone.Attempts["a"] = 99
	if reflect.DeepEqual(st.Attempts, clone.Attempts) {
		t.Fatal("expected clone's map mutation not to affect the original")
	}
}
