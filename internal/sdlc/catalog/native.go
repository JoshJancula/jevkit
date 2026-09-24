package catalog

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// NativeAgent is one agent discovered from the host's own agent definitions.
// A file that fails to parse yields a zero-value NativeAgent (Name == "")
// plus an entry in Warnings; discovery never fails outright over one bad
// file.
type NativeAgent struct {
	Name        string
	Description string
	Model       string
	Path        string
	Warnings    []string
}

// frontmatter is the subset of a Claude Code subagent's YAML frontmatter
// catalog cares about; description doubles, verbatim, as the Jev rubric.
type frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Model       string `yaml:"model"`
}

// DiscoverNative scans dirs (typically the project's .claude/agents and the
// user's ~/.claude/agents, in that order) for *.md subagent definitions.
// Absent directories are skipped silently; a present but unparseable file is
// skipped with a warning, never fatal — matching how a missing or malformed
// agent definition should never block the rest of a catalog from loading.
// The first directory to define a given subagent name wins; callers pass the
// project directory before the user directory so a project definition
// overrides a same-named user one, mirroring how Claude Code itself layers
// project and user agent definitions.
func DiscoverNative(dirs ...string) []NativeAgent {
	byName := map[string]NativeAgent{}
	var order []string
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue // absent or unreadable directory: not an error.
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, name := range names {
			path := filepath.Join(dir, name)
			a := parseAgentFile(path)
			if a.Name == "" {
				// Unparseable: still surface it, but it never joins byName
				// and so never becomes a catalog candidate.
				order = append(order, path)
				byName[path] = a
				continue
			}
			if _, exists := byName[a.Name]; exists {
				continue // an earlier directory already defined this name.
			}
			order = append(order, a.Name)
			byName[a.Name] = a
		}
	}
	out := make([]NativeAgent, 0, len(order))
	for _, key := range order {
		out = append(out, byName[key])
	}
	return out
}

// parseAgentFile reads one *.md subagent file and extracts its frontmatter.
// Any failure (missing delimiters, invalid YAML, missing name or
// description) yields a zero-value NativeAgent carrying a Warnings entry.
func parseAgentFile(path string) NativeAgent {
	raw, err := os.ReadFile(path)
	if err != nil {
		return NativeAgent{Warnings: []string{fmt.Sprintf("%s: %v", path, err)}}
	}
	body, ok := extractFrontmatter(raw)
	if !ok {
		return NativeAgent{Warnings: []string{fmt.Sprintf("%s: missing --- frontmatter delimiters", path)}}
	}
	var fm frontmatter
	dec := yaml.NewDecoder(bytes.NewReader(body))
	dec.KnownFields(false) // a subagent's frontmatter may carry fields jevkit doesn't use (tools, color, ...).
	if err := dec.Decode(&fm); err != nil {
		return NativeAgent{Warnings: []string{fmt.Sprintf("%s: invalid frontmatter: %v", path, err)}}
	}
	if fm.Name == "" || fm.Description == "" {
		return NativeAgent{Warnings: []string{fmt.Sprintf("%s: frontmatter needs both name and description", path)}}
	}
	return NativeAgent{Name: fm.Name, Description: fm.Description, Model: fm.Model, Path: path}
}

// extractFrontmatter returns the YAML block between a leading "---\n" line
// and the next "---" line on its own, or ok=false when the file does not
// start with that shape.
func extractFrontmatter(raw []byte) (body []byte, ok bool) {
	const delim = "---"
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF}) // tolerate a UTF-8 BOM.
	lines := strings.Split(string(raw), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != delim {
		return nil, false
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == delim {
			return []byte(strings.Join(lines[1:i], "\n")), true
		}
	}
	return nil, false
}
