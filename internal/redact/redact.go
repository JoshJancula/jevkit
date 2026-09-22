package redact

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/OWNER/jevkit/internal/jev"
)

// Options configures a Redactor. Only SOFT rules are tunable.
type Options struct {
	// Key is the live API key value; it is always redacted (HARD).
	Key string
	// Secrets are further known secret values (env-var values) that must not
	// survive redaction (HARD).
	Secrets []string
	// Home is the home directory rewritten to "~" (SOFT).
	Home string
	// DisableSoft lists SOFT rule ids to turn off.
	DisableSoft []string
	// Allow maps a SOFT rule id to RE2 patterns; a match of the rule whose text
	// matches any pattern is left in place.
	Allow map[string][]string
	// Custom are extra regexp rules. They are HARD: they cannot be disabled or
	// allowlisted.
	Custom []CustomRule
	// Placeholder overrides the "[REDACTED]" replacement; nil keeps it.
	Placeholder Placeholder
	// EntropyThreshold (bits/char) and MinTokenLen tune the SOFT high-entropy
	// rule; zero keeps DefaultEntropyThreshold and DefaultMinTokenLen.
	EntropyThreshold float64
	MinTokenLen      int
	// Strict redacts every SOFT rule and lowers the entropy threshold and
	// minimum token length to StrictEntropyThreshold and StrictMinTokenLen
	// (never raising them). It cannot be combined with DisableSoft or Allow.
	Strict bool
}

// Limits on custom rules.
const (
	MaxPatternLen     = 1024
	MaxReplacementLen = 256
	maxRuleIDLen      = 128
)

// CustomRule is a user-defined redaction pattern (Go RE2).
type CustomRule struct {
	ID      string
	Pattern string
	// Flags is any of "i", "m", "s".
	Flags string
	// Replacement, when set, replaces each match verbatim; otherwise the
	// configured placeholder is used.
	Replacement string
}

var customIDRe = regexp.MustCompile(`^[a-z][a-z0-9_-]*(?:\.[a-z0-9_-]+)*$`)

// CustomRuleError says which field of a CustomRule is unacceptable: "id",
// "pattern", "flags" or "replacement".
type CustomRuleError struct {
	Field string
	Msg   string
}

func (e *CustomRuleError) Error() string { return e.Field + ": " + e.Msg }

func badField(field, format string, args ...any) error {
	return &CustomRuleError{Field: field, Msg: fmt.Sprintf(format, args...)}
}

// compileCustom validates and compiles a custom rule. Failures are
// *CustomRuleError.
func compileCustom(c CustomRule) (*regexp.Regexp, error) {
	switch {
	case len(c.ID) > maxRuleIDLen || !customIDRe.MatchString(c.ID):
		return nil, badField("id", "must be lowercase letters, digits, '.', '_' or '-'")
	case strings.HasPrefix(c.ID, "builtin."):
		return nil, badField("id", "must not use the reserved builtin. prefix")
	case c.Pattern == "":
		return nil, badField("pattern", "is empty")
	case len(c.Pattern) > MaxPatternLen:
		return nil, badField("pattern", "is longer than %d bytes", MaxPatternLen)
	case len(c.Replacement) > MaxReplacementLen || strings.ContainsAny(c.Replacement, "\r\n"):
		return nil, badField("replacement", "must be one line of at most %d bytes", MaxReplacementLen)
	}
	flags := ""
	for _, f := range c.Flags {
		if !strings.ContainsRune("ims", f) || strings.ContainsRune(flags, f) {
			return nil, badField("flags", "%q: only i, m and s are allowed, once each", c.Flags)
		}
		flags += string(f)
	}
	pat := c.Pattern
	if flags != "" {
		pat = "(?" + flags + ")" + pat
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return nil, badField("pattern", "invalid regexp: %v", err)
	}
	// A pattern that matches nothing-width would rewrite every line.
	for _, probe := range []string{"", "a", "a b", " a "} {
		for _, loc := range re.FindAllStringIndex(probe, -1) {
			if loc[0] == loc[1] {
				return nil, badField("pattern", "matches the empty string")
			}
		}
	}
	return re, nil
}

// ValidateCustomRule reports why c is not an acceptable custom rule.
func ValidateCustomRule(c CustomRule) error {
	_, err := compileCustom(c)
	return err
}

// Hit reports that a rule fired; it never carries matched content.
type Hit struct {
	RuleID string
	Count  int
}

// Result is the redacted text and the rules that fired, in rule order.
type Result struct {
	Text string
	Hits []Hit
}

// Redactor applies the built-in rules. It is safe for concurrent use.
type Redactor struct {
	rules   []rule
	secrets []string
	mark    Placeholder
	// verifyHook lets tests force a verification failure.
	verifyHook func(text string) error
}

// New builds a Redactor. It fails if Options tries to disable or allowlist a
// HARD rule, or names an unknown rule.
func New(opts Options) (*Redactor, error) {
	classes := map[string]Class{}
	for _, r := range Rules() {
		classes[r.ID] = r.Class
	}
	check := func(id string, allow bool) error {
		c, ok := classes[id]
		switch {
		case !ok:
			return fmt.Errorf("redact: unknown rule %q", id)
		case c == Hard:
			return fmt.Errorf("redact: rule %s is HARD and cannot be disabled or allowlisted", id)
		case allow && id == RuleHomePath:
			return fmt.Errorf("redact: rule %s takes no allowlist", id)
		}
		return nil
	}
	disabled := map[string]bool{}
	for _, id := range opts.DisableSoft {
		if err := check(id, false); err != nil {
			return nil, err
		}
		disabled[id] = true
	}
	allow := map[string][]*regexp.Regexp{}
	for id, pats := range opts.Allow {
		if err := check(id, true); err != nil {
			return nil, err
		}
		for _, p := range pats {
			re, err := regexp.Compile(p)
			if err != nil {
				return nil, fmt.Errorf("redact: allow pattern for %s: %w", id, err)
			}
			allow[id] = append(allow[id], re)
		}
	}

	if opts.EntropyThreshold < 0 || opts.MinTokenLen < 0 {
		return nil, errors.New("redact: tuning values must not be negative")
	}
	if opts.Strict && (len(opts.DisableSoft) > 0 || len(opts.Allow) > 0) {
		return nil, errors.New("redact: strict mode redacts all SOFT rules and takes no disable or allowlist")
	}
	mark := markerFor(opts.Placeholder)
	var custom []rule
	seen := map[string]bool{}
	for _, c := range opts.Custom {
		re, err := compileCustom(c)
		if err != nil {
			return nil, fmt.Errorf("redact: custom rule %q: %w", c.ID, err)
		}
		if seen[c.ID] {
			return nil, fmt.Errorf("redact: duplicate custom rule %q", c.ID)
		}
		seen[c.ID] = true
		id, repl := c.ID, c.Replacement
		custom = append(custom, regexRule(id, Hard, re, nil, func(m string) string {
			if repl != "" {
				return repl
			}
			return mark(id, m)
		}, nil))
	}

	secrets := secretPieces(append([]string{opts.Key}, opts.Secrets...))
	threshold, minLen := opts.EntropyThreshold, opts.MinTokenLen
	if opts.Strict {
		if threshold == 0 || threshold > StrictEntropyThreshold {
			threshold = StrictEntropyThreshold
		}
		if minLen == 0 || minLen > StrictMinTokenLen {
			minLen = StrictMinTokenLen
		}
	}
	t := tuning{mark: mark, threshold: threshold, minLen: minLen, custom: custom}
	var rules []rule
	for _, r := range builtinRules(secrets, opts.Home, allow, t) {
		if !disabled[r.id] {
			rules = append(rules, r)
		}
	}
	return &Redactor{rules: rules, secrets: secrets, mark: mark}, nil
}

// secretPieces splits secrets on newlines (redaction is per line), drops
// empties, dedupes, and orders longest first so a longer secret wins over one
// it contains.
func secretPieces(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		for _, p := range strings.Split(s, "\n") {
			p = strings.TrimSpace(p)
			if p != "" && !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

// Rules lists the built-in rule ids and classes, in application order.
func Rules() []Rule {
	out := []Rule{{RuleKnownSecret, Hard}, {RulePrivateKey, Hard}}
	for _, r := range builtinRules(nil, "/h", nil, tuning{})[1:] {
		out = append(out, Rule{r.id, r.class})
	}
	return out
}

func reject(err error) error {
	return &jev.Error{Code: jev.CodeRejected, Reason: "redaction failed", Err: err}
}

// Apply redacts text. Any failure, including a secret surviving the
// verification pass or a changed line count, returns a *jev.Error with
// jev.CodeRejected and no text.
func (r *Redactor) Apply(text string) (res Result, err error) {
	defer func() {
		if p := recover(); p != nil {
			// The panic value is dropped: it could echo input.
			res, err = Result{}, reject(errors.New("internal error"))
		}
	}()
	out, hits, err := r.run(text)
	if err != nil {
		return Result{}, reject(err)
	}
	if err := r.verify(text, out); err != nil {
		return Result{}, reject(err)
	}
	return Result{Text: out, Hits: hits}, nil
}

func (r *Redactor) run(text string) (string, []Hit, error) {
	lines := strings.Split(text, "\n")
	counts := map[string]int{}
	counts[RulePrivateKey] = redactPrivateKeys(lines, r.mark(RulePrivateKey, ""))
	for i, line := range lines {
		for _, ru := range r.rules {
			s, n, err := ru.line(line)
			if err != nil {
				return "", nil, fmt.Errorf("rule %s: %w", ru.id, err)
			}
			if n > 0 {
				counts[ru.id] += n
				line = s
			}
		}
		lines[i] = line
	}
	// Hits follow application order: known secrets, private keys, then rules.
	var hits []Hit
	order := []string{RuleKnownSecret, RulePrivateKey}
	for _, ru := range r.rules {
		order = append(order, ru.id)
	}
	done := map[string]bool{}
	for _, id := range order {
		if n := counts[id]; n > 0 && !done[id] {
			done[id] = true
			hits = append(hits, Hit{RuleID: id, Count: n})
		}
	}
	return strings.Join(lines, "\n"), hits, nil
}

func (r *Redactor) verify(in, out string) error {
	if strings.Count(in, "\n") != strings.Count(out, "\n") {
		return errors.New("line count changed")
	}
	for _, s := range r.secrets {
		if strings.Contains(out, s) {
			return errors.New("known secret survived redaction")
		}
	}
	if pemBegin.MatchString(out) {
		return errors.New("private key block survived redaction")
	}
	if r.verifyHook != nil {
		return r.verifyHook(out)
	}
	return nil
}

// redactPrivateKeys replaces PEM private-key blocks in place, line by line: a
// line inside a block becomes the marker, so line count is preserved. An
// unterminated block runs to the end of the text (fail closed). It returns the
// number of blocks found.
func redactPrivateKeys(lines []string, marker string) int {
	blocks := 0
	in := false
	for i, line := range lines {
		for {
			if in {
				loc := pemEnd.FindStringIndex(line)
				if loc == nil {
					line = marker
					break
				}
				line = marker + line[loc[1]:]
				in = false
				continue
			}
			loc := pemBegin.FindStringIndex(line)
			if loc == nil {
				break
			}
			blocks++
			line = line[:loc[0]] + marker + line[loc[1]:]
			in = true
			// Re-scan the remainder for an END on the same line.
			head := line[:loc[0]+len(marker)]
			rest := line[loc[0]+len(marker):]
			if end := pemEnd.FindStringIndex(rest); end != nil {
				line = head + rest[end[1]:]
				in = false
				continue
			}
			line = head
			break
		}
		lines[i] = line
	}
	return blocks
}

// secretEnvName matches environment variable names that hold credentials.
var secretEnvName = regexp.MustCompile(`(?i)(?:KEY|TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIAL|AUTH)`)

// minEnvSecretLen keeps short values such as "1" or "true" from being treated
// as secrets.
const minEnvSecretLen = 8

// OptionsFromEnv builds Options from an environment (KEY=value entries): HOME,
// TYPESAFE_API_KEY, and the values of credential-looking variables.
func OptionsFromEnv(environ []string) Options {
	var o Options
	for _, kv := range environ {
		name, val, ok := strings.Cut(kv, "=")
		if !ok || val == "" {
			continue
		}
		switch {
		case name == "HOME":
			o.Home = val
		case name == "TYPESAFE_API_KEY":
			o.Key = val
		case secretEnvName.MatchString(name) && len(val) >= minEnvSecretLen:
			o.Secrets = append(o.Secrets, val)
		}
	}
	return o
}
