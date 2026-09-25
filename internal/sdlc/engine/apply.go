package engine

import (
	"fmt"
	"sort"

	"github.com/OWNER/jevkit/internal/registry"
	"github.com/OWNER/jevkit/internal/sdlc/graph"
	"github.com/OWNER/jevkit/internal/sdlc/spec"
)

// NewRun starts a fresh run at g's entry node.
func NewRun(g *graph.Graph) (State, []Effect, error) {
	return NewRunAt(g, g.Entry)
}

// NewRunAt starts a fresh run at startNode instead of g.Entry: the seeded-
// artifact entry point a `sdlc start --file` skips forward to. startNode
// must be a real node in g.
func NewRunAt(g *graph.Graph, startNode string) (State, []Effect, error) {
	return enter(g, newState(), startNode)
}

// Apply advances st by resolving the Effect that produced ev. It is a pure
// function: st is never mutated in place, and calling it twice with the same
// (g, st, ev) always returns the same result — which is what lets a ledger
// replay a run's event journal to rebuild identical state on resume, with no
// second execution path.
func Apply(g *graph.Graph, st State, ev Event) (State, []Effect, error) {
	st = st.clone()
	p := ev.progress()
	st.ActiveSeconds = p.ActiveSeconds
	st.EstimatedCostUsd = p.EstimatedCostUsd

	if st.Status != StatusRunning {
		return st, nil, fmt.Errorf("engine: run is not active (status=%s)", st.Status)
	}
	if fin, ok := checkBudgetCeilings(g, st); ok {
		return fin, []Effect{RunFinished{Status: fin.Status, Outcome: fin.Outcome}}, nil
	}

	switch e := ev.(type) {
	case WorkCompleted:
		return applyWorkCompleted(g, st, e)
	case CommandCompleted:
		return applyCommandCompleted(g, st, e)
	case JevDecided:
		return applyJevDecided(g, st, e)
	case HumanResponded:
		return applyHumanResponded(g, st, e)
	default:
		return st, nil, fmt.Errorf("engine: unrecognized event %T", ev)
	}
}

// checkBudgetCeilings aborts the run when the caller-reported cumulative
// spend exceeds a declared ceiling. A zero ceiling is not "no limit" here —
// spec.Workflow.Validate already requires maxRunActiveSeconds >= 1, and
// maxEstimatedCostUsd's zero value (the field has no required minimum) is
// treated as "no cost ceiling declared", matching a workflow that never set
// budgets.maxEstimatedCostUsd at all.
func checkBudgetCeilings(g *graph.Graph, st State) (State, bool) {
	if g.Budgets.MaxRunActiveSeconds > 0 && st.ActiveSeconds > g.Budgets.MaxRunActiveSeconds {
		st.Status, st.Outcome = StatusFailed, "budget-exceeded:maxRunActiveSeconds"
		return st, true
	}
	if g.Budgets.MaxEstimatedCostUsd > 0 && st.EstimatedCostUsd > g.Budgets.MaxEstimatedCostUsd {
		st.Status, st.Outcome = StatusFailed, "budget-exceeded:maxEstimatedCostUsd"
		return st, true
	}
	return st, false
}

// requireCurrent rejects an event that does not resolve the effect the run
// is actually waiting on: a stale or misrouted event must never silently
// mutate a different node's state.
func requireCurrent(st State, nodeID string) error {
	if st.Current != nodeID {
		return fmt.Errorf("engine: event for node %q but run is waiting on %q", nodeID, st.Current)
	}
	return nil
}

// nodeOfKind resolves nodeID and checks its kind, the shape check every
// event handler needs before touching kind-specific fields.
func nodeOfKind(g *graph.Graph, nodeID, kind string) (*graph.Node, error) {
	n, ok := g.Node(nodeID)
	if !ok {
		return nil, fmt.Errorf("engine: internal error: unknown node %q", nodeID)
	}
	if n.Kind != kind {
		return nil, fmt.Errorf("engine: node %q is kind %q, not %q", nodeID, n.Kind, kind)
	}
	return n, nil
}

// enter transitions st onto nodeID and produces its Effect, recursing through
// any node kind that needs no external resolution (join passes straight
// through; a terminal ends the run) until it reaches one that does.
func enter(g *graph.Graph, st State, nodeID string) (State, []Effect, error) {
	n, ok := g.Node(nodeID)
	if !ok {
		return st, nil, fmt.Errorf("engine: internal error: unknown node %q", nodeID)
	}
	st.Current = nodeID

	switch n.Kind {
	case spec.KindWork:
		assignment, err := resolveAssignment(st, n)
		if err != nil {
			return st, nil, err
		}
		attempt := st.Attempts[nodeID] + 1
		st.Attempts[nodeID] = attempt
		return st, []Effect{RunWork{
			NodeID: nodeID, Attempt: attempt, Assignment: assignment,
			Objective: n.Objective, Consumes: n.Consumes, Produces: n.Produces,
		}}, nil

	case spec.KindCheck:
		attempt := st.Attempts[nodeID] + 1
		st.Attempts[nodeID] = attempt
		return st, []Effect{RunCommand{NodeID: nodeID, Attempt: attempt, Command: n.Command}}, nil

	case spec.KindSelect:
		return st, []Effect{AskJev{
			NodeID: nodeID, Kind: AskJevSelect, QuestionSet: n.QuestionSet,
			Candidates: append([]string{}, n.Candidates...), State: n.State,
		}}, nil

	case spec.KindGate:
		return st, []Effect{AskJev{NodeID: nodeID, Kind: AskJevGate, QuestionSet: n.QuestionSet, State: n.State}}, nil

	case spec.KindDecide:
		return st, []Effect{AskJev{NodeID: nodeID, Kind: AskJevDecide, QuestionSet: n.QuestionSet, State: n.State}}, nil

	case spec.KindHuman:
		routes := make([]string, 0, len(n.Routes))
		for label := range n.Routes {
			routes = append(routes, label)
		}
		sort.Strings(routes)
		return st, []Effect{WaitForHuman{NodeID: nodeID, Prompt: n.Prompt, Routes: routes}}, nil

	case spec.KindJoin:
		return enter(g, st, n.Next)

	case spec.KindTerminal:
		st.Status, st.Outcome = StatusTerminal, n.Outcome
		return st, []Effect{RunFinished{Status: StatusTerminal, Outcome: n.Outcome}}, nil

	default:
		return st, nil, fmt.Errorf("engine: internal error: node %q has unknown kind %q", nodeID, n.Kind)
	}
}

// resolveAssignment reports which agent should run a work node: itself, when
// pinned agent: self, or whichever agent a preceding select node assigned.
func resolveAssignment(st State, n *graph.Node) (Assignment, error) {
	if n.Agent == "self" {
		return Assignment{Self: true}, nil
	}
	agentID, ok := st.Assigned[n.ID]
	if !ok {
		return Assignment{}, fmt.Errorf("engine: work node %q has no agent assignment yet (no select node has resolved to it)", n.ID)
	}
	return Assignment{AgentID: agentID}, nil
}

// retryOrFinish is the one attempts ladder shared by work and check nodes: a
// failed execution (or, for work, a missing required artifact — see
// WorkCompleted's doc comment) retries the same node up to
// budgets.maxNodeAttempts, or budgets.maxNodeAttempts capped to 1 when the
// node is Supervised (a "gather"-confidence select assignment: a failure
// there escalates rather than retrying the same low-confidence agent).
// Attempts are exhausted -> the run fails outright, since the compiled graph
// declares no edge for that outcome.
func retryOrFinish(g *graph.Graph, st State, nodeID string, remake func(attempt int) Effect) (State, []Effect, error) {
	limit := g.Budgets.MaxNodeAttempts
	if st.Supervised[nodeID] && limit > 1 {
		limit = 1
	}
	if st.Attempts[nodeID] < limit {
		attempt := st.Attempts[nodeID] + 1
		st.Attempts[nodeID] = attempt
		return st, []Effect{remake(attempt)}, nil
	}
	st.Status, st.Outcome = StatusFailed, "node-attempts-exhausted:"+nodeID
	return st, []Effect{RunFinished{Status: StatusFailed, Outcome: st.Outcome}}, nil
}

func applyWorkCompleted(g *graph.Graph, st State, e WorkCompleted) (State, []Effect, error) {
	if err := requireCurrent(st, e.NodeID); err != nil {
		return st, nil, err
	}
	n, err := nodeOfKind(g, e.NodeID, spec.KindWork)
	if err != nil {
		return st, nil, err
	}
	assignment, err := resolveAssignment(st, n)
	if err != nil {
		return st, nil, err
	}

	if e.ExecutionFailed || !producedAllRequired(n, e.ProducedPaths) {
		return retryOrFinish(g, st, e.NodeID, func(attempt int) Effect {
			return RunWork{
				NodeID: e.NodeID, Attempt: attempt, Assignment: assignment,
				Objective: n.Objective, Consumes: n.Consumes, Produces: n.Produces,
			}
		})
	}

	for _, p := range e.ProducedPaths {
		st.Satisfied[p] = true
	}
	if !e.CostReported {
		st = applyMissingUsage(g, st, e.NodeID)
		if st.Status != StatusRunning {
			return st, []Effect{RunFinished{Status: st.Status, Outcome: st.Outcome}}, nil
		}
	}
	return enter(g, st, n.Next)
}

// producedAllRequired reports whether every required declared artifact is
// among produced — a structural fact the engine can check on its own,
// without Jev: whether the work was merely *attempted* is mechanical, unlike
// whether it was *good enough*, which is a downstream gate/decide node's job
// (the plan's "gates matter more than the selector").
func producedAllRequired(n *graph.Node, produced []string) bool {
	have := make(map[string]bool, len(produced))
	for _, p := range produced {
		have[p] = true
	}
	for _, a := range n.Produces {
		if a.Required && !have[a.Path] {
			return false
		}
	}
	return true
}

// applyMissingUsage enforces budgets.missingUsage when a work node's cost
// could not be itemized (an opaque, native-subagent cost, per the plan's
// Cost section): warn records a Warning and continues, block fails the run,
// ignore does nothing.
func applyMissingUsage(g *graph.Graph, st State, nodeID string) State {
	switch g.Budgets.MissingUsage {
	case "block":
		st.Status, st.Outcome = StatusFailed, "missing-usage:"+nodeID
	case "warn":
		st.Warnings = append(st.Warnings, fmt.Sprintf("node %q: cost could not be itemized", nodeID))
	}
	return st
}

func applyCommandCompleted(g *graph.Graph, st State, e CommandCompleted) (State, []Effect, error) {
	if err := requireCurrent(st, e.NodeID); err != nil {
		return st, nil, err
	}
	n, err := nodeOfKind(g, e.NodeID, spec.KindCheck)
	if err != nil {
		return st, nil, err
	}
	if e.ExecutionFailed {
		return retryOrFinish(g, st, e.NodeID, func(attempt int) Effect {
			return RunCommand{NodeID: e.NodeID, Attempt: attempt, Command: n.Command}
		})
	}
	target, ok := n.Routes[e.Route]
	if !ok {
		return st, nil, fmt.Errorf("engine: node %q: %q is not a declared route", e.NodeID, e.Route)
	}
	return enter(g, st, target)
}

func applyHumanResponded(g *graph.Graph, st State, e HumanResponded) (State, []Effect, error) {
	if err := requireCurrent(st, e.NodeID); err != nil {
		return st, nil, err
	}
	n, err := nodeOfKind(g, e.NodeID, spec.KindHuman)
	if err != nil {
		return st, nil, err
	}
	target, ok := n.Routes[e.Route]
	if !ok {
		return st, nil, fmt.Errorf("engine: node %q: %q is not a declared route", e.NodeID, e.Route)
	}
	return enter(g, st, target)
}

func applyJevDecided(g *graph.Graph, st State, e JevDecided) (State, []Effect, error) {
	if err := requireCurrent(st, e.NodeID); err != nil {
		return st, nil, err
	}
	n, ok := g.Node(e.NodeID)
	if !ok {
		return st, nil, fmt.Errorf("engine: internal error: unknown node %q", e.NodeID)
	}
	st.JevAvailable[e.NodeID] = e.Available

	switch n.Kind {
	case spec.KindSelect:
		return applySelectDecision(g, st, n, e.Decision)
	case spec.KindGate:
		return applyGateDecision(g, st, n, e.Decision)
	case spec.KindDecide:
		return applyDecideDecision(g, st, n, e.Decision)
	default:
		return st, nil, fmt.Errorf("engine: node %q (kind %q) does not accept a Jev decision", e.NodeID, n.Kind)
	}
}

// applySelectDecision implements the plan's confidence table for a select
// node: act assigns the chosen agent; gather assigns it but marks the target
// node Supervised (its own attempts budget effectively caps at 1 — a failure
// there reroutes by escalating rather than retrying the same low-confidence
// pick); fallback assigns the declared default, exactly as it would for an
// unavailable or breaker-open Jev, since registry.Decider already reduces
// both to the same "fallback" decision.
func applySelectDecision(g *graph.Graph, st State, n *graph.Node, dec registry.Decision) (State, []Effect, error) {
	var chosen string
	switch dec.Decision {
	case registry.Act, registry.Gather:
		if dec.Chosen == nil {
			return st, nil, fmt.Errorf("engine: node %q: %s decision has no chosen candidate", n.ID, dec.Decision)
		}
		chosen = *dec.Chosen
	case registry.Fallback:
		chosen = n.Default
	default:
		return st, nil, fmt.Errorf("engine: node %q: unrecognized decision %q", n.ID, dec.Decision)
	}
	st.Assigned[n.AssignTo] = chosen
	st.Supervised[n.AssignTo] = dec.Decision == registry.Gather
	return enter(g, st, n.AssignTo)
}

// applyGateDecision turns a noul decision into one of the gate's two routes.
// A fallback decision (too uncertain to trust — which, given
// registry.Policy.Classify uses the noul value itself as its confidence,
// also covers a confidently-false answer, not only an ambiguous one) always
// takes Default, the author's declared safe path. An act or gather decision
// trusts the answer and reads its actual truth value from Confidence (the
// raw noul, preserved by registry.Decider regardless of classification) to
// pick TrueRoute or the other route.
func applyGateDecision(g *graph.Graph, st State, n *graph.Node, dec registry.Decision) (State, []Effect, error) {
	var target string
	switch dec.Decision {
	case registry.Fallback:
		target = n.Default
	case registry.Act, registry.Gather:
		if dec.Confidence >= 0.5 {
			target = n.Routes[n.TrueRoute]
		} else {
			target = otherGateRoute(n)
		}
	default:
		return st, nil, fmt.Errorf("engine: node %q: unrecognized decision %q", n.ID, dec.Decision)
	}
	return enter(g, st, target)
}

func otherGateRoute(n *graph.Node) string {
	for label, tgt := range n.Routes {
		if label != n.TrueRoute {
			return tgt
		}
	}
	return ""
}

// applyDecideDecision routes a choice-typed decide node: act/gather trust
// the chosen label, fallback takes Default.
func applyDecideDecision(g *graph.Graph, st State, n *graph.Node, dec registry.Decision) (State, []Effect, error) {
	var target string
	switch dec.Decision {
	case registry.Act, registry.Gather:
		if dec.Chosen == nil {
			return st, nil, fmt.Errorf("engine: node %q: %s decision has no chosen label", n.ID, dec.Decision)
		}
		t, ok := n.Routes[*dec.Chosen]
		if !ok {
			return st, nil, fmt.Errorf("engine: node %q: chosen label %q is not a declared route", n.ID, *dec.Chosen)
		}
		target = t
	case registry.Fallback:
		target = n.Default
	default:
		return st, nil, fmt.Errorf("engine: node %q: unrecognized decision %q", n.ID, dec.Decision)
	}
	return enter(g, st, target)
}
