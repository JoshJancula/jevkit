package engine

import (
	"fmt"

	"github.com/OWNER/jevkit/internal/sdlc/graph"
	"github.com/OWNER/jevkit/internal/sdlc/spec"
)

// FirstUnsatisfiedNode walks forward from g.Entry, skipping over a work node
// whose every required declared artifact is already in satisfied, and
// returns the first node it lands on that isn't such a node.
//
// Only a work node is ever skipped this way: seeding an artifact substitutes
// for the node that would have produced it, but a gate, select, decide or
// check node is a decision or verification step, never something an
// artifact's mere presence can stand in for — skipping one of those would
// mean silently picking an outcome nothing actually decided. A join is
// likewise never itself returned, matching engine.enter's own pass-through:
// walking through one costs nothing since it has no artifacts of its own.
func FirstUnsatisfiedNode(g *graph.Graph, satisfied map[string]bool) (string, error) {
	id := g.Entry
	seen := map[string]bool{}
	for {
		if seen[id] {
			return "", fmt.Errorf("engine: internal error: cycle while walking to first unsatisfied node at %q (compiled graphs must be acyclic)", id)
		}
		seen[id] = true
		n, ok := g.Node(id)
		if !ok {
			return "", fmt.Errorf("engine: internal error: unknown node %q", id)
		}
		switch n.Kind {
		case spec.KindWork:
			// A work node with no required artifact of its own has nothing
			// seeding could ever satisfy, so it must still run rather than
			// being vacuously "already done".
			if !hasRequiredArtifact(n) || !producedAllRequired(n, satisfiedKeys(n, satisfied)) {
				return id, nil
			}
			id = n.Next
		case spec.KindJoin:
			id = n.Next
		default:
			return id, nil
		}
	}
}

// hasRequiredArtifact reports whether n declares at least one required
// produced artifact.
func hasRequiredArtifact(n *graph.Node) bool {
	for _, a := range n.Produces {
		if a.Required {
			return true
		}
	}
	return false
}

// satisfiedKeys returns the subset of n's declared produced paths that are
// marked satisfied, the shape producedAllRequired expects.
func satisfiedKeys(n *graph.Node, satisfied map[string]bool) []string {
	var out []string
	for _, a := range n.Produces {
		if satisfied[a.Path] {
			out = append(out, a.Path)
		}
	}
	return out
}
