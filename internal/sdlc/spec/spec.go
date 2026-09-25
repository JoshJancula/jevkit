// Package spec loads and validates jevkit SDLC workflow definitions:
// authored YAML at .jevkit/sdlc/*.yaml, declarative only (no shell, no
// templates, no code), following the compaction.yaml precedent
// (internal/compact/policy.go).
package spec

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"sort"

	"gopkg.in/yaml.v3"
)

// Node kinds.
const (
	KindWork     = "work"
	KindGate     = "gate"
	KindSelect   = "select"
	KindCheck    = "check"
	KindDecide   = "decide"
	KindHuman    = "human"
	KindJoin     = "join"
	KindTerminal = "terminal"
)

var validKinds = map[string]bool{
	KindWork: true, KindGate: true, KindSelect: true, KindCheck: true,
	KindDecide: true, KindHuman: true, KindJoin: true, KindTerminal: true,
}

// Default budgets, applied when a workflow omits them.
const (
	DefaultMaxNodeAttempts     = 3
	DefaultMaxReroutes         = 2
	DefaultMaxRunActiveSeconds = 21600
	DefaultMissingUsage        = "warn"
)

// Budgets bound a run. Zero values are filled with the defaults above.
type Budgets struct {
	MaxNodeAttempts     int     `yaml:"maxNodeAttempts"`
	MaxReroutes         int     `yaml:"maxReroutes"`
	MaxRunActiveSeconds int     `yaml:"maxRunActiveSeconds"`
	MaxEstimatedCostUsd float64 `yaml:"maxEstimatedCostUsd"`
	MissingUsage        string  `yaml:"missingUsage"`
}

// unsetMaxReroutes marks a Budgets value decoded without an explicit
// maxReroutes: 0 is a legitimate authored value ("no reroutes allowed") and
// must not be silently overwritten by the default the way the other budget
// fields are, since 0 is invalid for all of them anyway.
const unsetMaxReroutes = -1

// UnmarshalYAML decodes Budgets, distinguishing an omitted maxReroutes from
// an explicit 0.
func (b *Budgets) UnmarshalYAML(value *yaml.Node) error {
	type alias Budgets
	aux := alias{MaxReroutes: unsetMaxReroutes}
	if err := value.Decode(&aux); err != nil {
		return err
	}
	*b = Budgets(aux)
	return nil
}

func (b *Budgets) applyDefaults() {
	if b.MaxNodeAttempts == 0 {
		b.MaxNodeAttempts = DefaultMaxNodeAttempts
	}
	if b.MaxReroutes == unsetMaxReroutes {
		b.MaxReroutes = DefaultMaxReroutes
	}
	if b.MaxRunActiveSeconds == 0 {
		b.MaxRunActiveSeconds = DefaultMaxRunActiveSeconds
	}
	if b.MissingUsage == "" {
		b.MissingUsage = DefaultMissingUsage
	}
}

// Artifact is one node input/output declaration.
type Artifact struct {
	Path     string `yaml:"path"`
	Required bool   `yaml:"required"`
	Seedable bool   `yaml:"seedable"`
	Schema   string `yaml:"schema"`
}

// StateSpec bounds the state a node hands to Jev.
type StateSpec struct {
	From     []string `yaml:"from"`
	MaxBytes int      `yaml:"maxBytes"`
}

// Node is one workflow node. Not every field applies to every kind; Validate
// enforces which fields a given kind requires.
type Node struct {
	ID          string            `yaml:"id"`
	Kind        string            `yaml:"kind"`
	Agent       string            `yaml:"agent"`
	Produces    []Artifact        `yaml:"produces"`
	Consumes    []Artifact        `yaml:"consumes"`
	Objective   string            `yaml:"objective"`
	Next        string            `yaml:"next"`
	QuestionSet string            `yaml:"questionSet"`
	State       *StateSpec        `yaml:"state"`
	Routes      map[string]string `yaml:"routes"`
	Default     string            `yaml:"default"`
	// TrueRoute names which of a gate node's two Routes keys is taken when
	// its noul question answers true (e.g. "yes" or "ready"); the other key
	// is taken when it answers false. A noul answer carries no chosen label
	// of its own — only a truth value — so a generic engine has no other way
	// to know which authored label means which outcome. Gate-only.
	TrueRoute  string   `yaml:"trueRoute"`
	Candidates []string `yaml:"candidates"`
	AssignTo   string   `yaml:"assignTo"`
	Command    string   `yaml:"command"`
	Prompt     string   `yaml:"prompt"`
	Outcome    string   `yaml:"outcome"`
}

// Workflow is one authored spec file.
type Workflow struct {
	Version     int     `yaml:"version" json:"version"`
	Name        string  `yaml:"name" json:"name"`
	Description string  `yaml:"description" json:"description"`
	Budgets     Budgets `yaml:"budgets" json:"budgets,omitempty"`
	Nodes       []Node  `yaml:"nodes" json:"nodes,omitempty"`
	Entry       string  `yaml:"entry,omitempty" json:"entry,omitempty"`
	MaxSteps    int     `yaml:"maxSteps,omitempty" json:"maxSteps,omitempty"`
	Stages      []Stage `yaml:"stages,omitempty" json:"stages,omitempty"`
}

// Stage workflows are authored as questions, standard SDLC work, and endings.
// They share the project workflow file location with older node graphs.
type Stage struct {
	ID       string         `yaml:"id" json:"id"`
	Question *StageQuestion `yaml:"question,omitempty" json:"question,omitempty"`
	Work     *StageWork     `yaml:"work,omitempty" json:"work,omitempty"`
	Spawn    *StageSpawn    `yaml:"spawn,omitempty" json:"spawn,omitempty"`
	Finish   string         `yaml:"finish,omitempty" json:"finish,omitempty"`
}

type StageQuestion struct {
	Prompt        string            `yaml:"prompt" json:"prompt"`
	Options       map[string]string `yaml:"options" json:"options"`
	Routes        map[string]string `yaml:"routes" json:"routes"`
	Fallback      string            `yaml:"fallback" json:"fallback"`
	MinConfidence float64           `yaml:"minConfidence,omitempty" json:"minConfidence,omitempty"`
}

type StageWork struct {
	Role      string            `yaml:"role" json:"role"`
	Objective string            `yaml:"objective" json:"objective"`
	Focus     string            `yaml:"focus,omitempty" json:"focus,omitempty"`
	Routes    map[string]string `yaml:"routes" json:"routes"`
}

// StageSpawn starts another SDLC run with the parent's task and policy.
type StageSpawn struct {
	Workflow  string            `yaml:"workflow" json:"workflow"`
	Objective string            `yaml:"objective,omitempty" json:"objective,omitempty"`
	Routes    map[string]string `yaml:"routes" json:"routes"`
}

func (w *Workflow) IsStageFlow() bool { return len(w.Stages) > 0 }

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
var idRE = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)

// Load parses and validates raw YAML into a Workflow, applying budget
// defaults. Unknown fields are rejected so a typo never silently no-ops.
func Load(raw []byte) (*Workflow, error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	w := Workflow{Budgets: Budgets{MaxReroutes: unsetMaxReroutes}}
	if err := dec.Decode(&w); err != nil {
		return nil, fmt.Errorf("sdlc: parse workflow: %w", err)
	}
	w.Budgets.applyDefaults()
	if w.IsStageFlow() {
		if err := w.ValidateStages(); err != nil {
			return nil, err
		}
		return &w, nil
	}
	if err := w.Validate(); err != nil {
		return nil, err
	}
	return &w, nil
}

// Validate checks structural and cross-referential correctness: the schema
// JSON Schema cannot express (target existence, kind-specific requirements,
// id uniqueness) is enforced here.
func (w *Workflow) Validate() error {
	if w.Version != 1 {
		return fmt.Errorf("sdlc: workflow version must be 1, got %d", w.Version)
	}
	if !nameRE.MatchString(w.Name) {
		return fmt.Errorf("sdlc: workflow name %q must match %s", w.Name, nameRE.String())
	}
	if w.Description == "" {
		return errors.New("sdlc: workflow description is required: it is the rubric jev.sdlc.workflow-selection matches a bare --task against")
	}
	if len(w.Nodes) == 0 {
		return errors.New("sdlc: workflow declares no nodes")
	}
	if w.Budgets.MaxNodeAttempts < 1 {
		return errors.New("sdlc: budgets.maxNodeAttempts must be at least 1")
	}
	if w.Budgets.MaxReroutes < 0 {
		return errors.New("sdlc: budgets.maxReroutes must not be negative")
	}
	if w.Budgets.MaxRunActiveSeconds < 1 {
		return errors.New("sdlc: budgets.maxRunActiveSeconds must be at least 1")
	}
	switch w.Budgets.MissingUsage {
	case "warn", "block", "ignore":
	default:
		return fmt.Errorf("sdlc: budgets.missingUsage must be warn, block or ignore, got %q", w.Budgets.MissingUsage)
	}

	ids := make(map[string]bool, len(w.Nodes))
	for _, n := range w.Nodes {
		if n.ID == "" {
			return errors.New("sdlc: node id must not be empty")
		}
		if !idRE.MatchString(n.ID) {
			return fmt.Errorf("sdlc: node id %q must match %s", n.ID, idRE.String())
		}
		if ids[n.ID] {
			return fmt.Errorf("sdlc: duplicate node id %q", n.ID)
		}
		ids[n.ID] = true
	}

	seedablePaths := map[string]string{}
	producedPaths := map[string]string{}
	for _, n := range w.Nodes {
		if !validKinds[n.Kind] {
			return fmt.Errorf("sdlc: node %q: unknown kind %q", n.ID, n.Kind)
		}
		if err := n.validateKind(); err != nil {
			return fmt.Errorf("sdlc: node %q: %w", n.ID, err)
		}
		for _, a := range n.Produces {
			if a.Path == "" {
				return fmt.Errorf("sdlc: node %q: produces artifact path must not be empty", n.ID)
			}
			if owner, dup := producedPaths[a.Path]; dup {
				return fmt.Errorf("sdlc: artifact %q is produced by both %q and %q", a.Path, owner, n.ID)
			}
			producedPaths[a.Path] = n.ID
			if a.Seedable {
				if owner, dup := seedablePaths[a.Path]; dup {
					return fmt.Errorf("sdlc: seedable artifact %q declared by both %q and %q", a.Path, owner, n.ID)
				}
				seedablePaths[a.Path] = n.ID
			}
		}
	}

	target := func(field, id string) error {
		if id == "" {
			return nil
		}
		if !ids[id] {
			return fmt.Errorf("sdlc: %s %q is not a declared node id", field, id)
		}
		return nil
	}
	for _, n := range w.Nodes {
		if err := target("next", n.Next); err != nil {
			return err
		}
		// A select node's default names a candidate agent id, not a graph
		// node id; every other kind's default is a route target.
		if n.Kind != KindSelect {
			if err := target("default", n.Default); err != nil {
				return err
			}
		}
		routeKeys := make([]string, 0, len(n.Routes))
		for label, tgt := range n.Routes {
			routeKeys = append(routeKeys, label)
			if err := target(fmt.Sprintf("route %q", label), tgt); err != nil {
				return fmt.Errorf("sdlc: node %q: %w", n.ID, err)
			}
		}
		sort.Strings(routeKeys)
		if n.Kind == KindSelect {
			// A select node's traversal edge is assignTo (the node that runs
			// under the chosen agent); default names a candidate agent id,
			// not a node id, so it is checked against Candidates instead of
			// the node-id namespace.
			if err := target("assignTo", n.AssignTo); err != nil {
				return fmt.Errorf("sdlc: node %q: %w", n.ID, err)
			}
			cand := make(map[string]bool, len(n.Candidates))
			for _, c := range n.Candidates {
				cand[c] = true
			}
			if n.Default != "" && !cand[n.Default] {
				return fmt.Errorf("sdlc: node %q: default %q is not among candidates", n.ID, n.Default)
			}
		}
	}

	assignedTo := map[string]bool{}
	for _, n := range w.Nodes {
		if n.Kind == KindSelect && n.AssignTo != "" {
			assignedTo[n.AssignTo] = true
		}
	}
	for _, n := range w.Nodes {
		if n.Kind == KindWork && n.Agent != "self" && !assignedTo[n.ID] {
			return fmt.Errorf("sdlc: node %q: a non-self work node must be some select node's assignTo, or it can never be assigned an agent", n.ID)
		}
	}
	return nil
}

// validateKind enforces the fields a node's kind requires, beyond generic
// target-existence checks.
func (n Node) validateKind() error {
	switch n.Kind {
	case KindWork:
		if n.Agent != "" && n.Agent != "self" {
			return fmt.Errorf("work node agent must be empty or %q (a select node's assignTo provides any other agent)", "self")
		}
		if n.Next == "" {
			return errors.New("work node requires next")
		}
		if n.Objective == "" && n.Agent != "self" {
			return errors.New("work node requires an objective")
		}
	case KindGate:
		if n.QuestionSet == "" {
			return errors.New("gate node requires questionSet")
		}
		// A gate's question is always noul (proceed or loop), so its answer
		// carries a confidence, not a chosen route label — a gate's two
		// authored labels aren't fixed to "yes"/"no" (the plan's own example
		// uses "ready"/"not-ready"), and which label means "true" isn't
		// derivable from the labels themselves, so trueRoute (below) says so
		// explicitly. What default's match requirement checks is only that
		// the fallback path is a real, already-declared destination rather
		// than a hidden third edge.
		if len(n.Routes) != 2 {
			return errors.New("gate node routes must have exactly two entries (proceed and loop)")
		}
		if n.Default == "" {
			return errors.New("gate node requires default")
		}
		matches := 0
		for _, tgt := range n.Routes {
			if tgt == n.Default {
				matches++
			}
		}
		if matches != 1 {
			return errors.New("gate node default must equal exactly one of its two routes' targets (the low-confidence path)")
		}
		if _, ok := n.Routes[n.TrueRoute]; n.TrueRoute == "" || !ok {
			return errors.New("gate node requires trueRoute naming which of its two routes is taken when the noul answers true")
		}
	case KindDecide:
		if n.QuestionSet == "" {
			return errors.New("decide node requires questionSet")
		}
		if len(n.Routes) == 0 {
			return errors.New("decide node requires routes")
		}
		if n.Default == "" {
			return errors.New("decide node requires default: the low-confidence fallback path")
		}
	case KindSelect:
		if n.QuestionSet == "" {
			return errors.New("select node requires questionSet")
		}
		if len(n.Candidates) == 0 {
			return errors.New("select node requires candidates")
		}
		if n.AssignTo == "" {
			return errors.New("select node requires assignTo")
		}
		if n.Default == "" {
			return errors.New("select node requires default")
		}
	case KindCheck:
		if n.Command == "" {
			return errors.New("check node requires command")
		}
		if len(n.Routes) == 0 {
			return errors.New("check node requires routes")
		}
	case KindHuman:
		if n.Prompt == "" {
			return errors.New("human node requires prompt")
		}
		if len(n.Routes) == 0 {
			return errors.New("human node requires routes")
		}
	case KindJoin:
		if n.Next == "" {
			return errors.New("join node requires next")
		}
	case KindTerminal:
		if n.Outcome == "" {
			return errors.New("terminal node requires outcome")
		}
	}
	return nil
}

// SeedableArtifacts lists every seedable artifact path declared anywhere in
// the workflow, in node order, for `--file` entry-point resolution.
func (w *Workflow) SeedableArtifacts() []string {
	var out []string
	for _, n := range w.Nodes {
		for _, a := range n.Produces {
			if a.Seedable {
				out = append(out, a.Path)
			}
		}
	}
	return out
}

// Node looks up a node by id.
func (w *Workflow) Node(id string) (Node, bool) {
	for _, n := range w.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return Node{}, false
}
