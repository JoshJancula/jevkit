package main

import (
	"fmt"
	"os"
	"regexp"
	"regexp/syntax"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/OWNER/jevkit/internal/redact"
	"github.com/OWNER/jevkit/internal/redact/config"
)

// finding is one lint result.
type finding struct {
	kind string // duplicate, shadowed, never-matches, over-broad
	msg  string
}

// layerFile is the lint-relevant content of one loaded config file.
type layerFile struct {
	name      string
	Literals  []string `yaml:"literals"`
	EnvValues []string `yaml:"env_values"`
	NeverSend []string `yaml:"never_send"`
	Disable   []string `yaml:"disable"`
	Allowlist []struct {
		Rule    string `yaml:"rule"`
		Regex   string `yaml:"regex"`
		Literal string `yaml:"literal"`
	} `yaml:"allowlist"`
}

func (a *App) redactCheck(args []string) int {
	fs := a.newFlagSet("redact check")
	if _, code, done := parseFlags(fs, args); done {
		return code
	}
	cfg, ok := a.load("redact check")
	if !ok {
		return exitFail
	}
	a.outf("config ok (%s)\n", a.layersSummary(cfg))

	problems := 0
	for _, f := range a.lint(cfg) {
		a.outf("lint [%s]: %s\n", f.kind, f.msg)
		problems++
	}
	problems += a.runEmbeddedTests(cfg)
	if problems > 0 {
		a.outf("FAIL: %d problem(s)\n", problems)
		return exitFail
	}
	a.outf("ok: no lint findings; %d embedded test(s) passed\n", len(cfg.Tests))
	return exitOK
}

func (a *App) layersSummary(cfg *config.Config) string {
	if len(cfg.Sources) == 0 {
		return "built-in rules only; no user or project file"
	}
	var names []string
	for _, s := range cfg.Sources {
		names = append(names, a.layerName(s)+" "+s)
	}
	return "layers: built-in, " + strings.Join(names, ", ")
}

// runEmbeddedTests runs every `tests:` case and returns the failure count.
// Failures name the case and the list index, never the needle: it may be a
// secret.
func (a *App) runEmbeddedTests(cfg *config.Config) int {
	if len(cfg.Tests) == 0 {
		return 0
	}
	r, err := cfg.Redactor()
	if err != nil {
		a.outf("test: cannot build the redactor: %v\n", unwrapReason(err))
		return 1
	}
	failures := 0
	for i, tc := range cfg.Tests {
		label := fmt.Sprintf("tests[%d]", i)
		if tc.Name != "" {
			label += " " + tc.Name
		}
		label += " (" + a.layerName(tc.File) + ")"
		res, err := r.Apply(tc.Input)
		if err != nil {
			a.outf("test FAIL: %s: engine rejected the input: %v\n", label, unwrapReason(err))
			failures++
			continue
		}
		for j, s := range tc.MustNotContain {
			if strings.Contains(res.Text, s) {
				a.outf("test FAIL: %s: output still contains must_not_contain[%d]\n", label, j)
				failures++
			}
		}
		for j, s := range tc.MustContain {
			if !strings.Contains(res.Text, s) {
				a.outf("test FAIL: %s: output lacks must_contain[%d]\n", label, j)
				failures++
			}
		}
	}
	return failures
}

// lint inspects the resolved config for redundant or ineffective entries.
func (a *App) lint(cfg *config.Config) []finding {
	var files []layerFile
	for _, src := range cfg.Sources {
		data, err := os.ReadFile(src)
		if err != nil {
			continue
		}
		lf := layerFile{name: a.layerName(src)}
		if yaml.Unmarshal(data, &lf) == nil {
			files = append(files, lf)
		}
	}
	var out []finding
	out = append(out, lintDuplicates(files, cfg)...)
	out = append(out, lintRules(files, cfg)...)
	out = append(out, lintAllowlists(files)...)
	return out
}

func lintDuplicates(files []layerFile, cfg *config.Config) []finding {
	var out []finding
	dupes := func(what string, pick func(layerFile) []string, seed ...string) {
		seen := map[string]string{}
		for _, s := range seed {
			seen[s] = "the built-in list"
		}
		for _, f := range files {
			for _, v := range pick(f) {
				if prev, dup := seen[v]; dup {
					// A literal is a secret: never print it.
					shown := fmt.Sprintf("%q", v)
					if what == "literal" {
						shown = "(hidden)"
					}
					out = append(out, finding{"duplicate", fmt.Sprintf("%s %s in the %s file is already listed in %s", what, shown, f.name, prev)})
					continue
				}
				seen[v] = "the " + f.name + " file"
			}
		}
	}
	dupes("literal", func(f layerFile) []string { return f.Literals })
	dupes("env_values entry", func(f layerFile) []string { return f.EnvValues })
	dupes("never_send pattern", func(f layerFile) []string { return f.NeverSend }, config.BuiltinNeverSend()...)

	byPattern := map[string]string{}
	for _, c := range cfg.Options.Custom {
		k := c.Flags + "\x00" + c.Pattern
		if prev, dup := byPattern[k]; dup {
			out = append(out, finding{"duplicate", fmt.Sprintf("rule %s has the same pattern as %s", c.ID, prev)})
			continue
		}
		byPattern[k] = c.ID
	}
	return out
}

type compiledRule struct {
	id string
	re *regexp.Regexp
	// lit is the exact text a pattern with no metacharacters matches.
	lit   string
	isLit bool
	tree  *syntax.Regexp
}

func compileForLint(c redact.CustomRule) (compiledRule, bool) {
	pat := c.Pattern
	if c.Flags != "" {
		pat = "(?" + c.Flags + ")" + pat
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return compiledRule{}, false
	}
	cr := compiledRule{id: c.ID, re: re}
	cr.lit, cr.isLit = re.LiteralPrefix()
	if t, err := syntax.Parse(pat, syntax.Perl); err == nil {
		cr.tree = t.Simplify()
	}
	return cr, true
}

// generic strings a sane redaction rule should not match.
var broadProbes = []string{"a", "0", "x y", "hello world", "2024-01-02", "abcdefghij"}

func lintRules(files []layerFile, cfg *config.Config) []finding {
	var out []finding
	var literals []string
	for _, f := range files {
		literals = append(literals, f.Literals...)
	}
	var earlier []compiledRule
	for _, c := range cfg.Options.Custom {
		cr, ok := compileForLint(c)
		if !ok {
			continue
		}
		if cr.tree != nil && neverMatches(cr.tree) {
			out = append(out, finding{"never-matches", fmt.Sprintf("rule %s can never match: redaction is line by line and the pattern cannot match within a single line", c.ID)})
		}
		if cr.isLit && cr.lit != "" && c.Flags == "" {
			if by := shadowedBy(cr.lit, literals, earlier); by != "" {
				out = append(out, finding{"shadowed", fmt.Sprintf("rule %s never fires: its text is always redacted first by %s", c.ID, by)})
			}
		}
		if hits := broadHits(cr.re); hits >= 4 || broadOnSingleChar(cr.re) {
			out = append(out, finding{"over-broad", fmt.Sprintf("rule %s matches ordinary text (%s); it would redact most of every output", c.ID, broadExample(cr.re))})
		}
		earlier = append(earlier, cr)
	}
	return out
}

// shadowedBy reports what redacts lit before a rule for lit gets a chance:
// literals run first, then earlier custom rules in order.
func shadowedBy(lit string, literals []string, earlier []compiledRule) string {
	for _, l := range literals {
		if strings.TrimSpace(l) != "" && strings.Contains(lit, strings.TrimSpace(l)) {
			return "a literal in `literals`"
		}
	}
	for _, e := range earlier {
		if e.re.MatchString(lit) {
			return "rule " + e.id
		}
	}
	return ""
}

func broadHits(re *regexp.Regexp) int {
	n := 0
	for _, p := range broadProbes {
		if re.MatchString(p) {
			n++
		}
	}
	return n
}

func broadOnSingleChar(re *regexp.Regexp) bool {
	return re.MatchString("a") || re.MatchString("0")
}

func broadExample(re *regexp.Regexp) string {
	for _, p := range broadProbes {
		if re.MatchString(p) {
			return fmt.Sprintf("it matches %q", p)
		}
	}
	return "it matches generic text"
}

// neverMatches reports whether no single line can satisfy re.
func neverMatches(re *syntax.Regexp) bool {
	switch re.Op {
	case syntax.OpNoMatch:
		return true
	case syntax.OpLiteral:
		return slices.Contains(re.Rune, '\n')
	case syntax.OpCharClass:
		return len(re.Rune) == 2 && re.Rune[0] == '\n' && re.Rune[1] == '\n'
	case syntax.OpConcat:
		return slices.ContainsFunc(re.Sub, neverMatches)
	case syntax.OpAlternate:
		return len(re.Sub) > 0 && !slices.ContainsFunc(re.Sub, func(s *syntax.Regexp) bool { return !neverMatches(s) })
	case syntax.OpCapture, syntax.OpPlus:
		return neverMatches(re.Sub[0])
	case syntax.OpRepeat:
		return re.Min >= 1 && neverMatches(re.Sub[0])
	}
	return false
}

// lintAllowlists flags allowlist entries that can never apply.
func lintAllowlists(files []layerFile) []finding {
	var out []finding
	disabled := map[string]bool{}
	for _, f := range files {
		for _, d := range f.Disable {
			disabled[d] = true
		}
	}
	seen := map[string]bool{}
	for _, f := range files {
		for i, al := range f.Allowlist {
			if al.Rule != "" && disabled[al.Rule] {
				out = append(out, finding{"never-matches", fmt.Sprintf("allowlist[%d] targets %s, which is disabled, so it never applies", i, al.Rule)})
			}
			k := al.Rule + "\x00" + al.Regex + "\x00" + al.Literal
			if seen[k] {
				out = append(out, finding{"duplicate", fmt.Sprintf("allowlist[%d] repeats an earlier allowlist entry", i)})
			}
			seen[k] = true
		}
	}
	return out
}
