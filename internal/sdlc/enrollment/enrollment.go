// Package enrollment separates agent discovery from permission to route work.
// A project policy and a user's explicit roster must both allow an assignment.
package enrollment

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	HostSelf = "host-self"
	Native   = "native"
	Runtime  = "runtime"
)

var profiles = map[string]int{"lean": 1, "collaborative": 2, "assured": 3}

type RolePolicy struct {
	Via         []string `yaml:"via"`
	Runtimes    []string `yaml:"runtimes"`
	WriteScopes []string `yaml:"writeScopes"`
	Write       bool     `yaml:"write"`
	ReadOnly    bool     `yaml:"readOnly"`
	Isolated    bool     `yaml:"isolated"`
}

type Policy struct {
	Version                   int                   `yaml:"version"`
	MinimumProfile            string                `yaml:"minimumProfile"`
	MaxConcurrent             int                   `yaml:"maxConcurrent"`
	MaxAssignments            int                   `yaml:"maxAssignments"`
	MaxRevisions              int                   `yaml:"maxRevisions"`
	MaxInvocationSeconds      int                   `yaml:"maxInvocationSeconds"`
	MaxRunSeconds             int                   `yaml:"maxRunSeconds"`
	MaxEstimatedCostUSD       float64               `yaml:"maxEstimatedCostUsd"`
	AdaptiveBuiltinDelegation string                `yaml:"adaptiveBuiltinDelegation"`
	SpecialistMode            string                `yaml:"specialistMode"`
	SessionStrategy           string                `yaml:"sessionStrategy"`
	Quorums                   map[string]int        `yaml:"quorums"`
	Roles                     map[string]RolePolicy `yaml:"roles"`
}

type Agent struct {
	ID           string            `yaml:"id" json:"id"`
	Disabled     bool              `yaml:"disabled,omitempty" json:"disabled,omitempty"`
	Roles        []string          `yaml:"roles" json:"roles"`
	Rubric       string            `yaml:"rubric" json:"rubric"`
	RoleRubrics  map[string]string `yaml:"roleRubrics,omitempty" json:"roleRubrics,omitempty"`
	Via          string            `yaml:"via" json:"via"`
	Subagent     string            `yaml:"subagent,omitempty" json:"subagent,omitempty"`
	Runtime      string            `yaml:"runtime,omitempty" json:"runtime,omitempty"`
	Model        string            `yaml:"model,omitempty" json:"model,omitempty"`
	RuntimeAgent string            `yaml:"agent,omitempty" json:"agent,omitempty"`
	Binary       string            `yaml:"binary,omitempty" json:"binary,omitempty"`
	WriteScopes  []string          `yaml:"writeScopes,omitempty" json:"writeScopes,omitempty"`
	ReadOnly     bool              `yaml:"readOnly,omitempty" json:"readOnly,omitempty"`
	Isolated     bool              `yaml:"isolated,omitempty" json:"isolated,omitempty"`
}

// Ready reports whether a roster entry may be offered for work. Starter
// entries require an explicit opt-in and a real model binding.
func (a Agent) Ready() bool {
	return !a.Disabled && (a.Via != Runtime || (a.Model != "YOUR_MODEL" && a.Model != "MODEL"))
}

type Roster struct {
	Version int     `yaml:"version"`
	Agents  []Agent `yaml:"agents"`
}

// Reach is supplied by the current driver. Filesystem discovery never sets
// Native or Self: only a host integration can declare those capabilities.
type Reach struct {
	Driver   string
	Self     bool
	Native   map[string]HostCapability
	Runtimes map[string]RuntimeCapability
	Binaries map[string]RuntimeCapability
}

type HostCapability struct{ ReadOnly, Isolated, Write, Scopes bool }
type RuntimeCapability struct{ ReadOnly, Isolated, Write, Scopes bool }

type Requirement struct {
	Role     string
	Write    bool
	ReadOnly bool
	Isolated bool
	Scopes   []string
	Excluded map[string]bool
}

type Candidate struct {
	Agent   Agent
	Binding string
}

// DefaultPolicy is deliberately permissive about agent type, but grants no
// enrollment. Projects can narrow it in .jevkit/sdlc/policy.yaml.
func DefaultPolicy() Policy {
	return Policy{Version: 1, MinimumProfile: "lean", MaxConcurrent: 3, MaxAssignments: 20, MaxRevisions: 3,
		AdaptiveBuiltinDelegation: "off",
		SpecialistMode:            "off",
		SessionStrategy:           "auto",
		MaxInvocationSeconds:      1800, MaxRunSeconds: 21600,
		Roles: map[string]RolePolicy{
			"planner":     {Via: []string{HostSelf, Native, Runtime}, Write: true},
			"implementer": {Via: []string{HostSelf, Native, Runtime}, Write: true},
			"assessor":    {Via: []string{HostSelf, Native, Runtime}, Write: true},
			"research":    {Via: []string{Native, Runtime}, ReadOnly: true},
			"qa":          {Via: []string{Native, Runtime}, ReadOnly: true},
			"security":    {Via: []string{Native, Runtime}, ReadOnly: true},
			"code-review": {Via: []string{Native, Runtime}, ReadOnly: true},
		},
	}
}

func LoadPolicy(path string) (Policy, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return DefaultPolicy(), nil
	}
	if err != nil {
		return Policy{}, fmt.Errorf("read project policy %s: %w", path, err)
	}
	p := DefaultPolicy()
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil {
		return Policy{}, fmt.Errorf("parse project policy %s: %w", path, err)
	}
	if err := p.Validate(); err != nil {
		return Policy{}, fmt.Errorf("project policy %s: %w", path, err)
	}
	return p, nil
}

func (p Policy) Validate() error {
	if p.SessionStrategy != "" && p.SessionStrategy != "auto" && p.SessionStrategy != "fresh" && p.SessionStrategy != "resume" && p.SessionStrategy != "compact" {
		return fmt.Errorf("sdlc: sessionStrategy must be auto, fresh, resume or compact")
	}
	if p.SpecialistMode != "off" && p.SpecialistMode != "advisory" && p.SpecialistMode != "required" {
		return fmt.Errorf("specialistMode must be off, advisory or required")
	}
	if p.AdaptiveBuiltinDelegation != "off" && p.AdaptiveBuiltinDelegation != "opt-in" && p.AdaptiveBuiltinDelegation != "on" {
		return fmt.Errorf("adaptiveBuiltinDelegation must be off, opt-in or on")
	}
	if p.Version != 1 {
		return fmt.Errorf("version must be 1")
	}
	if _, ok := profiles[p.MinimumProfile]; !ok {
		return fmt.Errorf("minimumProfile must be lean, collaborative or assured")
	}
	if p.MaxConcurrent < 1 {
		return fmt.Errorf("maxConcurrent must be positive")
	}
	if p.MaxAssignments < 1 || p.MaxRevisions < 1 || p.MaxInvocationSeconds < 1 || p.MaxRunSeconds < 1 || p.MaxEstimatedCostUSD < 0 {
		return fmt.Errorf("maxAssignments, maxRevisions, maxInvocationSeconds and maxRunSeconds must be positive; maxEstimatedCostUsd must be nonnegative")
	}
	for profile, q := range p.Quorums {
		if _, ok := profiles[profile]; !ok || q < 1 {
			return fmt.Errorf("invalid quorum for %q", profile)
		}
	}
	for role, rule := range p.Roles {
		if role == "" {
			return fmt.Errorf("role must not be empty")
		}
		for _, via := range rule.Via {
			if via != HostSelf && via != Native && via != Runtime {
				return fmt.Errorf("role %q has invalid via %q", role, via)
			}
		}
	}
	return nil
}

func LoadRoster(path string) (Roster, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Roster{Version: 1}, nil
	}
	if err != nil {
		return Roster{}, fmt.Errorf("read user roster %s: %w", path, err)
	}
	var r Roster
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&r); err != nil {
		return Roster{}, fmt.Errorf("parse user roster %s: %w", path, err)
	}
	if err := r.Validate(); err != nil {
		return Roster{}, fmt.Errorf("user roster %s: %w", path, err)
	}
	return r, nil
}

func (r Roster) Validate() error {
	if r.Version != 1 {
		return fmt.Errorf("version must be 1")
	}
	seen := map[string]bool{}
	for _, a := range r.Agents {
		if a.ID == "" || seen[a.ID] {
			return fmt.Errorf("agent IDs must be nonempty and unique: %q", a.ID)
		}
		seen[a.ID] = true
		if len(a.Roles) == 0 || strings.TrimSpace(a.Rubric) == "" {
			return fmt.Errorf("agent %q needs roles and rubric", a.ID)
		}
		for role, rubric := range a.RoleRubrics {
			if !contains(a.Roles, role) || strings.TrimSpace(rubric) == "" {
				return fmt.Errorf("agent %q has an invalid role rubric for %q", a.ID, role)
			}
		}
		switch a.Via {
		case HostSelf:
			if a.Subagent != "" || a.Runtime != "" || a.Model != "" || a.RuntimeAgent != "" || a.Binary != "" || a.ReadOnly || a.Isolated || len(a.WriteScopes) > 0 {
				return fmt.Errorf("agent %q: host-self has no subagent or runtime binding", a.ID)
			}
		case Native:
			if a.Subagent == "" || a.Runtime != "" || a.Model != "" || a.RuntimeAgent != "" || a.Binary != "" {
				return fmt.Errorf("agent %q: native requires only subagent", a.ID)
			}
		case Runtime:
			if a.Runtime == "" || a.Model == "" || a.Subagent != "" {
				return fmt.Errorf("agent %q: runtime requires runtime and model", a.ID)
			}
		default:
			return fmt.Errorf("agent %q: invalid via %q", a.ID, a.Via)
		}
	}
	return nil
}

func (p Policy) Quorum(profile string) (int, error) {
	rank, ok := profiles[profile]
	if !ok {
		return 0, fmt.Errorf("unknown policy profile %q", profile)
	}
	if rank < profiles[p.MinimumProfile] {
		return 0, fmt.Errorf("policy profile %q is below project minimum %q", profile, p.MinimumProfile)
	}
	if n := p.Quorums[profile]; n > 0 {
		return n, nil
	}
	return rank, nil
}

// Eligible applies every boundary at assignment time. The returned IDs are
// the only IDs a caller may offer Jev for this particular action.
func Eligible(p Policy, roster Roster, reach Reach, req Requirement) []Candidate {
	rule, allowedRole := p.Roles[req.Role]
	if !allowedRole {
		return nil
	}
	req.ReadOnly = req.ReadOnly || rule.ReadOnly || !rule.Write
	req.Isolated = req.Isolated || rule.Isolated
	var out []Candidate
	for _, a := range roster.Agents {
		if !a.Ready() || req.Excluded[a.ID] || !contains(a.Roles, req.Role) || !contains(rule.Via, a.Via) {
			continue
		}
		if len(rule.Runtimes) > 0 && a.Via == Runtime && !contains(rule.Runtimes, a.Runtime) {
			continue
		}
		if req.Write && !rule.Write {
			continue
		}
		if req.Write && a.ReadOnly {
			continue
		}
		if req.ReadOnly && !a.ReadOnly {
			continue
		}
		if req.Isolated && !a.Isolated {
			continue
		}
		if !scopesAllowed(req.Scopes, a.WriteScopes) || !scopesAllowed(req.Scopes, rule.WriteScopes) {
			continue
		}
		binding := ""
		switch a.Via {
		case HostSelf:
			if reach.Driver != "host" || !reach.Self || a.ReadOnly || a.Isolated || req.ReadOnly || req.Isolated || len(a.WriteScopes) > 0 || req.Write && len(rule.WriteScopes) > 0 {
				continue
			}
			binding = "host-self"
		case Native:
			cap, ok := reach.Native[a.Subagent]
			if reach.Driver != "host" || !ok || (req.ReadOnly || a.ReadOnly) && !cap.ReadOnly || (req.Isolated || a.Isolated) && !cap.Isolated || req.Write && !cap.Write || (len(a.WriteScopes) > 0 || req.Write && len(rule.WriteScopes) > 0) && !cap.Scopes {
				continue
			}
			binding = "native:" + a.Subagent
		case Runtime:
			cap, ok := reach.Runtimes[a.Runtime]
			if a.Binary != "" {
				cap, ok = reach.Binaries[a.Binary]
			}
			if !ok || (req.ReadOnly || a.ReadOnly) && !cap.ReadOnly || (req.Isolated || a.Isolated) && !cap.Isolated || req.Write && !cap.Write || (len(a.WriteScopes) > 0 || req.Write && len(rule.WriteScopes) > 0) && !cap.Scopes {
				continue
			}
			binding = "runtime:" + a.Runtime + ":" + a.Model + ":" + a.RuntimeAgent + ":" + a.Binary
		}
		out = append(out, Candidate{Agent: a, Binding: binding})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Agent.ID < out[j].Agent.ID })
	return out
}

func DistinctBindings(candidates []Candidate) int {
	seen := map[string]bool{}
	for _, c := range candidates {
		seen[c.Binding] = true
	}
	return len(seen)
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// An empty scope list means no additional restriction. Glob patterns are
// intentionally not interpreted here: require an exact declared scope or **.
func scopesAllowed(wanted, allowed []string) bool {
	if len(wanted) == 0 || len(allowed) == 0 {
		return true
	}
	for _, scope := range wanted {
		if !contains(allowed, scope) && !contains(allowed, "**") {
			return false
		}
	}
	return true
}

func Rubrics(candidates []Candidate, role ...string) map[string]string {
	out := map[string]string{}
	for _, c := range candidates {
		rubric := c.Agent.Rubric
		if len(role) > 0 && c.Agent.RoleRubrics[role[0]] != "" {
			rubric = c.Agent.RoleRubrics[role[0]]
		}
		out[c.Agent.ID] = rubric
	}
	return out
}
