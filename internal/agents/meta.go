package agents

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DetectBins maps adapter Name() -> PATH binaries that mean the agent is
// installed. First hit wins for doctor display.
var DetectBins = map[string][]string{
	ClaudeName:      {"claude"},
	CursorName:      {"cursor-agent", "cursor"},
	CodexName:       {"codex"},
	OpenCodeName:    {"opencode"},
	AntigravityName: {"agy", "gemini"},
}

// HookMarker is the idempotency substring for each adapter's managed hooks.
var HookMarker = map[string]string{
	ClaudeName:      ClaudeHookMarker,
	CursorName:      CursorHookMarker,
	CodexName:       CodexHookMarker,
	OpenCodeName:    OpenCodePluginMarker,
	AntigravityName: AntigravityHookMarker,
}

// SortedNames returns registered adapter names in stable order.
func SortedNames() []string {
	names := Names()
	sort.Strings(names)
	return names
}

// ResolveScope returns "project" or "user" given opts.
func ResolveScope(opts InstallOptions) string {
	scope := strings.ToLower(strings.TrimSpace(opts.Scope))
	if scope != "" {
		return scope
	}
	if opts.WorkDir != "" {
		return "project"
	}
	return "user"
}

// HookConfigPath is the primary hooks/settings/plugin path for name.
func HookConfigPath(name string, opts InstallOptions) (string, error) {
	switch name {
	case ClaudeName:
		return claudeSettingsPath(opts)
	case CursorName:
		return cursorHooksPath(opts)
	case CodexName:
		return codexHooksPath(opts)
	case OpenCodeName:
		return opencodePluginPath(opts)
	case AntigravityName:
		return antigravityHooksPath(opts)
	default:
		return "", errUnknownAgent(name)
	}
}

// MCPConfigPath is where jevkit registers its MCP server for name.
func MCPConfigPath(name string, opts InstallOptions) (string, error) {
	scope := ResolveScope(opts)
	switch name {
	case ClaudeName:
		if scope == "project" {
			if opts.WorkDir == "" {
				return "", errNeedWorkDir(name)
			}
			return filepath.Join(opts.WorkDir, ".mcp.json"), nil
		}
		if opts.ConfigDir == "" {
			return "", errNeedHome(name)
		}
		return filepath.Join(opts.ConfigDir, ".claude", "mcp.json"), nil
	case CursorName:
		base, err := scopeBase(name, scope, opts, ".cursor")
		if err != nil {
			return "", err
		}
		return filepath.Join(base, "mcp.json"), nil
	case AntigravityName:
		base, err := scopeBase(name, scope, opts, ".agents")
		if err != nil {
			return "", err
		}
		return filepath.Join(base, "mcp_config.json"), nil
	case OpenCodeName:
		if scope == "project" {
			if opts.WorkDir == "" {
				return "", errNeedWorkDir(name)
			}
			return filepath.Join(opts.WorkDir, "opencode.json"), nil
		}
		if opts.ConfigDir == "" {
			return "", errNeedHome(name)
		}
		return filepath.Join(opts.ConfigDir, ".opencode", "opencode.json"), nil
	case CodexName:
		base, err := scopeBase(name, scope, opts, ".codex")
		if err != nil {
			return "", err
		}
		return filepath.Join(base, "config.toml"), nil
	default:
		return "", errUnknownAgent(name)
	}
}

func scopeBase(name, scope string, opts InstallOptions, dir string) (string, error) {
	switch scope {
	case "project":
		if opts.WorkDir == "" {
			return "", errNeedWorkDir(name)
		}
		return filepath.Join(opts.WorkDir, dir), nil
	case "user":
		if opts.ConfigDir == "" {
			return "", errNeedHome(name)
		}
		return filepath.Join(opts.ConfigDir, dir), nil
	default:
		return "", errUnknownScope(name, scope)
	}
}

// InstallState is per-agent install reporting for doctor.
type InstallState struct {
	Name     string
	Detected bool
	Binary   string // first PATH hit; empty when not detected
	Hooks    string // "not installed" | "project" | "user" | "project+user"
	MCP      string // same vocabulary
}

// DetectLookPath finds the first PATH hit for name's detect binaries.
func DetectLookPath(name string, look func(string) (string, error)) (path string, ok bool) {
	if look == nil {
		return "", false
	}
	for _, bin := range DetectBins[name] {
		if p, err := look(bin); err == nil && p != "" {
			return p, true
		}
	}
	return "", false
}

// StateOf reports detection and whether jevkit hooks/MCP are present.
func StateOf(name string, workDir, homeDir string, look func(string) (string, error)) InstallState {
	st := InstallState{Name: name, Hooks: "not installed", MCP: "not installed"}
	if p, ok := DetectLookPath(name, look); ok {
		st.Detected = true
		st.Binary = p
	}
	st.Hooks = layerState(name, workDir, homeDir, hooksInstalled)
	st.MCP = layerState(name, workDir, homeDir, mcpInstalled)
	return st
}

// AllStates returns InstallState for every registered adapter in sorted order.
func AllStates(workDir, homeDir string, look func(string) (string, error)) []InstallState {
	names := SortedNames()
	out := make([]InstallState, 0, len(names))
	for _, n := range names {
		out = append(out, StateOf(n, workDir, homeDir, look))
	}
	return out
}

func layerState(name, workDir, homeDir string, check func(string, InstallOptions) bool) string {
	var layers []string
	if workDir != "" {
		opts := InstallOptions{WorkDir: workDir, Scope: "project"}
		if check(name, opts) {
			layers = append(layers, "project")
		}
	}
	if homeDir != "" {
		opts := InstallOptions{ConfigDir: homeDir, Scope: "user"}
		if check(name, opts) {
			layers = append(layers, "user")
		}
	}
	if len(layers) == 0 {
		return "not installed"
	}
	return strings.Join(layers, "+")
}

func hooksInstalled(name string, opts InstallOptions) bool {
	path, err := HookConfigPath(name, opts)
	if err != nil {
		return false
	}
	marker := HookMarker[name]
	if marker == "" {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), marker)
}

func mcpInstalled(name string, opts InstallOptions) bool {
	path, err := MCPConfigPath(name, opts)
	if err != nil {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return mcpContainsJevkit(name, data)
}

func mcpContainsJevkit(name string, data []byte) bool {
	s := string(data)
	switch name {
	case OpenCodeName:
		// OpenCode uses "mcp": { "jevkit": ... }
		return strings.Contains(s, `"jevkit"`) && strings.Contains(s, `"mcp"`)
	case CodexName:
		return strings.Contains(s, codexMCPBegin) || strings.Contains(s, "[mcp_servers.jevkit]")
	default:
		return strings.Contains(s, `"jevkit"`) && strings.Contains(s, "mcp")
	}
}
