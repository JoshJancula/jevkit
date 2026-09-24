package catalog

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// LedgerAgent is one .jevkit/sdlc/agents.yaml entry.
type LedgerAgent struct {
	ID          string   `yaml:"id"`
	Rubric      string   `yaml:"rubric"`
	Via         string   `yaml:"via"`
	Subagent    string   `yaml:"subagent"`
	Runtime     string   `yaml:"runtime"`
	Model       string   `yaml:"model"`
	Agent       string   `yaml:"agent"` // the foreign runtime's own named agent
	Binary      string   `yaml:"binary"`
	WriteScopes []string `yaml:"writeScopes"`
	ReadOnly    bool     `yaml:"readOnly"`
}

// LedgerFile is one parsed agents.yaml, with Path kept for diagnostics and
// Source attribution.
type LedgerFile struct {
	Version int           `yaml:"version"`
	Agents  []LedgerAgent `yaml:"agents"`
	Path    string        `yaml:"-"`
}

// LoadLedger reads and validates path. A missing file returns a zero-value,
// empty LedgerFile (no ledger is a valid, if unusual, configuration): the
// caller decides whether that is acceptable.
func LoadLedger(path string) (LedgerFile, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return LedgerFile{Path: path}, nil
	}
	if err != nil {
		return LedgerFile{}, fmt.Errorf("sdlc: catalog: read %s: %w", path, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var l LedgerFile
	if err := dec.Decode(&l); err != nil {
		return LedgerFile{}, fmt.Errorf("sdlc: catalog: parse %s: %w", path, err)
	}
	l.Path = path
	if l.Version != 1 {
		return LedgerFile{}, fmt.Errorf("sdlc: catalog: %s: version must be 1, got %d", path, l.Version)
	}
	seen := map[string]bool{}
	for _, e := range l.Agents {
		if e.ID == "" {
			return LedgerFile{}, fmt.Errorf("sdlc: catalog: %s: agent id must not be empty", path)
		}
		if seen[e.ID] {
			return LedgerFile{}, fmt.Errorf("sdlc: catalog: %s: duplicate agent id %q", path, e.ID)
		}
		seen[e.ID] = true
		if err := e.validate(); err != nil {
			return LedgerFile{}, fmt.Errorf("sdlc: catalog: %s: agent %q: %w", path, e.ID, err)
		}
	}
	return l, nil
}

func (e LedgerAgent) validate() error {
	switch e.Via {
	case ViaNative:
		if e.Subagent == "" {
			return fmt.Errorf("via: native requires subagent")
		}
		if e.Runtime != "" || e.Model != "" || e.Agent != "" || e.Binary != "" || e.ReadOnly || len(e.WriteScopes) > 0 {
			return fmt.Errorf("via: native cannot declare runtime reach fields (runtime, model, agent, binary, readOnly, writeScopes)")
		}
	case ViaRuntime:
		if e.Runtime == "" {
			return fmt.Errorf("via: runtime requires runtime")
		}
		if e.Subagent != "" {
			return fmt.Errorf("via: runtime cannot declare subagent")
		}
	default:
		return fmt.Errorf("via must be %q or %q, got %q", ViaNative, ViaRuntime, e.Via)
	}
	if e.Rubric == "" {
		return fmt.Errorf("rubric is required: it is the criteria text Jev matches this candidate against")
	}
	return nil
}

func (e LedgerAgent) toAgent(path string) (*Agent, error) {
	if err := e.validate(); err != nil {
		return nil, fmt.Errorf("sdlc: catalog: %s: agent %q: %w", path, e.ID, err)
	}
	a := &Agent{
		ID: e.ID, Rubric: e.Rubric, Via: e.Via,
		Subagent: e.Subagent, Runtime: e.Runtime, Model: e.Model,
		RuntimeAgent: e.Agent, Binary: e.Binary,
		WriteScopes: e.WriteScopes, ReadOnly: e.ReadOnly,
		Source: "ledger:" + path,
	}
	return a, nil
}
