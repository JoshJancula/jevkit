package agents

import (
	"fmt"
	"strings"
)

// ApplyReport is the outcome of InstallAgent / UninstallAgent.
type ApplyReport struct {
	Agent    string
	Previews []FilePreview
}

// InstallAgent runs hook Install then MCP registration for one adapter.
func InstallAgent(a Agent, opts InstallOptions) (ApplyReport, error) {
	return InstallAgentComponents(a, opts, DefaultComponents())
}

// InstallAgentComponents installs only the selected, independently reversible
// integration components.
func InstallAgentComponents(a Agent, opts InstallOptions, components Components) (ApplyReport, error) {
	if a == nil {
		return ApplyReport{}, fmt.Errorf("nil agent")
	}
	rep := ApplyReport{Agent: a.Name()}
	opts = withPreviewCollector(opts, &rep.Previews)
	if components.Hooks {
		if err := a.Install(opts); err != nil {
			return rep, err
		}
	}
	if components.MCP {
		if err := InstallMCP(a.Name(), opts); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

// UninstallAgent removes MCP registration then restores hook config.
func UninstallAgent(a Agent, opts InstallOptions) (ApplyReport, error) {
	return UninstallAgentComponents(a, opts, DefaultComponents())
}

// UninstallAgentComponents removes only components selected by the caller.
func UninstallAgentComponents(a Agent, opts InstallOptions, components Components) (ApplyReport, error) {
	if a == nil {
		return ApplyReport{}, fmt.Errorf("nil agent")
	}
	rep := ApplyReport{Agent: a.Name()}
	opts = withPreviewCollector(opts, &rep.Previews)
	if components.MCP {
		if err := UninstallMCP(a.Name(), opts); err != nil {
			return rep, err
		}
	}
	if components.Hooks {
		if err := a.Uninstall(opts); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

func withPreviewCollector(opts InstallOptions, into *[]FilePreview) InstallOptions {
	prev := opts.Preview
	opts.Preview = func(p FilePreview) {
		*into = append(*into, p)
		if prev != nil {
			prev(p)
		}
	}
	return opts
}

// FormatPreviews renders unified diffs for changed previews.
func FormatPreviews(previews []FilePreview) string {
	var b strings.Builder
	for _, p := range previews {
		if string(p.Before) == string(p.After) {
			continue
		}
		diff := UnifiedDiff(p.Path, p.Path, p.Before, p.After)
		if diff == "" {
			continue
		}
		b.WriteString(diff)
		if !strings.HasSuffix(diff, "\n") {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// SelectAgents resolves "all" or a single name against the registry.
// When all is true and detectedOnly is set, only agents found on PATH are
// returned (explicit names always resolve regardless of detection).
func SelectAgents(target string, look func(string) (string, error), detectedOnly bool) ([]Agent, error) {
	target = strings.ToLower(strings.TrimSpace(target))
	if target == "" {
		return nil, fmt.Errorf("agent name required (or \"all\")")
	}
	if target == "all" {
		var out []Agent
		for _, name := range SortedNames() {
			if detectedOnly {
				if _, ok := DetectLookPath(name, look); !ok {
					continue
				}
			}
			a := Lookup(name)
			if a != nil {
				out = append(out, a)
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("no agents detected on PATH; pass a name (claude, cursor, codex, opencode, antigravity) or install an agent CLI")
		}
		return out, nil
	}
	// Aliases.
	switch target {
	case "agy":
		target = AntigravityName
	case "cursor-agent":
		target = CursorName
	}
	a := Lookup(target)
	if a == nil {
		return nil, fmt.Errorf("unknown agent %q (want claude, cursor, codex, opencode, antigravity, or all)", target)
	}
	return []Agent{a}, nil
}
