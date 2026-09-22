package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/OWNER/jevkit/internal/redact/config"
)

func (a *App) redactAdd(args []string) int {
	fs := a.newFlagSet("redact add")
	pattern := fs.String("pattern", "", "add a regex rule")
	literal := fs.String("literal", "", "add a literal that must never be sent")
	env := fs.String("env", "", "add an env var name (glob) whose value is redacted")
	never := fs.String("never-send", "", "add a never_send command/path glob")
	id := fs.String("id", "", "rule id for --pattern (default: custom.rule-N)")
	flags := fs.String("flags", "", "regex flags for --pattern: any of i, m, s")
	repl := fs.String("replacement", "", "verbatim replacement for --pattern")
	project := fs.Bool("project", false, "edit .jevkit/redact.yaml instead of the user file")
	pos, code, done := parseFlags(fs, args)
	if done {
		return code
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	kinds := 0
	for _, k := range []string{"pattern", "literal", "env", "never-send"} {
		if set[k] {
			kinds++
		}
	}
	if len(pos) > 0 || kinds != 1 {
		a.errf("usage: jevkit redact add --pattern|--literal|--env|--never-send <value> [--project]\n")
		return exitUsage
	}
	if !set["pattern"] && (set["id"] || set["flags"] || set["replacement"]) {
		a.errf("jevkit redact add: --id, --flags and --replacement only apply to --pattern\n")
		return exitUsage
	}
	path, err := a.target(*project)
	if err != nil {
		a.errf("jevkit redact add: %v\n", err)
		return exitFail
	}

	var (
		key, what string
		entry     *yaml.Node
		ruleID    string
	)
	switch {
	case set["pattern"]:
		key, ruleID = "rules", *id
		entry = &yaml.Node{Kind: yaml.MappingNode}
		what = "rule"
	case set["literal"]:
		key, entry, what = "literals", scalar(*literal), "literal"
	case set["env"]:
		key, entry, what = "env_values", scalar(*env), "env_values entry"
	default:
		key, entry, what = "never_send", scalar(*never), "never_send entry"
	}

	old, err := os.ReadFile(path)
	missing := errors.Is(err, os.ErrNotExist)
	switch {
	case missing:
		old = []byte("version: 1\n")
	case err != nil:
		a.errf("jevkit redact add: %v\n", err)
		return exitFail
	}
	if set["pattern"] {
		if ruleID == "" {
			ruleID = nextRuleID(old)
		}
		entry.Content = mapPairs("id", ruleID, "pattern", *pattern)
		if *flags != "" {
			entry.Content = append(entry.Content, scalar("flags"), scalar(*flags))
		}
		if *repl != "" {
			entry.Content = append(entry.Content, scalar("replacement"), scalar(*repl))
		}
	}
	updated, added, err := addEntry(old, key, entry)
	if err != nil {
		a.errf("jevkit redact add: %s: %v\n", path, err)
		return exitFail
	}
	if !added {
		a.outf("already present in %s; nothing changed\n", path)
		return exitOK
	}

	// Validate the whole layered result before touching the real file.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		a.errf("jevkit redact add: %v\n", err)
		return exitFail
	}
	perm := os.FileMode(0o600)
	if fi, err := os.Stat(path); err == nil && *project {
		perm = fi.Mode().Perm()
	}
	tmp, err := stage(path, updated, perm)
	if err != nil {
		a.errf("jevkit redact add: %v\n", err)
		return exitFail
	}
	opts := a.loadOptions()
	if *project {
		opts.ProjectPath = tmp
	} else {
		opts.UserPath = tmp
	}
	if _, err := config.Load(opts); err != nil {
		_ = os.Remove(tmp)
		a.errf("jevkit redact add: rejected, %s left unchanged: %s\n", path, strings.ReplaceAll(unwrapReason(err).Error(), tmp, path))
		return exitFail
	}
	if err := commit(tmp, path); err != nil {
		a.errf("jevkit redact add: %v\n", err)
		return exitFail
	}
	// Never echo a literal: it is the secret.
	switch key {
	case "literals":
		a.outf("added 1 literal to %s\n", path)
	case "rules":
		a.outf("added rule %s to %s\n", ruleID, path)
	default:
		a.outf("added %s %q to %s\n", what, entry.Value, path)
	}
	return exitOK
}

func scalar(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v} }

func mapPairs(kv ...string) []*yaml.Node {
	var out []*yaml.Node
	for _, s := range kv {
		out = append(out, scalar(s))
	}
	return out
}

// rootMapping parses data and returns its top-level mapping, creating an empty
// one for an empty document.
func rootMapping(data []byte) (*yaml.Node, *yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, nil, fmt.Errorf("invalid YAML: %w", err)
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, nil, errors.New("must be a YAML mapping")
	}
	return &doc, doc.Content[0], nil
}

func findKey(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// addEntry appends entry to the sequence under key, editing the YAML node
// tree so every existing comment survives. added is false when an equal scalar
// is already listed.
func addEntry(data []byte, key string, entry *yaml.Node) (out []byte, added bool, err error) {
	doc, root, err := rootMapping(data)
	if err != nil {
		return nil, false, err
	}
	seq := findKey(root, key)
	switch {
	case seq == nil:
		seq = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		root.Content = append(root.Content, scalar(key), seq)
	case seq.Kind == yaml.ScalarNode && seq.Tag == "!!null":
		seq.Kind, seq.Tag, seq.Value = yaml.SequenceNode, "!!seq", ""
	case seq.Kind != yaml.SequenceNode:
		return nil, false, fmt.Errorf("key %q is not a list", key)
	}
	if entry.Kind == yaml.ScalarNode {
		for _, n := range seq.Content {
			if n.Kind == yaml.ScalarNode && n.Value == entry.Value {
				return data, false, nil
			}
		}
	}
	// An empty `key: []` is flow style; a grown list reads better as a block.
	seq.Style &^= yaml.FlowStyle
	seq.Content = append(seq.Content, entry)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, false, err
	}
	if err := enc.Close(); err != nil {
		return nil, false, err
	}
	return buf.Bytes(), true, nil
}

// nextRuleID returns the first unused custom.rule-N id in data.
func nextRuleID(data []byte) string {
	used := map[string]bool{}
	var spec struct {
		Rules []struct {
			ID string `yaml:"id"`
		} `yaml:"rules"`
	}
	_ = yaml.Unmarshal(data, &spec)
	for _, r := range spec.Rules {
		used[r.ID] = true
	}
	for n := 1; ; n++ {
		if id := fmt.Sprintf("custom.rule-%d", n); !used[id] {
			return id
		}
	}
}
