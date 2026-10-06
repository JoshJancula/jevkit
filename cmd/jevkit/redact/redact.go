package redact

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/redact"
	"github.com/JoshJancula/jevkit/internal/redact/config"
	"github.com/spf13/cobra"
)

const RedactUsage = `Inspect and tune what jevkit removes before data leaves your machine.

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
		a.Errf("%s", a.HelpText(a.Stderr, RedactUsage))
		return app.ExitUsage
	}
	if args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		a.Outf("%s", a.HelpText(a.Stdout, RedactUsage))
		return app.ExitOK
	}
	handlers := map[string]func([]string) int{
		"init": a.redactInit, "list": a.redactList, "explain": a.redactExplain,
		"test": a.redactTest, "add": a.redactAdd, "remove": a.redactRemove,
		"check": a.redactCheck, "audit": a.redactAudit, "last": a.redactLast,
	}
	handler, ok := handlers[args[0]]
	if !ok {
		a.Errf("jevkit redact: unknown subcommand %q\n\n%s", args[0], a.HelpText(a.Stderr, RedactUsage))
		return app.ExitUsage
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
	if code != app.ExitOK {
		a.Errf("%s", captured.String())
		return code
	}
	info := redactHelp[name]
	a.Outf("%s\n\n%s\n  %s\n", info.summary, a.Styled(a.Stdout, app.ANSICyan, "Usage:"), info.usage)
	_, flags, _ := strings.Cut(captured.String(), "\n")
	if strings.TrimSpace(flags) != "" {
		a.Outf("\n%s\n%s", a.Styled(a.Stdout, app.ANSICyan, "Flags:"), flags)
	}
	return app.ExitOK
}

func (a *App) redactInit(args []string) int {
	fs := a.NewFlagSet("redact init")
	project := fs.Bool("project", false, "write .jevkit/redact.yaml in the current directory")
	force := fs.Bool("force", false, "overwrite an existing file")
	pos, code, done := app.ParseFlags(fs, args)
	if done {
		return code
	}
	if len(pos) > 0 {
		a.Errf("jevkit redact init: unexpected argument %q\n", pos[0])
		return app.ExitUsage
	}
	path, err := a.Target(*project)
	if err != nil {
		a.Errf("jevkit redact init: %v\n", err)
		return app.ExitFail
	}
	if _, err := os.Lstat(path); err == nil && !*force {
		a.Errf("jevkit redact init: %s already exists (use --force to overwrite it)\n", path)
		return app.ExitFail
	}
	dirMode, body := os.FileMode(0o700), starterUser
	if *project {
		dirMode, body = 0o755, starterProject
	}
	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
		a.Errf("jevkit redact init: %v\n", err)
		return app.ExitFail
	}
	if err := app.WriteAtomic(path, []byte(body), 0o600); err != nil {
		a.Errf("jevkit redact init: %v\n", err)
		return app.ExitFail
	}
	a.Outf("wrote %s\nnext: edit it, then run `jevkit redact check`\n", path)
	return app.ExitOK
}

func (a *App) redactList(args []string) int {
	fs := a.NewFlagSet("redact list")
	if _, code, done := app.ParseFlags(fs, args); done {
		return code
	}
	cfg, ok := a.LoadConfig("redact list")
	if !ok {
		return app.ExitFail
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
		rows = append(rows, []string{c.ID, fmt.Sprint(redact.Hard), a.LayerName(cfg.RuleSource[c.ID]), "enabled"})
	}
	a.Heading("Rules")
	a.Table([]string{"ID", "CLASS", "SOURCE", "STATE"}, rows)
	mode := "standard"
	if cfg.Options.Strict {
		mode = "strict"
	}
	a.Outf("\nmode: %s\n", mode)
	return app.ExitOK
}

func (a *App) redactExplain(args []string) int {
	fs := a.NewFlagSet("redact explain")
	pos, code, done := app.ParseFlags(fs, args)
	if done {
		return code
	}
	if len(pos) != 1 {
		a.Errf("usage: jevkit redact explain <rule-id>\n")
		return app.ExitUsage
	}
	cfg, ok := a.LoadConfig("redact explain")
	if !ok {
		return app.ExitFail
	}
	id := pos[0]
	for _, c := range cfg.Options.Custom {
		if c.ID == id {
			a.explainCustom(cfg, c)
			return app.ExitOK
		}
	}
	info, found := redact.Describe(id)
	if !found {
		info, found = redact.Describe("builtin." + id)
	}
	if !found {
		a.Errf("jevkit redact explain: unknown rule %q (see `jevkit redact list`)\n", id)
		return app.ExitFail
	}
	state := "enabled"
	if slices.Contains(cfg.Options.DisableSoft, info.ID) {
		state = "disabled"
	}
	a.Outf("rule:    %s\nclass:   %s\nsource:  builtin\nstate:   %s\npattern: %s\n\n%s\n\n", info.ID, info.Class, state, info.Pattern, info.Summary)
	a.Outf("%s", tuneText(info))
	return app.ExitOK
}

func (a *App) explainCustom(cfg *config.Config, c redact.CustomRule) {
	src := cfg.RuleSource[c.ID]
	a.Outf("rule:    %s\nclass:   HARD\nsource:  %s (%s)\npattern: %s\n", c.ID, a.LayerName(src), src, c.Pattern)
	if c.Flags != "" {
		a.Outf("flags:   %s\n", c.Flags)
	}
	if c.Replacement != "" {
		a.Outf("replaces with: %s\n", c.Replacement)
	}
	a.Outf("\nHOW TO TUNE: custom rules are HARD and cannot be disabled or allowlisted.\nEdit or delete the rule under `rules:` in %s, then run `jevkit redact check`.\n", src)
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
	fs := a.NewFlagSet("redact test")
	diff := fs.Bool("diff", false, "print a unified diff instead of the redacted text")
	pos, code, done := app.ParseFlags(fs, args)
	if done {
		return code
	}
	if len(pos) > 1 {
		a.Errf("usage: jevkit redact test [--diff] [file|-]\n")
		return app.ExitUsage
	}
	in, name := a.Stdin, "stdin"
	if len(pos) == 1 && pos[0] != "-" {
		f, err := os.Open(pos[0])
		if err != nil {
			a.Errf("jevkit redact test: %v\n", err)
			return app.ExitFail
		}
		defer func() { _ = f.Close() }()
		in, name = f, pos[0]
	}
	data, err := io.ReadAll(io.LimitReader(in, app.MaxTestInput+1))
	if err != nil {
		a.Errf("jevkit redact test: %v\n", err)
		return app.ExitFail
	}
	if len(data) > app.MaxTestInput {
		a.Errf("jevkit redact test: input larger than %d bytes\n", app.MaxTestInput)
		return app.ExitFail
	}
	cfg, ok := a.LoadConfig("redact test")
	if !ok {
		return app.ExitFail
	}
	r, err := cfg.Redactor()
	if err != nil {
		a.Errf("jevkit redact test: %v\n", app.UnwrapReason(err))
		return app.ExitFail
	}
	res, err := r.Apply(string(data))
	if err != nil {
		a.Errf("jevkit redact test: %v\n", app.UnwrapReason(err))
		return app.ExitFail
	}
	if *diff {
		a.Outf("%s", app.UnifiedDiff(name, "redacted", string(data), res.Text))
	} else {
		a.Outf("%s", res.Text)
	}
	// The hit table goes to stderr so stdout stays a clean, pipeable result.
	if len(res.Hits) == 0 {
		a.Errf("no rules matched\n")
		return app.ExitOK
	}
	rows := make([][]string, 0, len(res.Hits))
	for _, h := range res.Hits {
		rows = append(rows, []string{h.RuleID, fmt.Sprintf("%d", h.Count)})
	}
	a.Errf("Redaction matches\n")
	app.WriteTable(a.Stderr, []string{"RULE", "HITS"}, rows)
	return app.ExitOK
}

// redactCmd hands everything after "redact" to the redact subcommand
// dispatcher, which owns its own flag parsing.
func (a *App) redactCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "redact",
		Short:              `inspect and fine-tune redaction (run "jevkit redact help" for subcommands)`,
		DisableFlagParsing: true,
		RunE: func(_ *cobra.Command, args []string) error {
			return app.CodeErr(a.redact(args))
		},
	}
}
