package enrollment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ToolPolicy controls built-in tool families for a runtime/model scaffold.
// False disables a family; true and nil retain runtime configuration and
// approval rules. These are tool availability controls, not filesystem,
// network, or external-service security boundaries.
type ToolPolicy struct {
	Auto     bool  `yaml:"-" json:"auto,omitempty"`
	Shell    *bool `yaml:"shell,omitempty" json:"shell,omitempty"`
	Web      *bool `yaml:"web,omitempty" json:"web,omitempty"`
	Delegate *bool `yaml:"delegate,omitempty" json:"delegate,omitempty"`
}

// UnmarshalYAML accepts explicit inheritance or a strictly checked mapping.
func (p *ToolPolicy) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode && node.Tag == "!!str" && node.Value == "auto" {
		*p = ToolPolicy{Auto: true}
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("tools must be auto or a mapping of shell, web, and delegate")
	}
	var decoded struct {
		Shell    *bool          `yaml:"shell"`
		Web      *bool          `yaml:"web"`
		Delegate *bool          `yaml:"delegate"`
		Unknown  map[string]any `yaml:",inline"`
	}
	// Decode the original node so YAML aliases and merged mappings retain
	// their anchors. Capture unknown keys because Node.Decode does not inherit
	// the enclosing decoder's KnownFields setting.
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	if len(decoded.Unknown) > 0 {
		var errors []string
		for field := range decoded.Unknown {
			errors = append(errors, fmt.Sprintf("field %s not found in type enrollment.ToolPolicy", field))
		}
		sort.Strings(errors)
		return &yaml.TypeError{Errors: errors}
	}
	*p = ToolPolicy{Shell: decoded.Shell, Web: decoded.Web, Delegate: decoded.Delegate}
	return nil
}

func (p ToolPolicy) MarshalYAML() (any, error) {
	if p.Auto {
		return "auto", nil
	}
	type mapping ToolPolicy
	return mapping(p), nil
}

func (p *ToolPolicy) Empty() bool {
	return p == nil || p.Shell == nil && p.Web == nil && p.Delegate == nil
}

// Fingerprint prevents sessions with different tool settings being reused.
func (p *ToolPolicy) Fingerprint() string {
	if p.Empty() {
		return ""
	}
	raw, _ := json.Marshal(p)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

// NamedAgentSupported describes actual --agent support in the worker adapter.
func NamedAgentSupported(runtime string) bool {
	return runtime == "claude" || runtime == "opencode" || runtime == "antigravity"
}

func ToolPolicySupported(runtime string) bool {
	return runtime == "codex" || runtime == "claude"
}

// ValidateTools is shared by enrollment, suggestions, and the execution
// boundary. Native agent definitions own their tool allowances, including
// when selected by a CLI invocation rather than a host executor.
func (a Agent) ValidateTools() error {
	if a.RuntimeAgent != "" {
		if strings.TrimSpace(a.RuntimeAgent) == "" || !NamedAgentSupported(a.Runtime) {
			return fmt.Errorf("agent %q: direct named-agent selection is not supported by the %s CLI adapter", a.ID, a.Runtime)
		}
	}
	if a.Tools == nil {
		return nil
	}
	if a.Tools.Auto {
		if !a.Tools.Empty() {
			return fmt.Errorf("agent %q: tools auto cannot be combined with tool restrictions", a.ID)
		}
		return nil
	}
	if a.Via != Runtime || a.RuntimeAgent != "" {
		return fmt.Errorf("agent %q: tools applies only to runtime/model agents; configure native agent tools in the native runtime", a.ID)
	}
	if !ToolPolicySupported(a.Runtime) {
		return fmt.Errorf("agent %q: YAML tool controls are not supported by the %s CLI adapter (supported: codex, claude)", a.ID, a.Runtime)
	}
	if a.Tools.Empty() {
		return fmt.Errorf("agent %q: tools must set shell, web, or delegate; omit tools to inherit runtime configuration", a.ID)
	}
	return nil
}
