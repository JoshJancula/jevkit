// Package catalog builds discovery inventory from native definitions and
// optional project runtime declarations. Discovery does not enroll an agent
// or make it eligible for routing. The enrollment package applies project
// policy, user roster, driver reach, action requirements and budget first.
package catalog

import (
	"fmt"
	"sort"
)

// Provenance values.
const (
	ViaNative  = "native"
	ViaRuntime = "runtime"
)

// enforceableReadOnly lists the runtimes whose worker adapter maps readOnly
// to a real enforced flag (internal/sdlc/worker's capability matrix): claude
// via its permission mode, codex via sandbox config, cursor via --mode
// plan/ask, and Antigravity via --mode plan. OpenCode's read-only behavior is
// agent-defined, not a flag this package can enforce; Ralph's rule
// carries over unchanged: never default to a capability you cannot enforce.
var enforceableReadOnly = map[string]bool{
	"claude":      true,
	"codex":       true,
	"cursor":      true,
	"antigravity": true,
}

// Agent is one discovered inventory entry. Rubric is a suggested enrollment
// rubric; it is not permission to present this ID to Jev.
type Agent struct {
	ID     string
	Rubric string
	Via    string // ViaNative or ViaRuntime

	// Native reach: the host subagent this id delegates to. Equal to ID for
	// an agent discovered directly from the host's own agent definitions;
	// distinct from ID for a ledger entry that augments a native subagent
	// under a narrower id and rubric (agents.yaml's local-security-reviewer
	// pattern).
	Subagent string

	// Runtime reach.
	Runtime      string
	Model        string
	RuntimeAgent string // the foreign runtime's own named agent, e.g. opencode --agent
	Binary       string // override path for the runtime's CLI
	WriteScopes  []string
	ReadOnly     bool

	// Source names where this entry came from, for `sdlc agents` and
	// diagnostics: "native:<path>", "ledger:<path>" or "ledger-override:<path>".
	Source string
}

// Catalog is the merged, validated discovery inventory.
type Catalog struct {
	agents   map[string]*Agent
	Warnings []string
}

// Agent looks up one candidate by id.
func (c *Catalog) Agent(id string) (*Agent, bool) {
	a, ok := c.agents[id]
	return a, ok
}

// IDs returns every candidate id, sorted.
func (c *Catalog) IDs() []string {
	ids := make([]string, 0, len(c.agents))
	for id := range c.agents {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Rubrics returns inventory descriptions. It must not be used as Jev choice
// criteria; enrollment.Rubrics accepts only action-specific eligible agents.
func (c *Catalog) Rubrics() map[string]string {
	out := make(map[string]string, len(c.agents))
	for id, a := range c.agents {
		out[id] = a.Rubric
	}
	return out
}

// Merge combines discovered native agents and ledger-declared agents into one
// namespace, validated for id collisions and readOnly enforceability. Native
// discovery warnings (a skipped, unparseable agent file) are carried through
// on the result rather than failing the merge; a ledger problem (a duplicate
// id, or readOnly declared for a runtime that cannot enforce it) does fail it,
// since the ledger is authored and reviewable, unlike scanned agent files.
func Merge(native []NativeAgent, ledgers ...LedgerFile) (*Catalog, error) {
	c := &Catalog{agents: map[string]*Agent{}}
	for _, n := range native {
		c.Warnings = append(c.Warnings, n.Warnings...)
		if n.Name == "" {
			continue // unparseable; already recorded as a warning.
		}
		id := n.Name
		if n.Runtime != "" {
			id = n.Runtime + "/" + n.Name
		}
		a := &Agent{
			ID: n.Name, Rubric: n.Description, Via: ViaNative,
			Subagent: n.Name, Model: n.Model, Source: "native:" + n.Path,
		}
		if n.Runtime != "" {
			a.ID, a.Via, a.Runtime, a.RuntimeAgent, a.Subagent = id, ViaRuntime, n.Runtime, n.Name, ""
		}
		c.agents[id] = a
	}
	for _, l := range ledgers {
		for _, e := range l.Agents {
			if err := addLedgerEntry(c, e, l.Path); err != nil {
				return nil, err
			}
		}
	}
	return c, nil
}

func addLedgerEntry(c *Catalog, e LedgerAgent, path string) error {
	if existing, dup := c.agents[e.ID]; dup {
		return fmt.Errorf("sdlc: catalog: agent id %q declared by both %s and ledger:%s", e.ID, existing.Source, path)
	}
	a, err := e.toAgent(path)
	if err != nil {
		return err
	}
	if a.ReadOnly && !enforceableReadOnly[a.Runtime] {
		return fmt.Errorf("sdlc: catalog: agent %q declares readOnly for runtime %q, which has no enforced read-only flag", a.ID, a.Runtime)
	}
	c.agents[a.ID] = a
	return nil
}

// ApplyUserOverride applies a user-level ledger as an override: it may adjust
// an existing entry's reach (runtime, model, runtime agent, binary) but must
// name an id already in the catalog, and must not change that entry's rubric
// or write scopes — model access varies per machine, but capability claims
// are project knowledge (internal/redact/config's additive-only precedent).
// readOnly is a capability claim, not reach, and is likewise not overridable.
func (c *Catalog) ApplyUserOverride(l LedgerFile) error {
	for _, e := range l.Agents {
		base, ok := c.agents[e.ID]
		if !ok {
			return fmt.Errorf("sdlc: catalog: user override %s: agent id %q is not in the catalog (overrides may not introduce new agents)", l.Path, e.ID)
		}
		if e.Rubric != "" && e.Rubric != base.Rubric {
			return fmt.Errorf("sdlc: catalog: user override %s: agent %q may not change rubric", l.Path, e.ID)
		}
		if len(e.WriteScopes) > 0 && !equalScopes(e.WriteScopes, base.WriteScopes) {
			return fmt.Errorf("sdlc: catalog: user override %s: agent %q may not change writeScopes", l.Path, e.ID)
		}
		if e.ReadOnly {
			return fmt.Errorf("sdlc: catalog: user override %s: agent %q may not change readOnly (a capability claim, not reach)", l.Path, e.ID)
		}
		if e.Runtime != "" {
			base.Runtime = e.Runtime
		}
		if e.Model != "" {
			base.Model = e.Model
		}
		if e.Agent != "" {
			base.RuntimeAgent = e.Agent
		}
		if e.Binary != "" {
			base.Binary = e.Binary
		}
		base.Source = "ledger-override:" + l.Path + " (base: " + base.Source + ")"
	}
	return nil
}

func equalScopes(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
