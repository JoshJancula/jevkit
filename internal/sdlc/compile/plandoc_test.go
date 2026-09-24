package compile

import (
	"os"
	"testing"
)

// TestCompilePlanExample compiles the exact ship-feature workflow from the
// design plan end to end: multiple gate/select/decide/check/human nodes,
// three distinct reroute cycles (triage->tests, triage/disposition/human-scope
// ->pick-implementer, handoff-gate->implement), and two terminals.
func TestCompilePlanExample(t *testing.T) {
	raw, err := os.ReadFile("testdata/ship-feature.yaml")
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	w := mustLoad(t, string(raw))
	g, err := Compile(w)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if g.Entry != "write-spec" {
		t.Errorf("Entry = %q, want write-spec", g.Entry)
	}
	if g.SHA256 == "" {
		t.Error("expected a sha256 pin")
	}
	// Recompiling must be deterministic.
	g2, err := Compile(mustLoad(t, string(raw)))
	if err != nil {
		t.Fatalf("Compile (again): %v", err)
	}
	if g.SHA256 != g2.SHA256 {
		t.Errorf("recompiling the same workflow produced different hashes: %s vs %s", g.SHA256, g2.SHA256)
	}
	// Every declared node id must still resolve (no unrolled copy left a
	// dangling reference).
	for id, n := range g.Nodes {
		for _, e := range n.Edges() {
			if _, ok := g.Node(e[1]); !ok {
				t.Errorf("node %q edge %q points at undeclared node %q", id, e[0], e[1])
			}
		}
	}
}
