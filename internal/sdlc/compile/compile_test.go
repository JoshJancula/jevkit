package compile

import (
	"strings"
	"testing"

	"github.com/OWNER/jevkit/internal/sdlc/spec"
)

func mustLoad(t *testing.T, y string) *spec.Workflow {
	t.Helper()
	w, err := spec.Load([]byte(y))
	if err != nil {
		t.Fatalf("spec.Load: %v", err)
	}
	return w
}

const acyclicYAML = `
version: 1
name: acyclic
description: d
nodes:
  - id: write-spec
    kind: work
    agent: self
    next: implement
    produces: [{path: spec.md, required: true, seedable: true}]
  - id: implement
    kind: work
    agent: self
    next: done
    consumes: [{path: spec.md, required: true}]
    produces: [{path: patch.diff, required: true}]
  - id: done
    kind: terminal
    outcome: succeeded
`

func TestCompileAcyclic(t *testing.T) {
	w := mustLoad(t, acyclicYAML)
	g, err := Compile(w)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if g.SHA256 == "" {
		t.Error("expected a non-empty sha256 pin")
	}
	if len(g.Nodes) != 3 {
		t.Errorf("len(Nodes) = %d, want 3 (no unrolling expected)", len(g.Nodes))
	}
	n, ok := g.Node("implement")
	if !ok || len(n.Consumes) != 1 || n.Consumes[0].Path != "spec.md" {
		t.Errorf("artifact-derived consumes not preserved: %+v", n)
	}
}

// A 2-node reroute cycle (tests <-> triage), matching the plan's own example.
const rerouteYAML = `
version: 1
name: reroute
description: d
budgets:
  maxReroutes: 2
nodes:
  - id: implement
    kind: work
    agent: self
    next: tests
  - id: tests
    kind: check
    command: "go test ./..."
    routes: {pass: done, fail: triage}
  - id: triage
    kind: decide
    questionSet: graph.failure-class
    routes:
      transient-runtime: tests
      agent-correctable: human-scope
    default: human-scope
  - id: human-scope
    kind: human
    prompt: "Continue?"
    routes: {continue: done}
  - id: done
    kind: terminal
    outcome: succeeded
`

func TestCompileUnrollsReroute(t *testing.T) {
	w := mustLoad(t, rerouteYAML)
	g, err := Compile(w)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	// budgets.maxReroutes: 2 => 3 total copies of the {tests, triage} cycle.
	for _, id := range []string{"tests", "triage"} {
		if _, ok := g.Node(id); !ok {
			t.Errorf("expected original node %q to survive unrolling", id)
		}
		for _, suffix := range []string{"~2", "~3"} {
			if _, ok := g.Node(id + suffix); !ok {
				t.Errorf("expected unrolled copy %q", id+suffix)
			}
		}
		if _, ok := g.Node(id + "~4"); ok {
			t.Errorf("did not expect a 4th copy of %q (budget is maxReroutes=2 => 3 copies)", id)
		}
	}
	// The last copy's failure route must go to the overflow terminal, not
	// back into the loop.
	last, ok := g.Node("triage~3")
	if !ok {
		t.Fatal("missing triage~3")
	}
	overflow := last.Routes["transient-runtime"]
	if !strings.HasSuffix(overflow, "budget-exhausted") {
		t.Errorf("triage~3 back edge = %q, want it redirected to a budget-exhausted terminal", overflow)
	}
	oflowNode, ok := g.Node(overflow)
	if !ok || oflowNode.Kind != spec.KindTerminal {
		t.Errorf("overflow target %q is not a terminal node: %+v", overflow, oflowNode)
	}
	// A route leaving the cycle (agent-correctable -> human-scope) must be
	// preserved, unchanged, in every copy.
	if last.Routes["agent-correctable"] != "human-scope" {
		t.Errorf("triage~3 agent-correctable route = %q, want human-scope (unchanged exit edge)", last.Routes["agent-correctable"])
	}
	first, _ := g.Node("triage")
	if first.Routes["agent-correctable"] != "human-scope" {
		t.Errorf("original triage agent-correctable route = %q, want human-scope", first.Routes["agent-correctable"])
	}
	// The internal edge tests -> triage must remain pointed within each copy.
	tests2, ok := g.Node("tests~2")
	if !ok || tests2.Routes["fail"] != "triage~2" {
		t.Errorf("tests~2 fail route = %v, want triage~2", tests2)
	}
}

func TestCompileZeroReroutesStillProducesOneCopyAndOverflow(t *testing.T) {
	y := strings.Replace(rerouteYAML, "maxReroutes: 2", "maxReroutes: 0", 1)
	w := mustLoad(t, y)
	g, err := Compile(w)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	first, _ := g.Node("triage")
	overflow := first.Routes["transient-runtime"]
	if !strings.HasSuffix(overflow, "budget-exhausted") {
		t.Errorf("with maxReroutes=0 the only copy's back edge should go straight to overflow, got %q", overflow)
	}
	if _, ok := g.Node("triage~2"); ok {
		t.Error("did not expect any unrolled copies when maxReroutes=0")
	}
}

func TestCompileUnreachableNode(t *testing.T) {
	y := `
version: 1
name: unreach
description: d
nodes:
  - id: entry
    kind: work
    agent: self
    next: done
  - id: done
    kind: terminal
    outcome: succeeded
  - id: x
    kind: work
    agent: self
    next: y
  - id: y
    kind: check
    command: "go test ./..."
    routes: {pass: island-done, fail: x}
  - id: island-done
    kind: terminal
    outcome: succeeded
`
	// x, y and island-done form a small island with no incoming edge from
	// the main graph, so none of them is ambiguous as an entry candidate
	// (each has an incoming edge from within the island) but the whole
	// island is unreachable from "entry".
	w := mustLoad(t, y)
	_, err := Compile(w)
	if err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("expected unreachable-node error, got %v", err)
	}
}

func TestHashStableAcrossNodeReordering(t *testing.T) {
	w1 := mustLoad(t, acyclicYAML)
	g1, err := Compile(w1)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	reordered := `
version: 1
name: acyclic
description: d
nodes:
  - id: done
    kind: terminal
    outcome: succeeded
  - id: implement
    kind: work
    agent: self
    next: done
    consumes: [{path: spec.md, required: true}]
    produces: [{path: patch.diff, required: true}]
  - id: write-spec
    kind: work
    agent: self
    next: implement
    produces: [{path: spec.md, required: true, seedable: true}]
`
	w2 := mustLoad(t, reordered)
	g2, err := Compile(w2)
	if err != nil {
		t.Fatalf("Compile (reordered): %v", err)
	}
	if g1.SHA256 != g2.SHA256 {
		t.Errorf("hash changed across a semantically-neutral node reordering: %s vs %s", g1.SHA256, g2.SHA256)
	}
}

func TestHashChangesWithSemanticChange(t *testing.T) {
	w1 := mustLoad(t, acyclicYAML)
	g1, err := Compile(w1)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	changed := strings.Replace(acyclicYAML, "outcome: succeeded", "outcome: aborted", 1)
	w2 := mustLoad(t, changed)
	g2, err := Compile(w2)
	if err != nil {
		t.Fatalf("Compile (changed): %v", err)
	}
	if g1.SHA256 == g2.SHA256 {
		t.Error("expected hash to change when the graph's semantics change")
	}
}
