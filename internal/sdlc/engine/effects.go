package engine

import "github.com/OWNER/jevkit/internal/sdlc/spec"

// Effect is something Apply wants its caller to resolve and feed back as an
// Event. The engine never resolves an Effect itself; it only ever produces
// the request. Effect is a sealed set: RunWork, RunCommand, AskJev,
// WaitForHuman and RunFinished.
type Effect interface{ effect() }

// Assignment names which agent should carry out a RunWork effect.
type Assignment struct {
	// Self is true when the node pins agent: self: the orchestrator does the
	// work inline, no delegation.
	Self bool
	// AgentID is the catalog id Jev (or a declared default) chose, when Self
	// is false.
	AgentID string
}

// RunWork asks the caller to have Assignment execute Objective and produce
// Produces from Consumes. Attempt is 1-based and increases only when a prior
// attempt's execution itself failed (WorkCompleted.ExecutionFailed) and
// budgets.maxNodeAttempts allows another try.
type RunWork struct {
	NodeID     string
	Attempt    int
	Assignment Assignment
	Objective  string
	Consumes   []spec.Artifact
	Produces   []spec.Artifact
}

func (RunWork) effect() {}

// RunCommand asks the caller to run Command and report back which of the
// node's declared route labels the exit status corresponds to.
type RunCommand struct {
	NodeID  string
	Attempt int
	Command string
}

func (RunCommand) effect() {}

// AskJevKind distinguishes the three Jev-driven node kinds; Apply uses it
// only to decide how to interpret the returned Decision, never to call Jev
// itself.
type AskJevKind string

const (
	AskJevSelect AskJevKind = "select"
	AskJevGate   AskJevKind = "gate"
	AskJevDecide AskJevKind = "decide"
)

// AskJev asks the caller to classify against QuestionSet and report a
// registry.Decision back as JevDecided. Candidates is populated only for a
// select node (the call-time criteria a caller assembles from the catalog);
// State names which run inputs/artifacts to bound and pass as Jev state.
type AskJev struct {
	NodeID      string
	Kind        AskJevKind
	QuestionSet string
	Candidates  []string
	State       *spec.StateSpec
}

func (AskJev) effect() {}

// WaitForHuman asks the caller to present Prompt and collect one of Routes.
type WaitForHuman struct {
	NodeID string
	Prompt string
	Routes []string
}

func (WaitForHuman) effect() {}

// RunFinished reports that the run has stopped: Outcome is a terminal node's
// declared outcome on success, or the engine's own failure reason when
// Status is StatusFailed. It is the only Effect a caller need not resolve
// with an Event — there is nothing left to drive.
type RunFinished struct {
	Status  string
	Outcome string
}

func (RunFinished) effect() {}
