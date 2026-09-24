// Package compile turns a validated spec.Workflow into a frozen, acyclic
// graph.Graph: it resolves every reroute loop by unrolling it up to the
// workflow's budgets.maxReroutes bound, so the engine only ever walks a DAG.
//
// A node's own retry (the same node re-attempted up to
// budgets.maxNodeAttempts) is a runtime engine concern, not a graph edge, and
// so is never unrolled here: it never appears as a cycle in the compiled
// graph. What compile unrolls is a *reroute* loop — one or more routes that
// send work back to an earlier node (e.g. a failure-triage node routing back
// to the implementer it came from, or a review disposition doing the same).
// A workflow's reroute loops commonly share nodes — triage, disposition and
// the human-scope gate can all route back into the same pick-implementer —
// so the unit of unrolling is a whole strongly connected component (SCC), not
// one cycle at a time: unrolling cycles independently when they overlap does
// not converge (each unroll can re-close a different cycle through the nodes
// it shares with the first).
//
// Within an SCC, compile runs one DFS from a deterministic start node and
// classifies each internal edge as either a *tree/forward/cross* edge (target
// not currently on the DFS stack) or a *back* edge (target currently on the
// stack, i.e. an ancestor) — the standard compiler notion of a retreating
// edge that closes a loop. budgets.maxReroutes+1 layered copies of the SCC
// are built: every non-back edge stays within its own layer, every back edge
// advances to the next layer, and the last layer's back edges are redirected
// to a synthesized terminal node instead of layer 0, so a run can never loop
// forever.
package compile

import (
	"fmt"
	"sort"
	"strings"

	"github.com/OWNER/jevkit/internal/sdlc/graph"
	"github.com/OWNER/jevkit/internal/sdlc/spec"
)

// Compile validates w, then builds and freezes the DAG.
func Compile(w *spec.Workflow) (*graph.Graph, error) {
	if err := w.Validate(); err != nil {
		return nil, err
	}
	g, err := fromSpec(w)
	if err != nil {
		return nil, err
	}

	for _, scc := range stronglyConnectedComponents(g) {
		if len(scc) == 1 && !selfLoop(g.Nodes[scc[0]]) {
			continue // a trivial, non-looping component needs no unrolling.
		}
		if err := unrollSCC(g, scc, w.Budgets.MaxReroutes); err != nil {
			return nil, err
		}
	}

	if cycle := findCycle(g); cycle != nil {
		return nil, &CycleError{Cycle: cycle}
	}
	if err := checkReachable(g); err != nil {
		return nil, err
	}
	if _, err := g.Freeze(); err != nil {
		return nil, fmt.Errorf("sdlc: compile: hash: %w", err)
	}
	return g, nil
}

// CycleError names a cycle compile could not resolve. Reaching this after
// unrollSCC has run over every SCC would mean the SCC decomposition itself
// was wrong, since every nontrivial SCC is unrolled into an acyclic layered
// copy; it is kept as a defensive final check, not a documented failure mode.
type CycleError struct {
	Cycle []string
}

func (e *CycleError) Error() string {
	return fmt.Sprintf("sdlc: compile: cycle could not be resolved: %s", strings.Join(append(e.Cycle, e.Cycle[0]), " -> "))
}

func selfLoop(n *graph.Node) bool {
	for _, e := range n.Edges() {
		if e[1] == n.ID {
			return true
		}
	}
	return false
}

// fromSpec builds the initial (possibly cyclic) graph 1:1 from w. The entry
// node is derived structurally (the one node nothing routes to) rather than
// taken as "whichever node the author listed first", so reordering a
// workflow's node list never changes its compiled meaning or its hash.
func fromSpec(w *spec.Workflow) (*graph.Graph, error) {
	entry, err := entryNode(w)
	if err != nil {
		return nil, err
	}
	g := &graph.Graph{
		Name:        w.Name,
		Description: w.Description,
		Budgets:     w.Budgets,
		Entry:       entry,
		Nodes:       make(map[string]*graph.Node, len(w.Nodes)),
	}
	for _, n := range w.Nodes {
		g.Nodes[n.ID] = nodeFromSpec(n)
		g.Order = append(g.Order, n.ID)
	}
	return g, nil
}

// entryNode returns the id of the one node with no incoming edge (next,
// route target, or a select node's assignTo). A select node's default names
// a candidate agent id, not a node id, and is not an edge.
func entryNode(w *spec.Workflow) (string, error) {
	incoming := map[string]bool{}
	for _, n := range w.Nodes {
		for _, tgt := range specTargets(n) {
			incoming[tgt] = true
		}
	}
	var candidates []string
	for _, n := range w.Nodes {
		if !incoming[n.ID] {
			candidates = append(candidates, n.ID)
		}
	}
	if len(candidates) != 1 {
		sort.Strings(candidates)
		return "", fmt.Errorf("sdlc: compile: workflow must have exactly one entry node (no incoming edge); found %d: %v", len(candidates), candidates)
	}
	return candidates[0], nil
}

// specTargets lists the node-id targets n's edges point at, pre-compile.
func specTargets(n spec.Node) []string {
	var out []string
	if n.Next != "" {
		out = append(out, n.Next)
	}
	for _, v := range n.Routes {
		out = append(out, v)
	}
	if n.Kind == spec.KindSelect {
		if n.AssignTo != "" {
			out = append(out, n.AssignTo)
		}
	} else if n.Default != "" {
		out = append(out, n.Default)
	}
	return out
}

func nodeFromSpec(n spec.Node) *graph.Node {
	var routes map[string]string
	if len(n.Routes) > 0 {
		routes = make(map[string]string, len(n.Routes))
		for k, v := range n.Routes {
			routes[k] = v
		}
	}
	return &graph.Node{
		ID: n.ID, Kind: n.Kind, Agent: n.Agent,
		Produces: n.Produces, Consumes: n.Consumes, Objective: n.Objective,
		Next: n.Next, QuestionSet: n.QuestionSet, State: n.State,
		Routes: routes, Default: n.Default, TrueRoute: n.TrueRoute, Candidates: n.Candidates,
		AssignTo: n.AssignTo, Command: n.Command, Prompt: n.Prompt, Outcome: n.Outcome,
	}
}

// stronglyConnectedComponents runs Tarjan's algorithm over g and returns its
// SCCs. Order is deterministic: nodes are visited in g.Order, and each SCC's
// own member list is in the order its nodes were popped off the DFS stack.
func stronglyConnectedComponents(g *graph.Graph) [][]string {
	index := 0
	indices := map[string]int{}
	lowlink := map[string]int{}
	onStack := map[string]bool{}
	var stack []string
	var sccs [][]string

	var strongconnect func(v string)
	strongconnect = func(v string) {
		indices[v] = index
		lowlink[v] = index
		index++
		stack = append(stack, v)
		onStack[v] = true

		for _, e := range g.Nodes[v].Edges() {
			w := e[1]
			if _, seen := indices[w]; !seen {
				strongconnect(w)
				if lowlink[w] < lowlink[v] {
					lowlink[v] = lowlink[w]
				}
			} else if onStack[w] {
				if indices[w] < lowlink[v] {
					lowlink[v] = indices[w]
				}
			}
		}

		if lowlink[v] == indices[v] {
			var scc []string
			for {
				n := len(stack) - 1
				w := stack[n]
				stack = stack[:n]
				onStack[w] = false
				scc = append(scc, w)
				if w == v {
					break
				}
			}
			sccs = append(sccs, scc)
		}
	}

	for _, id := range g.Order {
		if _, seen := indices[id]; !seen {
			strongconnect(id)
		}
	}
	return sccs
}

// idFor names copy c (0-based) of an SCC-member node id; copy 0 keeps the
// original id, so untouched external edges into the SCC still resolve.
func idFor(id string, c int) string {
	if c == 0 {
		return id
	}
	return fmt.Sprintf("%s~%d", id, c+1)
}

// unrollSCC replaces one nontrivial SCC (or self-loop) with budgetCopies+1
// layered copies, classifying each internal edge as forward (stays in-layer)
// or back (advances a layer, or exits to the overflow terminal on the last
// layer) via one DFS from a deterministic start node.
func unrollSCC(g *graph.Graph, scc []string, budgetCopies int) error {
	member := make(map[string]bool, len(scc))
	for _, id := range scc {
		member[id] = true
	}
	start := sccEntry(g, scc, member)

	back := map[[2]string]bool{} // (nodeID, label) -> is a back edge
	color := map[string]int{}    // 0 white, 1 gray, 2 black
	var classify func(id string)
	classify = func(id string) {
		color[id] = 1
		for _, e := range g.Nodes[id].Edges() {
			label, tgt := e[0], e[1]
			if !member[tgt] {
				continue
			}
			switch color[tgt] {
			case 1: // gray: an ancestor on the current DFS path.
				back[[2]string{id, label}] = true
			case 0:
				classify(tgt)
			}
		}
		color[id] = 2
	}
	classify(start)
	for _, id := range scc {
		if color[id] == 0 {
			// Every SCC member is mutually reachable from every other by
			// definition; an unvisited member would mean stronglyConnectedComponents
			// built an incorrect component.
			return fmt.Errorf("sdlc: compile: internal error: %q unreachable within its own SCC from %q", id, start)
		}
	}

	copies := budgetCopies + 1
	if copies < 1 {
		copies = 1
	}
	overflow := overflowNodeID(g, start)
	if _, exists := g.Nodes[overflow]; !exists {
		g.Nodes[overflow] = &graph.Node{ID: overflow, Kind: spec.KindTerminal, Outcome: "aborted"}
		g.Order = append(g.Order, overflow)
	}

	// Build every layer (0 reuses the original nodes in place; 1..copies-1
	// are fresh clones), then rewrite each layer's edges in a second pass so
	// every layer's node already exists when it is referenced.
	for c := 1; c < copies; c++ {
		for _, id := range scc {
			clone := cloneNode(g.Nodes[id])
			clone.ID = idFor(id, c)
			clone.Origin = id
			clone.UnrollIndex = c
			g.Nodes[clone.ID] = clone
			g.Order = append(g.Order, clone.ID)
		}
	}
	for c := 0; c < copies; c++ {
		for _, id := range scc {
			n := g.Nodes[idFor(id, c)]
			rewriteLayerEdges(n, id, c, copies, member, back, overflow)
		}
	}
	return nil
}

// rewriteLayerEdges rewrites node n (copy c of original origID) in place: a
// forward edge to an SCC member stays within layer c; a back edge advances to
// layer c+1, or to overflow when c is the last layer; an edge leaving the SCC
// is left exactly as authored.
func rewriteLayerEdges(n *graph.Node, origID string, c, copies int, member map[string]bool, back map[[2]string]bool, overflow string) {
	remap := func(label, target string) string {
		if target == "" || !member[target] {
			return target
		}
		if back[[2]string{origID, label}] {
			if c == copies-1 {
				return overflow
			}
			return idFor(target, c+1)
		}
		return idFor(target, c)
	}
	n.Next = remap("next", n.Next)
	n.AssignTo = remap("assignTo", n.AssignTo)
	if n.Kind != spec.KindSelect {
		n.Default = remap("default", n.Default)
	}
	if len(n.Routes) > 0 {
		routes := make(map[string]string, len(n.Routes))
		for label, tgt := range n.Routes {
			routes[label] = remap(label, tgt)
		}
		n.Routes = routes
	}
}

func sortedCopy(ids []string) []string {
	out := append([]string{}, ids...)
	sort.Strings(out)
	return out
}

// sccEntry picks the DFS root used to classify an SCC's back edges: the
// member node(s) reachable from outside the SCC (or the graph's own entry,
// for the SCC containing it). This matters because the root becomes the loop
// header — every edge into it from within the SCC becomes a back edge that
// advances a layer — so layer 0 stays reachable from wherever the rest of the
// graph actually enters this component, instead of an arbitrary node
// swallowing the only edge that reaches it.
//
// An SCC with more than one external entry point (an "irreducible" loop, in
// compiler terms) picks the lexicographically smallest of them: unrolling
// still produces a correct, terminating DAG, but a node reachable only
// through one of the other entry points may unroll less intuitively.
func sccEntry(g *graph.Graph, scc []string, member map[string]bool) string {
	var candidates []string
	for _, id := range scc {
		if id == g.Entry {
			return id
		}
	}
	for _, id := range scc {
		for _, other := range g.Order {
			if member[other] {
				continue
			}
			for _, e := range g.Nodes[other].Edges() {
				if e[1] == id {
					candidates = append(candidates, id)
				}
			}
		}
	}
	if len(candidates) == 0 {
		// Unreachable from outside the SCC (and not the graph entry): dead
		// code that checkReachable will reject regardless of which node
		// becomes the loop header here.
		return sortedCopy(scc)[0]
	}
	return sortedCopy(candidates)[0]
}

func cloneNode(n *graph.Node) *graph.Node {
	c := *n
	if n.Routes != nil {
		c.Routes = make(map[string]string, len(n.Routes))
		for k, v := range n.Routes {
			c.Routes[k] = v
		}
	}
	if n.Produces != nil {
		c.Produces = append([]spec.Artifact{}, n.Produces...)
	}
	if n.Consumes != nil {
		c.Consumes = append([]spec.Artifact{}, n.Consumes...)
	}
	if n.Candidates != nil {
		c.Candidates = append([]string{}, n.Candidates...)
	}
	return &c
}

func overflowNodeID(g *graph.Graph, start string) string {
	base := start + "-budget-exhausted"
	id := base
	for i := 2; ; i++ {
		if _, exists := g.Nodes[id]; !exists {
			return id
		}
		// Only reached if an authored node happens to collide with the
		// synthesized name; disambiguate deterministically.
		id = fmt.Sprintf("%s-%d", base, i)
	}
}

// findCycle runs a deterministic DFS from the entry node (falling back to
// every node in Order, so a cycle unreachable from Entry is still found and
// reported) and returns the first cycle discovered as an ordered list of node
// ids, or nil if the graph is acyclic.
func findCycle(g *graph.Graph) []string {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(g.Nodes))
	var stack []string
	var cycle []string

	var visit func(id string) bool
	visit = func(id string) bool {
		color[id] = gray
		stack = append(stack, id)
		n := g.Nodes[id]
		for _, e := range n.Edges() {
			tgt := e[1]
			switch color[tgt] {
			case gray:
				for i, s := range stack {
					if s == tgt {
						cycle = append([]string{}, stack[i:]...)
						return true
					}
				}
			case white:
				if visit(tgt) {
					return true
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[id] = black
		return cycle != nil
	}

	order := append([]string{}, g.Entry)
	order = append(order, g.Order...)
	for _, id := range order {
		if _, ok := g.Nodes[id]; !ok {
			continue
		}
		if color[id] == white {
			if visit(id) {
				return cycle
			}
		}
	}
	return nil
}

// checkReachable rejects any node the entry can never reach, naming every
// one found in Order.
func checkReachable(g *graph.Graph) error {
	seen := map[string]bool{}
	var walk func(string)
	walk = func(id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		n, ok := g.Nodes[id]
		if !ok {
			return
		}
		for _, e := range n.Edges() {
			walk(e[1])
		}
	}
	walk(g.Entry)
	unreached := make([]string, 0)
	for _, id := range g.Order {
		if !seen[id] {
			unreached = append(unreached, id)
		}
	}
	if len(unreached) > 0 {
		sort.Strings(unreached)
		return fmt.Errorf("sdlc: compile: unreachable from entry %q: %s", g.Entry, strings.Join(unreached, ", "))
	}
	return nil
}
