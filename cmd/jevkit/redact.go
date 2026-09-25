package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/OWNER/jevkit/internal/redact"
	"github.com/OWNER/jevkit/internal/redact/config"
)

// maxTestInput bounds what `redact test` reads.
const maxTestInput = 8 << 20

const redactUsage = `Inspect and tune what jevkit removes before data leaves your machine.

Usage:
  jevkit redact <command>

Commands:
  init [--project] [--force]              Create a commented redact.yaml starter.
  list                                    List rules, source layers, and state.
  explain <rule-id>                       Show a rule's pattern and tuning options.
  test [--diff] [file|-]                  Redact a file or stdin locally (no network).
  add --pattern|--literal|--env|--never-send <value> [--project]
                                          Add an entry while retaining comments.
  remove --rule|--literal|--env|--never-send <value> [--project]
                                          Remove one entry after full validation.
  check                                   Validate, lint, and run embedded tests.
  audit [--since <when>] [--format json] Summarize sent-data counts only.
  last [n]                                Show stored payloads (review mode required).

Examples:
  jevkit redact list
  printf 'token=example' | jevkit redact test -
  jevkit redact add --literal 'internal-project-name'
`

func (a *App) redact(args []string) int {
	if len(args) == 0 {
		a.errf("%s", a.helpText(a.Stderr, redactUsage))
		return exitUsage
	}
	if args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		a.outf("%s", a.helpText(a.Stdout, redactUsage))
		return exitOK
	}
	handlers := map[string]func([]string) int{
		"init": a.redactInit, "list": a.redactList, "explain": a.redactExplain,
		"test": a.redactTest, "add": a.redactAdd, "remove": a.redactRemove,
		"check": a.redactCheck, "audit": a.redactAudit, "last": a.redactLast,
	}
	handler, ok := handlers[args[0]]
	if !ok {
		a.errf("jevkit redact: unknown subcommand %q\n\n%s", args[0], a.helpText(a.Stderr, redactUsage))
		return exitUsage
	}
	if len(args) > 1 && (args[1] == "--help" || args[1] == "-h") {
		return a.redactSubHelp(args[0], handler)
	}
	return handler(args[1:])
}

var redactHelp = map[string]struct{ usage, summary string }{
	"init":    {"jevkit redact init [--project] [--force]", "Create a commented redaction config."},
	"list":    {"jevkit redact list", "Show rules, their source, and whether they are enabled."},
	"explain": {"jevkit redact explain <rule-id>", "Show a rule's pattern and tuning options."},
	"test":    {"jevkit redact test [--diff] [file|-]", "Redact a file or stdin locally."},
	"add":     {"jevkit redact add --pattern|--literal|--env|--never-send <value> [--project]", "Add a rule while retaining config comments."},
	"remove":  {"jevkit redact remove --rule|--literal|--env|--never-send <value> [--project]", "Remove a rule after validation."},
	"check":   {"jevkit redact check", "Validate and lint the redaction config."},
	"audit":   {"jevkit redact audit [--since <when>] [--format json]", "Summarize sent-data counts."},
	"last":    {"jevkit redact last [n]", "Show stored payloads when review mode is enabled."},
}

func (a *App) redactSubHelp(name string, handler func([]string) int) int {
	var captured bytes.Buffer
	previous := a.Stderr
	a.Stderr = &captured
	code := handler([]string{"--help"})
	a.Stderr = previous
	if code != exitOK {
		a.errf("%s", captured.String())
		return code
	}
	info := redactHelp[name]
	a.outf("%s\n\n%s\n  %s\n", info.summary, a.styled(a.Stdout, ansiCyan, "Usage:"), info.usage)
	_, flags, _ := strings.Cut(captured.String(), "\n")
	if strings.TrimSpace(flags) != "" {
		a.outf("\n%s\n%s", a.styled(a.Stdout, ansiCyan, "Flags:"), flags)
	}
	return exitOK
}

func (a *App) redactInit(args []string) int {
	fs := a.newFlagSet("redact init")
	project := fs.Bool("project", false, "write .jevkit/redact.yaml in the current directory")
	force := fs.Bool("force", false, "overwrite an existing file")
	pos, code, done := parseFlags(fs, args)
	if done {
		return code
	}
	if len(pos) > 0 {
		a.errf("jevkit redact init: unexpected argument %q\n", pos[0])
		return exitUsage
	}
	path, err := a.target(*project)
	if err != nil {
		a.errf("jevkit redact init: %v\n", err)
		return exitFail
	}
	if _, err := os.Lstat(path); err == nil && !*force {
		a.errf("jevkit redact init: %s already exists (use --force to overwrite it)\n", path)
		return exitFail
	}
	dirMode, body := os.FileMode(0o700), starterUser
	if *project {
		dirMode, body = 0o755, starterProject
	}
	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
		a.errf("jevkit redact init: %v\n", err)
		return exitFail
	}
	if err := writeAtomic(path, []byte(body), 0o600); err != nil {
		a.errf("jevkit redact init: %v\n", err)
		return exitFail
	}
	a.outf("wrote %s\nnext: edit it, then run `jevkit redact check`\n", path)
	return exitOK
}

// load resolves the layered config, reporting a failure to stderr.
func (a *App) load(cmd string) (*config.Config, bool) {
	cfg, err := config.Load(a.loadOptions())
	if err != nil {
		a.errf("jevkit %s: %v\n", cmd, unwrapReason(err))
		return nil, false
	}
	return cfg, true
}

// unwrapReason prefers the wrapped config error, which names file and key.
func unwrapReason(err error) error {
	var ce *config.Error
	if errors.As(err, &ce) {
		return ce
	}
	return err
}

func (a *App) redactList(args []string) int {
	fs := a.newFlagSet("redact list")
	if _, code, done := parseFlags(fs, args); done {
		return code
	}
	cfg, ok := a.load("redact list")
	if !ok {
		return exitFail
	}
	disabled := cfg.Options.DisableSoft
	rows := make([][]string, 0, len(redact.Rules())+len(cfg.Options.Custom))
	for _, r := range redact.Rules() {
		state := "enabled"
		if slices.Contains(disabled, r.ID) {
			state = "disabled"
		}
		rows = append(rows, []string{r.ID, fmt.Sprint(r.Class), "builtin", state})
	}
	for _, c := range cfg.Options.Custom {
		rows = append(rows, []string{c.ID, fmt.Sprint(redact.Hard), a.layerName(cfg.RuleSource[c.ID]), "enabled"})
	}
	a.heading("Rules")
	a.table([]string{"ID", "CLASS", "SOURCE", "STATE"}, rows)
	mode := "standard"
	if cfg.Options.Strict {
		mode = "strict"
	}
	a.outf("\nmode: %s\n", mode)
	return exitOK
}

func (a *App) redactExplain(args []string) int {
	fs := a.newFlagSet("redact explain")
	pos, code, done := parseFlags(fs, args)
	if done {
		return code
	}
	if len(pos) != 1 {
		a.errf("usage: jevkit redact explain <rule-id>\n")
		return exitUsage
	}
	cfg, ok := a.load("redact explain")
	if !ok {
		return exitFail
	}
	id := pos[0]
	for _, c := range cfg.Options.Custom {
		if c.ID == id {
			a.explainCustom(cfg, c)
			return exitOK
		}
	}
	info, found := redact.Describe(id)
	if !found {
		info, found = redact.Describe("builtin." + id)
	}
	if !found {
		a.errf("jevkit redact explain: unknown rule %q (see `jevkit redact list`)\n", id)
		return exitFail
	}
	state := "enabled"
	if slices.Contains(cfg.Options.DisableSoft, info.ID) {
		state = "disabled"
	}
	a.outf("rule:    %s\nclass:   %s\nsource:  builtin\nstate:   %s\npattern: %s\n\n%s\n\n", info.ID, info.Class, state, info.Pattern, info.Summary)
	a.outf("%s", tuneText(info))
	return exitOK
}

func (a *App) explainCustom(cfg *config.Config, c redact.CustomRule) {
	src := cfg.RuleSource[c.ID]
	a.outf("rule:    %s\nclass:   HARD\nsource:  %s (%s)\npattern: %s\n", c.ID, a.layerName(src), src, c.Pattern)
	if c.Flags != "" {
		a.outf("flags:   %s\n", c.Flags)
	}
	if c.Replacement != "" {
		a.outf("replaces with: %s\n", c.Replacement)
	}
	a.outf("\nHOW TO TUNE: custom rules are HARD and cannot be disabled or allowlisted.\nEdit or delete the rule under `rules:` in %s, then run `jevkit redact check`.\n", src)
}

func tuneText(info redact.RuleInfo) string {
	if info.Class == redact.Hard {
		return "HOW TO TUNE: this rule is HARD. It cannot be disabled or allowlisted, by design.\nTo redact more, add your own rule: jevkit redact add --pattern '<regex>'\n"
	}
	var b strings.Builder
	b.WriteString("HOW TO TUNE (user redact.yaml only; project files cannot loosen redaction):\n")
	fmt.Fprintf(&b, "  disable it:\n    disable: [%q]\n", info.ID)
	if info.ID != redact.RuleHomePath {
		fmt.Fprintf(&b, "  allow known-safe text (a regex, or a literal):\n    allowlist:\n      - rule: %s\n        regex: '^example$'\n", info.ID)
	}
	switch info.ID {
	case redact.RuleHighEntropy:
		b.WriteString("  tune the detector:\n    tuning:\n      entropy_threshold: 4.3\n      min_token_length: 40\n")
	case redact.RuleHomePath, redact.RuleUserPath:
		b.WriteString("  keep paths as they are:\n    tuning:\n      path_handling: keep\n")
	}
	b.WriteString("Strict mode (`mode: strict`) redacts every SOFT rule and conflicts with all of the above.\n")
	return b.String()
}

func (a *App) redactTest(args []string) int {
	fs := a.newFlagSet("redact test")
	diff := fs.Bool("diff", false, "print a unified diff instead of the redacted text")
	pos, code, done := parseFlags(fs, args)
	if done {
		return code
	}
	if len(pos) > 1 {
		a.errf("usage: jevkit redact test [--diff] [file|-]\n")
		return exitUsage
	}
	in, name := a.Stdin, "stdin"
	if len(pos) == 1 && pos[0] != "-" {
		f, err := os.Open(pos[0])
		if err != nil {
			a.errf("jevkit redact test: %v\n", err)
			return exitFail
		}
		defer func() { _ = f.Close() }()
		in, name = f, pos[0]
	}
	data, err := io.ReadAll(io.LimitReader(in, maxTestInput+1))
	if err != nil {
		a.errf("jevkit redact test: %v\n", err)
		return exitFail
	}
	if len(data) > maxTestInput {
		a.errf("jevkit redact test: input larger than %d bytes\n", maxTestInput)
		return exitFail
	}
	cfg, ok := a.load("redact test")
	if !ok {
		return exitFail
	}
	r, err := cfg.Redactor()
	if err != nil {
		a.errf("jevkit redact test: %v\n", unwrapReason(err))
		return exitFail
	}
	res, err := r.Apply(string(data))
	if err != nil {
		a.errf("jevkit redact test: %v\n", unwrapReason(err))
		return exitFail
	}
	if *diff {
		a.outf("%s", unifiedDiff(name, "redacted", string(data), res.Text))
	} else {
		a.outf("%s", res.Text)
	}
	// The hit table goes to stderr so stdout stays a clean, pipeable result.
	if len(res.Hits) == 0 {
		a.errf("no rules matched\n")
		return exitOK
	}
	rows := make([][]string, 0, len(res.Hits))
	for _, h := range res.Hits {
		rows = append(rows, []string{h.RuleID, fmt.Sprintf("%d", h.Count)})
	}
	a.errf("Redaction matches\n")
	writeTable(a.Stderr, []string{"RULE", "HITS"}, rows)
	return exitOK
}
