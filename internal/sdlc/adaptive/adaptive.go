// Package adaptive is a pure next-action SDLC loop. A driver resolves each
// assignment with an enrolled worker and feeds a structured result back.
package adaptive

import (
	"fmt"
	"sort"
)

const (
	Planning     = "planning"
	Implementing = "implementing"
	Assessing    = "assessing"
	Done         = "done"
	Paused       = "paused"
)

type Assignment struct {
	InvocationID       string   `json:"invocationId"`
	StageID            string   `json:"stageId,omitempty"`
	Objective          string   `json:"objective,omitempty"`
	AgentID            string   `json:"agentId"`
	Binding            string   `json:"binding"`
	Via                string   `json:"via,omitempty"`
	Runtime            string   `json:"runtime,omitempty"`
	Role               string   `json:"role"`
	Revision           string   `json:"revision,omitempty"`
	ReadOnly           bool     `json:"readOnly,omitempty"`
	Isolated           bool     `json:"isolated,omitempty"`
	ProjectWriteScopes []string `json:"projectWriteScopes,omitempty"`
	AgentWriteScopes   []string `json:"agentWriteScopes,omitempty"`
}

type Assessment struct {
	InvocationID string `json:"invocationId"`
	AgentID      string `json:"agentId"`
	Binding      string `json:"binding"`
	Revision     string `json:"revision"`
	Approved     bool   `json:"approved"`
}

type State struct {
	TaskKind            string                `json:"taskKind"`
	Profile             string                `json:"profile"`
	Stage               string                `json:"stage"`
	Outcome             string                `json:"outcome,omitempty"`
	PlanRevision        string                `json:"planRevision,omitempty"`
	DiffRevision        string                `json:"diffRevision,omitempty"`
	Quorum              int                   `json:"quorum"`
	MaxConcurrent       int                   `json:"maxConcurrent"`
	MaxAssignments      int                   `json:"maxAssignments,omitempty"`
	AssignmentCount     int                   `json:"assignmentCount,omitempty"`
	MaxEstimatedCostUSD float64               `json:"maxEstimatedCostUsd,omitempty"`
	EstimatedCostUSD    float64               `json:"estimatedCostUsd,omitempty"`
	MaxRevisions        int                   `json:"maxRevisions"`
	RevisionCount       int                   `json:"revisionCount"`
	Assignments         map[string]Assignment `json:"assignments,omitempty"`
	Assessments         []Assessment          `json:"assessments,omitempty"`
	Excluded            map[string]bool       `json:"excluded,omitempty"`
	ExcludedBindings    map[string]bool       `json:"excludedBindings,omitempty"`
	ExcludedRuntimes    map[string]bool       `json:"excludedRuntimes,omitempty"`
}

// Result is a worker's structured outcome. A revision is a content digest of
// the plan or diff, supplied by the driver after it writes the artifact.
type Result struct {
	InvocationID string  `json:"invocationId"`
	AgentID      string  `json:"agentId"`
	Outcome      string  `json:"outcome"` // planned, changed, answer, no-change, approved, changes-required, auth-failed, failed, timed-out, run-time-exhausted
	Revision     string  `json:"revision,omitempty"`
	CostUSD      float64 `json:"costUsd,omitempty"`
}

func New(taskKind, profile string, quorum, maxConcurrent, maxRevisions int) (State, error) {
	if taskKind == "" || quorum < 1 || maxConcurrent < 1 || maxRevisions < 1 {
		return State{}, fmt.Errorf("adaptive: invalid run settings")
	}
	return State{TaskKind: taskKind, Profile: profile, Stage: Planning, Quorum: quorum,
		MaxConcurrent: maxConcurrent, MaxRevisions: maxRevisions,
		Assignments: map[string]Assignment{}, Excluded: map[string]bool{}}, nil
}

func (s State) Role() string {
	switch s.Stage {
	case Planning:
		return "planner"
	case Implementing:
		return "implementer"
	case Assessing:
		return "assessor"
	}
	return ""
}

func (s State) Pending() []Assignment {
	out := make([]Assignment, 0, len(s.Assignments))
	for _, a := range s.Assignments {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].InvocationID < out[j].InvocationID })
	return out
}

// Assign accepts a driver-routed worker. The caller must check current
// eligibility first; this enforces invocation and assessor independence.
func (s *State) Assign(a Assignment) error {
	if s.BudgetExhausted() {
		return fmt.Errorf("adaptive: assignment budget exhausted")
	}
	if s.Role() == "" || a.Role != s.Role() || a.AgentID == "" || a.Binding == "" || a.InvocationID == "" {
		return fmt.Errorf("adaptive: invalid assignment for stage %s", s.Stage)
	}
	if s.Excluded[a.AgentID] {
		return fmt.Errorf("adaptive: agent %q was removed from this run", a.AgentID)
	}
	if s.ExcludedBindings[a.Binding] {
		return fmt.Errorf("adaptive: binding %q was removed from this run", a.Binding)
	}
	if a.Runtime != "" && s.ExcludedRuntimes[a.Runtime] {
		return fmt.Errorf("adaptive: runtime %q was removed from this run", a.Runtime)
	}
	if _, ok := s.Assignments[a.InvocationID]; ok {
		return fmt.Errorf("adaptive: duplicate invocation %q", a.InvocationID)
	}
	if len(s.Assignments) >= s.MaxConcurrent {
		return fmt.Errorf("adaptive: concurrency limit reached")
	}
	if s.Stage != Assessing && len(s.Assignments) > 0 {
		return fmt.Errorf("adaptive: one %s invocation is already pending", s.Stage)
	}
	if s.Stage == Assessing {
		if a.Revision != s.DiffRevision || a.Revision == "" {
			return fmt.Errorf("adaptive: assessment must target exact diff revision")
		}
		for _, pending := range s.Assignments {
			if pending.Binding == a.Binding || pending.AgentID == a.AgentID {
				return fmt.Errorf("adaptive: assessor already assigned")
			}
		}
		for _, done := range s.Assessments {
			if done.Revision == a.Revision && (done.Binding == a.Binding || done.AgentID == a.AgentID) {
				return fmt.Errorf("adaptive: assessor already counted for this revision")
			}
		}
	} else if a.Revision != "" && a.Revision != s.PlanRevision {
		return fmt.Errorf("adaptive: assignment references stale plan")
	}
	if s.Assignments == nil {
		s.Assignments = map[string]Assignment{}
	}
	s.Assignments[a.InvocationID] = a
	s.AssignmentCount++
	return nil
}

func (s State) BudgetExhausted() bool {
	return s.MaxAssignments > 0 && s.AssignmentCount >= s.MaxAssignments || s.MaxEstimatedCostUSD > 0 && s.EstimatedCostUSD >= s.MaxEstimatedCostUSD
}

// Apply leaves state untouched when a result is invalid.
func (s *State) Apply(r Result) error {
	c := *s
	c.Assignments = make(map[string]Assignment, len(s.Assignments))
	for k, v := range s.Assignments {
		c.Assignments[k] = v
	}
	c.Excluded = make(map[string]bool, len(s.Excluded))
	for k, v := range s.Excluded {
		c.Excluded[k] = v
	}
	c.ExcludedBindings = make(map[string]bool, len(s.ExcludedBindings))
	for k, v := range s.ExcludedBindings {
		c.ExcludedBindings[k] = v
	}
	c.ExcludedRuntimes = make(map[string]bool, len(s.ExcludedRuntimes))
	for k, v := range s.ExcludedRuntimes {
		c.ExcludedRuntimes[k] = v
	}
	c.Assessments = append([]Assessment(nil), s.Assessments...)
	if err := c.apply(r); err != nil {
		return err
	}
	*s = c
	return nil
}

func (s *State) apply(r Result) error {
	if r.CostUSD < 0 {
		return fmt.Errorf("adaptive: cost must be nonnegative")
	}
	a, ok := s.Assignments[r.InvocationID]
	if !ok || a.AgentID != r.AgentID {
		return fmt.Errorf("adaptive: result does not match a pending independent invocation")
	}
	delete(s.Assignments, r.InvocationID)
	s.EstimatedCostUSD += r.CostUSD
	if s.MaxEstimatedCostUSD > 0 && s.EstimatedCostUSD > s.MaxEstimatedCostUSD {
		s.Pause("cost-budget-exhausted")
		return nil
	}
	if r.Outcome == "timed-out" {
		s.Pause("invocation-timeout")
		return nil
	}
	if r.Outcome == "run-time-exhausted" {
		s.Pause("run-time-budget-exhausted")
		return nil
	}
	if r.Outcome == "auth-failed" {
		if s.Excluded == nil {
			s.Excluded = map[string]bool{}
		}
		s.Excluded[a.AgentID] = true
		if s.ExcludedBindings == nil {
			s.ExcludedBindings = map[string]bool{}
		}
		s.ExcludedBindings[a.Binding] = true
		if a.Runtime != "" {
			if s.ExcludedRuntimes == nil {
				s.ExcludedRuntimes = map[string]bool{}
			}
			s.ExcludedRuntimes[a.Runtime] = true
		}
		return nil // driver reroutes, or pauses when Eligible returns no replacement
	}
	switch s.Stage {
	case Planning:
		switch r.Outcome {
		case "answer", "no-change":
			s.Stage, s.Outcome = Done, r.Outcome
		case "planned":
			if r.Revision == "" {
				return fmt.Errorf("adaptive: plan revision required")
			}
			s.PlanRevision, s.Stage = r.Revision, Implementing
		case "failed":
			s.Stage, s.Outcome = Paused, "planner-failed"
		default:
			return fmt.Errorf("adaptive: invalid planning outcome %q", r.Outcome)
		}
	case Implementing:
		switch r.Outcome {
		case "answer", "no-change":
			s.Stage, s.Outcome = Done, r.Outcome
		case "changed":
			if r.Revision == "" || r.Revision == s.DiffRevision {
				return fmt.Errorf("adaptive: new diff revision required")
			}
			s.RevisionCount++
			if s.RevisionCount > s.MaxRevisions {
				s.Stage, s.Outcome = Paused, "revision-budget-exhausted"
				return nil
			}
			s.DiffRevision, s.Stage = r.Revision, Assessing
			s.Assessments = nil
		case "failed":
			s.Stage, s.Outcome = Paused, "implementer-failed"
		default:
			return fmt.Errorf("adaptive: invalid implementation outcome %q", r.Outcome)
		}
	case Assessing:
		if r.Revision != a.Revision {
			return fmt.Errorf("adaptive: assessment revision does not match assignment")
		}
		switch r.Outcome {
		case "approved", "changes-required":
			s.Assessments = append(s.Assessments, Assessment{InvocationID: r.InvocationID, AgentID: r.AgentID, Binding: a.Binding, Revision: r.Revision, Approved: r.Outcome == "approved"})
		case "failed":
			return nil // replacement can be routed
		default:
			return fmt.Errorf("adaptive: invalid assessment outcome %q", r.Outcome)
		}
		if len(s.Assessments) >= s.Quorum {
			approved := true
			for _, x := range s.Assessments {
				approved = approved && x.Approved
			}
			if approved {
				s.Stage, s.Outcome = Done, "approved"
			} else if s.RevisionCount >= s.MaxRevisions {
				s.Pause("revision-budget-exhausted")
			} else {
				s.Stage = Implementing
			}
			s.Assignments = map[string]Assignment{} // cancel excess pending reviews after decision
		}
	default:
		return fmt.Errorf("adaptive: run is %s", s.Stage)
	}
	return nil
}

func (s *State) Pause(reason string) {
	s.Stage, s.Outcome = Paused, reason
	s.Assignments = map[string]Assignment{}
}
