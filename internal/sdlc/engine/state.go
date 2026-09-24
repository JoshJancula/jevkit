// Package engine is the pure fold at the center of jevkit SDLC execution:
// Apply(graph, state, event) -> (state, []Effect). It performs no I/O, holds
// no clock, and never calls Jev, runs a command, or spawns an agent itself —
// those are Effects a client (the MCP conductor tools, or jevkit's own
// opt-in driver) resolves and feeds back as Events. This is what makes
// jevkit's "decision support, not autonomous execution" posture structural
// rather than a convention someone has to remember: the engine cannot
// execute anything even if it wanted to.
//
// Because internal/sdlc/compile already unrolls every bounded reroute loop
// into an acyclic chain terminated by a synthesized terminal node, the
// engine never needs its own reroute counter: reaching that terminal node
// *is* reroute exhaustion. The one budget the engine does track per node is
// budgets.maxNodeAttempts, which bounds retrying a single node's own failed
// execution (a crashed or timed-out invocation) — a runtime concern with no
// graph edge of its own, deliberately kept out of the compiled graph (see
// internal/sdlc/compile's package doc).
package engine

// Run statuses.
const (
	StatusRunning  = "running"
	StatusTerminal = "terminal"
	StatusFailed   = "failed"
)

// State is the run's entire persisted state: flat and JSON-serializable, so
// a ledger can store it and a resume can rebuild it by replaying events
// through Apply, never by re-deriving anything Apply didn't already return.
type State struct {
	// Current is the node awaiting an effect's resolution. Empty only before
	// NewRun, and once Status leaves StatusRunning.
	Current string `json:"current"`
	Status  string `json:"status"`
	// Outcome is set once Status != StatusRunning: a terminal node's declared
	// outcome, or an engine-detected failure reason (budget-exceeded:...,
	// node-attempts-exhausted:...).
	Outcome string `json:"outcome,omitempty"`

	// Attempts counts executions of each node's own effect, keyed by node
	// id; it exists only to bound retries of a single failed invocation
	// (budgets.maxNodeAttempts), never to model routing.
	Attempts map[string]int `json:"attempts,omitempty"`
	// Assigned records, for each work node reached via a select's assignTo,
	// the agent id Jev (or a fallback) chose.
	Assigned map[string]string `json:"assigned,omitempty"`
	// Supervised marks a work node whose assignment came from a "gather"
	// (medium-confidence) select decision: its own attempts budget is
	// effectively 1 — a supervised pick that fails does not get retried with
	// the same low-confidence agent, it fails straight to escalation.
	Supervised map[string]bool `json:"supervised,omitempty"`
	// JevAvailable records, for each select/gate/decide node, whether Jev
	// was available for its decision (false means a declared default/
	// fallback was used because Jev could not be reached) — audit only.
	JevAvailable map[string]bool `json:"jevAvailable,omitempty"`
	// Satisfied lists artifact paths already produced or seeded.
	Satisfied map[string]bool `json:"satisfied,omitempty"`

	// ActiveSeconds and EstimatedCostUsd are cumulative totals the caller
	// tracks and reports on every event (the engine has no clock and no
	// cost data of its own); they are compared against the graph's budgets
	// on every Apply call.
	ActiveSeconds    int     `json:"activeSeconds"`
	EstimatedCostUsd float64 `json:"estimatedCostUsd"`

	Warnings []string `json:"warnings,omitempty"`
}

func newState() State {
	return State{
		Attempts:     map[string]int{},
		Assigned:     map[string]string{},
		Supervised:   map[string]bool{},
		JevAvailable: map[string]bool{},
		Satisfied:    map[string]bool{},
		Status:       StatusRunning,
	}
}

// clone deep-copies st so Apply never mutates the caller's State in place;
// the engine is a pure fold and its inputs are immutable from its own
// point of view.
func (st State) clone() State {
	c := st
	c.Attempts = copyIntMap(st.Attempts)
	c.Assigned = copyStringMap(st.Assigned)
	c.Supervised = copyBoolMap(st.Supervised)
	c.JevAvailable = copyBoolMap(st.JevAvailable)
	c.Satisfied = copyBoolMap(st.Satisfied)
	c.Warnings = append([]string{}, st.Warnings...)
	return c
}

func copyIntMap(m map[string]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func copyStringMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func copyBoolMap(m map[string]bool) map[string]bool {
	out := make(map[string]bool, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
