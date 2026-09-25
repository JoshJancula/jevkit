package engine

import "github.com/OWNER/jevkit/internal/registry"

// Event feeds an Effect's resolution back into Apply. It is a sealed set:
// WorkCompleted, CommandCompleted, JevDecided and HumanResponded.
//
// Every Event carries a Progress: the caller's own cumulative tally of
// elapsed run time and estimated Jev/agent spend. The engine has no clock and
// no cost data of its own, so it trusts these totals and compares them
// against the graph's budgets on every Apply call — the caller decides how
// to measure both; the engine only ever enforces the ceiling.
type Event interface {
	event() bool
	progress() Progress
}

// Progress is the caller-tracked cumulative spend reported on every Event.
type Progress struct {
	ActiveSeconds    int
	EstimatedCostUsd float64
}

// WorkCompleted resolves a RunWork effect. ExecutionFailed marks the
// invocation itself failing to run at all (crash, timeout, transport error)
// — the only condition budgets.maxNodeAttempts retries; it is never set for
// an agent that ran but produced nothing useful, which ProducedPaths already
// captures structurally (a missing required artifact fails the node without
// consuming an attempt, since nothing about retrying the identical objective
// would fix a spec the agent already tried once to satisfy — that failure is
// for a downstream gate/decide node to route on, per the plan's own "gates
// matter more than the selector" design).
type WorkCompleted struct {
	NodeID          string
	ProducedPaths   []string
	ExecutionFailed bool
	// CostReported is false when the agent's usage could not be itemized
	// (a native subagent's cost is opaque and parent-owned, per the plan's
	// Cost section). budgets.missingUsage governs what happens then.
	CostReported bool
	P            Progress
}

func (WorkCompleted) event() bool          { return true }
func (e WorkCompleted) progress() Progress { return e.P }

// CommandCompleted resolves a RunCommand effect: Route names which of the
// check node's declared routes the exit status maps to. ExecutionFailed
// marks the command itself failing to run (not found, killed) rather than
// running and exiting non-zero — that distinction is what
// budgets.maxNodeAttempts retries.
type CommandCompleted struct {
	NodeID          string
	Route           string
	ExecutionFailed bool
	P               Progress
}

func (CommandCompleted) event() bool          { return true }
func (e CommandCompleted) progress() Progress { return e.P }

// JevDecided resolves an AskJev effect. Available is false when Jev could
// not be reached and Decision is a synthesized fallback to the node's
// declared default — "a run never stalls on the classifier".
type JevDecided struct {
	NodeID    string
	Decision  registry.Decision
	Available bool
	P         Progress
}

func (JevDecided) event() bool          { return true }
func (e JevDecided) progress() Progress { return e.P }

// HumanResponded resolves a WaitForHuman effect with the chosen route.
type HumanResponded struct {
	NodeID string
	Route  string
	P      Progress
}

func (HumanResponded) event() bool          { return true }
func (e HumanResponded) progress() Progress { return e.P }
