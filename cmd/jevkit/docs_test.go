package main

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// userDocPaths are the user-facing markdown files this TODO owns, plus the
// redaction guide linked from the README.
var userDocPaths = []string{
	"../../README.md",
	"../../docs/AGENT-INTEGRATIONS.md",
	"../../docs/KEYS.md",
	"../../docs/REDACTION.md",
	"../../CONTRIBUTING.md",
}

var (
	mdLinkRE = regexp.MustCompile(`\[[^\]]*\]\(([^)]+)\)`)
	// Leading optional prompt ($ or %) then jevkit and tokens.
	jevkitLineRE = regexp.MustCompile(`(?m)^[ \t]*(?:\$ |% )?jevkit\b(.*)`)
)

// TestUserDocsMarkdownLinksExist checks relative markdown links resolve on disk.
func TestUserDocsMarkdownLinksExist(t *testing.T) {
	for _, rel := range userDocPaths {
		path := filepath.Clean(rel)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read %s: %v", path, err)
			continue
		}
		base := filepath.Dir(path)
		for _, m := range mdLinkRE.FindAllStringSubmatch(string(data), -1) {
			raw := strings.TrimSpace(m[1])
			if raw == "" || strings.HasPrefix(raw, "#") {
				continue
			}
			u, err := url.Parse(raw)
			if err != nil {
				t.Errorf("%s: bad link %q: %v", path, raw, err)
				continue
			}
			if u.Scheme != "" || u.Host != "" {
				continue // external
			}
			target := u.Path
			if target == "" {
				continue
			}
			full := filepath.Clean(filepath.Join(base, target))
			if _, err := os.Stat(full); err != nil {
				t.Errorf("%s: broken link %q → %s", path, raw, full)
			}
		}
	}
}

// TestUserDocsCodeFenceCommandsExist parses fenced commands in the user docs
// and checks each jevkit invocation against the cobra command tree (plus the
// redact dispatcher subcommands, which are not cobra children).
func TestUserDocsCodeFenceCommandsExist(t *testing.T) {
	tree := commandTree(t)
	redactSubs := redactSubcommands()
	var checked int
	for _, rel := range []string{"../../README.md", "../../docs/AGENT-INTEGRATIONS.md", "../../docs/KEYS.md"} {
		path := filepath.Clean(rel)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, f := range docFences(t, string(data)) {
			if f.info != "" && !isShellFence(f.info) {
				continue
			}
			for _, line := range strings.Split(f.body, "\n") {
				line = stripFenceComment(line)
				m := jevkitLineRE.FindStringSubmatch(line)
				if m == nil {
					continue
				}
				args := tokenizeCLI(m[1])
				pathArgs, ok := resolveCommandPath(tree, redactSubs, args)
				if !ok {
					t.Errorf("%s:%d: unknown command in fence: jevkit %s\nline: %s",
						path, f.line, strings.Join(args, " "), line)
					continue
				}
				checked++
				_ = pathArgs
			}
		}
	}
	if checked < 10 {
		t.Errorf("only %d jevkit commands found in user-doc fences; expected a quickstart set", checked)
	}
}

type docFence struct {
	info string
	body string
	line int
}

func docFences(t *testing.T, src string) []docFence {
	t.Helper()
	var out []docFence
	var cur *docFence
	for i, l := range strings.Split(src, "\n") {
		switch {
		case cur == nil && strings.HasPrefix(l, "```"):
			cur = &docFence{info: strings.TrimSpace(strings.TrimPrefix(l, "```")), line: i + 1}
		case cur != nil && strings.HasPrefix(l, "```"):
			out = append(out, *cur)
			cur = nil
		case cur != nil:
			cur.body += l + "\n"
		}
	}
	if cur != nil {
		t.Fatalf("unterminated code fence at line %d", cur.line)
	}
	return out
}

func isShellFence(info string) bool {
	fields := strings.Fields(strings.ToLower(info))
	if len(fields) == 0 {
		return true
	}
	switch fields[0] {
	case "bash", "sh", "shell", "console", "zsh", "fish":
		return true
	default:
		return false
	}
}

func stripFenceComment(line string) string {
	line = strings.TrimSpace(line)
	if i := strings.Index(line, " #"); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	if strings.HasPrefix(line, "#") {
		return ""
	}
	return line
}

func tokenizeCLI(rest string) []string {
	rest = strings.TrimSpace(rest)
	// Cut at shell operators that start a new command or redirect.
	for _, sep := range []string{"|", ";", "&&", "||", "<", ">"} {
		if i := strings.Index(rest, sep); i >= 0 {
			rest = rest[:i]
		}
	}
	var out []string
	for _, f := range strings.Fields(rest) {
		if strings.HasPrefix(f, "-") {
			// Stop before flags: remaining tokens are flag args, not subcommands.
			break
		}
		out = append(out, f)
	}
	return out
}

func commandTree(t *testing.T) map[string]*cobra.Command {
	t.Helper()
	a := newApp(t)
	root := a.rootCmd()
	out := map[string]*cobra.Command{"": root}
	var walk func(prefix string, c *cobra.Command)
	walk = func(prefix string, c *cobra.Command) {
		for _, sub := range c.Commands() {
			if !sub.IsAvailableCommand() || sub.Hidden {
				continue
			}
			name := sub.Name()
			if name == "help" {
				continue
			}
			key := name
			if prefix != "" {
				key = prefix + " " + name
			}
			out[key] = sub
			walk(key, sub)
		}
	}
	walk("", root)
	return out
}

func redactSubcommands() map[string]bool {
	subs := map[string]bool{"help": true}
	for _, line := range strings.Split(redactUsage, "\n") {
		// usage lines look like "  init [--project] ..."
		if !strings.HasPrefix(line, "  ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		name := fields[0]
		if strings.HasPrefix(name, "-") || name == "usage:" || name == "subcommands:" {
			continue
		}
		subs[name] = true
	}
	return subs
}

// resolveCommandPath walks args against the cobra tree. Returns the matched
// path and whether it is valid. Positional args after the leaf are ignored.
func resolveCommandPath(tree map[string]*cobra.Command, redactSubs map[string]bool, args []string) ([]string, bool) {
	if len(args) == 0 {
		return nil, true // bare `jevkit` is fine in docs
	}
	var path []string
	for i, tok := range args {
		candidate := strings.Join(append(append([]string{}, path...), tok), " ")
		if _, ok := tree[candidate]; ok {
			path = append(path, tok)
			continue
		}
		// redact subcommands are dispatched outside cobra.
		if len(path) == 1 && path[0] == "redact" {
			if !redactSubs[tok] {
				return path, false
			}
			return append(path, tok), true
		}
		// Remaining tokens are positional (agent name, event, …).
		if len(path) > 0 {
			return path, true
		}
		if i == 0 {
			return nil, false
		}
		return path, true
	}
	return path, true
}
