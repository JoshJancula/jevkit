package compact

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Policy is a declarative local compaction policy. Rules can only narrow
// eligibility or select the existing deterministic path; they never execute
// code or override hard source/binary guards.
type Policy struct {
	Version int    `yaml:"version"`
	Rules   []Rule `yaml:"rules"`
}

type Rule struct {
	ID        string `yaml:"id"`
	Command   string `yaml:"command,omitempty"`
	Output    string `yaml:"output,omitempty"`
	Action    string `yaml:"action"`
	Threshold int    `yaml:"threshold_bytes,omitempty"`
	commandRE *regexp.Regexp
	outputRE  *regexp.Regexp
}

const (
	ActionNever         = "never-compact"
	ActionDeterministic = "deterministic-only"
	ActionEligible      = "eligible"
)

// LoadPolicy parses a bounded declarative policy. A project policy may only
// add never-compact rules, because repository content is untrusted.
func LoadPolicy(path string, project bool) (*Policy, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Policy
	if err := yaml.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("compaction policy: %w", err)
	}
	if p.Version != 1 {
		return nil, fmt.Errorf("compaction policy: version must be 1")
	}
	seen := map[string]bool{}
	for i := range p.Rules {
		r := &p.Rules[i]
		if r.ID == "" || seen[r.ID] {
			return nil, fmt.Errorf("compaction policy: rule ids must be non-empty and unique")
		}
		seen[r.ID] = true
		if r.Action != ActionNever && r.Action != ActionDeterministic && r.Action != ActionEligible {
			return nil, fmt.Errorf("compaction policy: %s has invalid action", r.ID)
		}
		if project && r.Action != ActionNever {
			return nil, fmt.Errorf("compaction policy: project rules may only use never-compact")
		}
		if r.Command == "" && r.Output == "" {
			return nil, fmt.Errorf("compaction policy: %s needs command or output matcher", r.ID)
		}
		if len(r.Command) > 512 || len(r.Output) > 512 {
			return nil, fmt.Errorf("compaction policy: %s matcher too long", r.ID)
		}
		if r.Threshold < 0 || r.Threshold > 16*1024*1024 {
			return nil, fmt.Errorf("compaction policy: %s threshold outside 0..16777216", r.ID)
		}
		if r.Command != "" {
			if r.commandRE, err = regexp.Compile(r.Command); err != nil {
				return nil, fmt.Errorf("compaction policy: %s command: %w", r.ID, err)
			}
		}
		if r.Output != "" {
			if r.outputRE, err = regexp.Compile(r.Output); err != nil {
				return nil, fmt.Errorf("compaction policy: %s output: %w", r.ID, err)
			}
		}
	}
	return &p, nil
}

// Match returns the first matching rule. Hard protections are evaluated by
// Compact before a policy result can ever be acted on.
func (p *Policy) Match(command, output string) *Rule {
	if p == nil {
		return nil
	}
	for i := range p.Rules {
		r := &p.Rules[i]
		if r.commandRE != nil && !r.commandRE.MatchString(command) {
			continue
		}
		if r.outputRE != nil && !r.outputRE.MatchString(output) {
			continue
		}
		return r
	}
	return nil
}

func (r *Rule) String() string {
	if r == nil {
		return "default"
	}
	return strings.TrimSpace(r.ID) + " (" + r.Action + ")"
}
