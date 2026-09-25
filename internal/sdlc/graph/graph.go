// Package graph is the frozen, read-only-at-run-time node/edge model that
// internal/sdlc/compile produces from a spec.Workflow. The engine executes
// only against a Graph: it never sees the authored YAML.
package graph

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/OWNER/jevkit/internal/sdlc/spec"
)

// Node is one compiled, frozen workflow node. Every route target it carries
// (Next, Default, Routes values) names a Node that exists in the same Graph.
type Node struct {
	ID          string            `json:"id"`
	Kind        string            `json:"kind"`
	Agent       string            `json:"agent,omitempty"`
	Produces    []spec.Artifact   `json:"produces,omitempty"`
	Consumes    []spec.Artifact   `json:"consumes,omitempty"`
	Objective   string            `json:"objective,omitempty"`
	Next        string            `json:"next,omitempty"`
	QuestionSet string            `json:"questionSet,omitempty"`
	State       *spec.StateSpec   `json:"state,omitempty"`
	Routes      map[string]string `json:"routes,omitempty"`
	Default     string            `json:"default,omitempty"`
	TrueRoute   string            `json:"trueRoute,omitempty"`
	Candidates  []string          `json:"candidates,omitempty"`
	AssignTo    string            `json:"assignTo,omitempty"`
	Command     string            `json:"command,omitempty"`
	Prompt      string            `json:"prompt,omitempty"`
	Outcome     string            `json:"outcome,omitempty"`

	// Origin is the authored node id this node was unrolled from, empty for
	// a node compiled 1:1 from the spec. UnrollIndex is that copy's ordinal
	// (2, 3, ...); the first copy keeps Origin's own id and UnrollIndex 0.
	Origin      string `json:"origin,omitempty"`
	UnrollIndex int    `json:"unrollIndex,omitempty"`
}

// Edges returns (label, target) for every outgoing edge in a stable order:
// "next" first (when present), then routes sorted by label, then "default"
// last. A select node is the one exception: its traversal edge is assignTo
// (the node that runs under the chosen agent) and its Default names a
// candidate agent id, not a node id, so Default is never reported as an edge
// for a select node.
func (n *Node) Edges() [][2]string {
	if n.Kind == spec.KindSelect {
		if n.AssignTo == "" {
			return nil
		}
		return [][2]string{{"assignTo", n.AssignTo}}
	}
	var out [][2]string
	if n.Next != "" {
		out = append(out, [2]string{"next", n.Next})
	}
	labels := make([]string, 0, len(n.Routes))
	for l := range n.Routes {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	for _, l := range labels {
		out = append(out, [2]string{l, n.Routes[l]})
	}
	if n.Default != "" {
		out = append(out, [2]string{"default", n.Default})
	}
	return out
}

// Graph is a compiled, sha256-pinned workflow. It is a pure DAG: compile
// unrolls every bounded cycle at compile time (see internal/sdlc/compile),
// so the engine never has to reason about loops at run time.
type Graph struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Budgets     spec.Budgets     `json:"budgets"`
	Entry       string           `json:"entry"`
	Nodes       map[string]*Node `json:"-"`
	// Order is the deterministic node order used for hashing and listing:
	// authored nodes first (in their spec order), then unrolled copies
	// grouped by origin and ordered by UnrollIndex.
	Order  []string `json:"-"`
	SHA256 string   `json:"-"`
}

// Node looks up a compiled node by id.
func (g *Graph) Node(id string) (*Node, bool) {
	n, ok := g.Nodes[id]
	return n, ok
}

// canonical is the shape Hash marshals: a sorted node slice makes the digest
// independent of Go map iteration order and of the authored YAML's node
// ordering, so semantically-neutral reorderings hash identically.
type canonical struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Budgets     spec.Budgets `json:"budgets"`
	Entry       string       `json:"entry"`
	Nodes       []*Node      `json:"nodes"`
}

// Hash computes the sha256 pin over the graph's canonical JSON encoding.
// It does not mutate g.SHA256; callers that want the pin recorded call
// Freeze.
func (g *Graph) Hash() (string, error) {
	ids := make([]string, 0, len(g.Nodes))
	for id := range g.Nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	nodes := make([]*Node, 0, len(ids))
	for _, id := range ids {
		nodes = append(nodes, g.Nodes[id])
	}
	c := canonical{Name: g.Name, Description: g.Description, Budgets: g.Budgets, Entry: g.Entry, Nodes: nodes}
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// Freeze computes and stores the sha256 pin on g, then returns it.
func (g *Graph) Freeze() (string, error) {
	sum, err := g.Hash()
	if err != nil {
		return "", err
	}
	g.SHA256 = sum
	return sum, nil
}
