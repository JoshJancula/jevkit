package adaptive

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path"
	"strings"
)

// Artifact names persisted for a successful planned handoff.
const (
	ArtifactPlan     = "plan.md"
	ArtifactChecks   = "checks.json"
	ArtifactSubtasks = "subtasks.json"
)

// Bounds keep planner-proposed fan-out finite and reviewable.
const (
	MaxPlanChecks            = 32
	MaxPlanSubtasks          = 12
	MaxOwnedPathsPerSubtask  = 32
	MaxCheckArgvLen          = 32
	MaxAcceptanceCriteriaLen = 32
	MaxNextStepsLen          = 32
)

// Check is a machine-readable verification gate. Exactly one of Argv or Manual
// must be set. Commands are never invented or executed during planning.
type Check struct {
	ID             string   `json:"id"`
	Description    string   `json:"description,omitempty"`
	Argv           []string `json:"argv,omitempty"`
	WorkingDir     string   `json:"workingDir,omitempty"`
	TimeoutSeconds int      `json:"timeoutSeconds,omitempty"`
	Manual         string   `json:"manual,omitempty"`
}

// Subtask is one scoped unit in an optional planner fan-out graph.
type Subtask struct {
	ID                 string   `json:"id"`
	Objective          string   `json:"objective"`
	DependsOn          []string `json:"dependsOn,omitempty"`
	ExpectedOutput     string   `json:"expectedOutput"`
	OwnedPaths         []string `json:"ownedPaths"`
	MergeOrder         int      `json:"mergeOrder"`
	AcceptanceCriteria []string `json:"acceptanceCriteria"`
}

// SubtaskGraph is an optional bounded dependency graph. Mode is "fan-out" when
// the validated graph is kept, or "single" when Jevkit falls back to one
// implementer (including when the planner omitted subtasks).
type SubtaskGraph struct {
	Mode                 string    `json:"mode"`
	FallbackReason       string    `json:"fallbackReason,omitempty"`
	IndependenceReason   string    `json:"independenceReason,omitempty"`
	IntegrationOwner     string    `json:"integrationOwner,omitempty"`
	SharedPaths          []string  `json:"sharedPaths,omitempty"`
	LatencyBenefit       string    `json:"latencyBenefit,omitempty"`
	Parallelize          bool      `json:"parallelize,omitempty"`
	EffectiveConcurrency int       `json:"effectiveConcurrency,omitempty"`
	Subtasks             []Subtask `json:"subtasks,omitempty"`
}

// ChecksFile is the on-disk checks.json envelope.
type ChecksFile struct {
	Checks []Check `json:"checks"`
}

// SingleImplementerGraph is the persisted form when fan-out is not used.
func SingleImplementerGraph(reason string) SubtaskGraph {
	if reason == "" {
		reason = "one implementer preferred"
	}
	return SubtaskGraph{Mode: "single", FallbackReason: reason}
}

// ValidatePlannedHandoff requires enough structured information for
// implementation and assessment. It does not invent checks or parse prose.
func ValidatePlannedHandoff(content string, nextSteps, acceptance []string, checks []Check) error {
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("adaptive: planned requires non-empty plan content")
	}
	if err := requireNonEmptyList("nextSteps", nextSteps, MaxNextStepsLen); err != nil {
		return err
	}
	if err := requireNonEmptyList("acceptanceCriteria", acceptance, MaxAcceptanceCriteriaLen); err != nil {
		return err
	}
	return ValidateChecks(checks)
}

// ValidateChecks validates structured checks only. Empty is allowed.
func ValidateChecks(checks []Check) error {
	if len(checks) > MaxPlanChecks {
		return fmt.Errorf("adaptive: at most %d checks allowed", MaxPlanChecks)
	}
	seen := map[string]bool{}
	for i, c := range checks {
		id := strings.TrimSpace(c.ID)
		if id == "" {
			return fmt.Errorf("adaptive: check %d missing id", i)
		}
		if seen[id] {
			return fmt.Errorf("adaptive: duplicate check id %q", id)
		}
		seen[id] = true
		hasArgv := len(c.Argv) > 0
		manual := strings.TrimSpace(c.Manual)
		if hasArgv == (manual != "") {
			return fmt.Errorf("adaptive: check %q must set exactly one of argv or manual", id)
		}
		if hasArgv {
			if len(c.Argv) > MaxCheckArgvLen {
				return fmt.Errorf("adaptive: check %q argv exceeds %d entries", id, MaxCheckArgvLen)
			}
			for _, arg := range c.Argv {
				if strings.TrimSpace(arg) == "" {
					return fmt.Errorf("adaptive: check %q argv contains an empty entry", id)
				}
			}
			if c.TimeoutSeconds < 0 {
				return fmt.Errorf("adaptive: check %q timeoutSeconds must be nonnegative", id)
			}
			if wd := strings.TrimSpace(c.WorkingDir); wd != "" {
				if path.IsAbs(wd) || strings.Contains(wd, "..") {
					return fmt.Errorf("adaptive: check %q workingDir must be a relative path without ..", id)
				}
			}
		}
	}
	return nil
}

// NormalizeSubtaskGraph validates an optional graph. Invalid or non-beneficial
// fan-out falls back to one implementer; a nil/empty proposal is single mode.
// Call BoundGraphConcurrency with enrolled policy and run budgets before
// persisting under --auto or when displaying the effective concurrency limit.
func NormalizeSubtaskGraph(g *SubtaskGraph) SubtaskGraph {
	if g == nil || len(g.Subtasks) == 0 {
		out := SingleImplementerGraph("no subtasks proposed")
		out.EffectiveConcurrency = 1
		return out
	}
	if reason := rejectSubtaskGraph(g); reason != "" {
		out := SingleImplementerGraph(reason)
		out.EffectiveConcurrency = 1
		return out
	}
	out := *g
	out.Mode = "fan-out"
	out.FallbackReason = ""
	return out
}

// BoundGraphConcurrency caps an otherwise eligible fan-out graph to the
// enrolled policy and remaining run budgets. EffectiveConcurrency is always set.
func BoundGraphConcurrency(g SubtaskGraph, maxConcurrent, remainingAssignments int) SubtaskGraph {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	limit := maxConcurrent
	if remainingAssignments > 0 && remainingAssignments < limit {
		limit = remainingAssignments
	}
	if g.Mode != "fan-out" || len(g.Subtasks) == 0 {
		g.Mode = "single"
		g.EffectiveConcurrency = 1
		g.Parallelize = false
		return g
	}
	ready := 0
	for _, st := range g.Subtasks {
		if len(st.DependsOn) == 0 {
			ready++
		}
	}
	if !g.Parallelize || ready < 2 || limit < 2 {
		g.EffectiveConcurrency = 1
		g.Parallelize = false
		return g
	}
	eff := ready
	if limit < eff {
		eff = limit
	}
	g.EffectiveConcurrency = eff
	return g
}

// DigestHex returns the lowercase hex SHA-256 of data.
func DigestHex(data []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

// HasArgvChecks reports whether any check would execute a command.
func HasArgvChecks(checks []Check) bool {
	for _, c := range checks {
		if len(c.Argv) > 0 {
			return true
		}
	}
	return false
}

func rejectSubtaskGraph(g *SubtaskGraph) string {
	if len(g.Subtasks) < 2 {
		return "prefer one implementer for small work"
	}
	if len(g.Subtasks) > MaxPlanSubtasks {
		return "unbounded subtask count"
	}
	if strings.TrimSpace(g.IndependenceReason) == "" {
		return "missing independence reason"
	}
	ids := map[string]int{}
	mergeOrders := map[int]string{}
	owned := map[string]string{}
	for i, st := range g.Subtasks {
		id := strings.TrimSpace(st.ID)
		if id == "" {
			return "subtask missing stable id"
		}
		if _, dup := ids[id]; dup {
			return "duplicate subtask id"
		}
		ids[id] = i
		if strings.TrimSpace(st.Objective) == "" || strings.TrimSpace(st.ExpectedOutput) == "" {
			return "subtask missing objective or expected output"
		}
		if err := requireNonEmptyList("subtask acceptanceCriteria", st.AcceptanceCriteria, MaxAcceptanceCriteriaLen); err != nil {
			return "subtask missing acceptance criteria"
		}
		if len(st.OwnedPaths) == 0 || len(st.OwnedPaths) > MaxOwnedPathsPerSubtask {
			return "unbounded or empty owned paths"
		}
		for _, p := range st.OwnedPaths {
			p = strings.TrimSpace(p)
			if p == "" || p == "." || p == "/" || p == "*" || p == "**" || strings.Contains(p, "..") {
				return "unbounded owned path scope"
			}
			if other, ok := owned[p]; ok && other != id {
				return "ambiguous ownership"
			}
			for prev, owner := range owned {
				if owner != id && pathsOverlap(prev, p) {
					return "ambiguous ownership"
				}
			}
			owned[p] = id
		}
		if prev, ok := mergeOrders[st.MergeOrder]; ok && prev != id {
			return "ambiguous merge order"
		}
		mergeOrders[st.MergeOrder] = id
	}
	for _, st := range g.Subtasks {
		for _, dep := range st.DependsOn {
			dep = strings.TrimSpace(dep)
			if dep == "" {
				return "missing dependencies"
			}
			if _, ok := ids[dep]; !ok {
				return "missing dependencies"
			}
			if dep == st.ID {
				return "cycles"
			}
		}
	}
	if hasCycle(g.Subtasks) {
		return "cycles"
	}
	shared := nonEmptyTrimmed(g.SharedPaths)
	needsIntegration := len(shared) > 0 || hasAnyDeps(g.Subtasks)
	if needsIntegration && strings.TrimSpace(g.IntegrationOwner) == "" {
		return "no usable integration path"
	}
	ready := 0
	for _, st := range g.Subtasks {
		if len(st.DependsOn) == 0 {
			ready++
		}
	}
	if g.Parallelize {
		if ready < 2 {
			return "parallelize needs at least two ready subtasks"
		}
		if strings.TrimSpace(g.LatencyBenefit) == "" {
			return "parallelize needs a plausible latency benefit"
		}
	}
	return ""
}

func hasAnyDeps(subtasks []Subtask) bool {
	for _, st := range subtasks {
		if len(st.DependsOn) > 0 {
			return true
		}
	}
	return false
}

func hasCycle(subtasks []Subtask) bool {
	deps := map[string][]string{}
	for _, st := range subtasks {
		deps[st.ID] = append([]string(nil), st.DependsOn...)
	}
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[string]int{}
	var visit func(string) bool
	visit = func(id string) bool {
		color[id] = gray
		for _, dep := range deps[id] {
			switch color[dep] {
			case gray:
				return true
			case white:
				if visit(dep) {
					return true
				}
			}
		}
		color[id] = black
		return false
	}
	for id := range deps {
		if color[id] == white && visit(id) {
			return true
		}
	}
	return false
}

func pathsOverlap(a, b string) bool {
	a, b = path.Clean("/"+strings.TrimPrefix(a, "/")), path.Clean("/"+strings.TrimPrefix(b, "/"))
	if a == b {
		return true
	}
	return strings.HasPrefix(a+"/", b+"/") || strings.HasPrefix(b+"/", a+"/")
}

func requireNonEmptyList(name string, items []string, max int) error {
	trimmed := nonEmptyTrimmed(items)
	if len(trimmed) == 0 {
		return fmt.Errorf("adaptive: %s must be a non-empty list", name)
	}
	if len(trimmed) > max {
		return fmt.Errorf("adaptive: %s exceeds %d entries", name, max)
	}
	return nil
}

func nonEmptyTrimmed(items []string) []string {
	out := make([]string, 0, len(items))
	for _, s := range items {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// MarshalChecks returns checks.json bytes.
func MarshalChecks(checks []Check) ([]byte, error) {
	if checks == nil {
		checks = []Check{}
	}
	return json.MarshalIndent(ChecksFile{Checks: checks}, "", "  ")
}

// MarshalSubtasks returns subtasks.json bytes.
func MarshalSubtasks(g SubtaskGraph) ([]byte, error) {
	if g.Mode == "" {
		g.Mode = "single"
	}
	if g.Subtasks == nil {
		g.Subtasks = []Subtask{}
	}
	return json.MarshalIndent(g, "", "  ")
}
