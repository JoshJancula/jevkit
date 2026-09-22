package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/OWNER/jevkit/internal/redact"
	"github.com/OWNER/jevkit/internal/redact/config"
)

// maxTestInput bounds what `redact test` reads.
const maxTestInput = 8 << 20

const redactUsage = `usage: jevkit redact <subcommand>

subcommands:
  init [--project] [--force]            write a commented starter redact.yaml
  list                                  list rules: id, class, source, state
  explain <rule-id>                     show a rule's pattern and how to tune it
  test [--diff] [file|-]                redact a file or stdin locally (no network)
  add --pattern|--literal|--env|--never-send <value>   [--project]
                                        add an entry, keeping comments
  check                                 validate, lint and run embedded tests
  audit [--since <when>] [--format json]
                                        summarise what was sent (counts only)
  last [n]                              show the exact payloads of the last n sends
                                        (needs review mode)
`

func (a *App) redact(args []string) int {
	if len(args) == 0 {
		a.errf("%s", redactUsage)
		return exitUsage
	}
	rest := args[1:]
	switch args[0] {
	case "init":
		return a.redactInit(rest)
	case "list":
		return a.redactList(rest)
	case "explain":
		return a.redactExplain(rest)
	case "test":
		return a.redactTest(rest)
	case "add":
		return a.redactAdd(rest)
	case "check":
		return a.redactCheck(rest)
	case "audit":
		return a.redactAudit(rest)
	case "last":
		return a.redactLast(rest)
	case "help", "-h", "--help":
		a.outf("%s", redactUsage)
		return exitOK
	}
	a.errf("jevkit redact: unknown subcommand %q\n\n%s", args[0], redactUsage)
	return exitUsage
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
	tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tCLASS\tSOURCE\tSTATE")
	for _, r := range redact.Rules() {
		state := "enabled"
		if slices.Contains(disabled, r.ID) {
			state = "disabled"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\tbuiltin\t%s\n", r.ID, r.Class, state)
	}
	for _, c := range cfg.Options.Custom {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\tenabled\n", c.ID, redact.Hard, a.layerName(cfg.RuleSource[c.ID]))
	}
	_ = tw.Flush()
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
	tw := tabwriter.NewWriter(a.Stderr, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "RULE\tHITS")
	for _, h := range res.Hits {
		_, _ = fmt.Fprintf(tw, "%s\t%d\n", h.RuleID, h.Count)
	}
	_ = tw.Flush()
	return exitOK
}
