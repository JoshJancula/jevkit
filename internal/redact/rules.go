package redact

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

// Class says whether a rule can be tuned.
type Class int

const (
	// Hard rules cannot be disabled or allowlisted.
	Hard Class = iota
	// Soft rules can be disabled or allowlisted through Options.
	Soft
)

func (c Class) String() string {
	if c == Hard {
		return "HARD"
	}
	return "SOFT"
}

// Marker replaces redacted content unless a Placeholder is configured.
const Marker = "[REDACTED]"

// Placeholder builds the replacement for content matched by rule ruleID. It
// must be deterministic, must not include matched content, and must not
// contain a newline.
type Placeholder func(ruleID, matched string) string

// markerFor returns the default marker when p is nil.
func markerFor(p Placeholder) Placeholder {
	if p == nil {
		return func(string, string) string { return Marker }
	}
	return p
}

// Stable rule ids.
const (
	RuleKnownSecret   = "builtin.known-secret"
	RulePrivateKey    = "builtin.private-key-block"
	RuleAuthHeader    = "builtin.authorization-header"
	RuleBearerToken   = "builtin.bearer-token"
	RuleOpenAIKey     = "builtin.openai-key"
	RuleAWSAccessKey  = "builtin.aws-access-key"
	RuleGitHubToken   = "builtin.github-token"
	RuleSlackToken    = "builtin.slack-token"
	RuleCredentialAsg = "builtin.credential-assignment"
	RuleHomePath      = "builtin.home-path"
	RuleUserPath      = "builtin.user-path"
	RuleEmail         = "builtin.email"
	RuleIPv4          = "builtin.ipv4"
	RuleEnvDump       = "builtin.env-dump"
	RuleHighEntropy   = "builtin.high-entropy"
)

// rule rewrites one line (which never contains a newline) and reports how
// many substitutions it made.
type rule struct {
	id    string
	class Class
	line  func(s string) (string, int, error)
}

// Rule describes a built-in rule for listing.
type Rule struct {
	ID    string
	Class Class
}

var (
	pemBegin = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----`)
	pemEnd   = regexp.MustCompile(`-----END [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----`)

	reAuthHeader = regexp.MustCompile(`(?i)\bauthorization\s*[=:]\s*(?:(?:bearer|basic|token|digest)\s+)?\S+`)
	reBearer     = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{8,}`)
	reOpenAI     = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_-])sk-[A-Za-z0-9_-]{8,}`)
	reAWS        = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_-])(?:AKIA|ASIA)[0-9A-Z]{8,}`)
	reGitHub     = regexp.MustCompile(`(?:^|[^A-Za-z0-9_-])(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})`)
	reSlack      = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_-])xox[baprs]-[A-Za-z0-9-]{10,}`)
	reCredential = regexp.MustCompile(`(?i)(?:password|passwd|secret|token|api[_-]?key|private[_-]?key|bearer|authorization|credential)\s*[=:]\s*\S+`)

	reUserPath = regexp.MustCompile(`(?:^|[^A-Za-z0-9_.-])/(?:Users|home)/[A-Za-z0-9._-]+`)
	reEmail    = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)
	reIPv4     = regexp.MustCompile(`\b(?:(?:25[0-5]|2[0-4][0-9]|1?[0-9]?[0-9])\.){3}(?:25[0-5]|2[0-4][0-9]|1?[0-9]?[0-9])\b`)
	reEnvDump  = regexp.MustCompile(`(?:^|[^A-Za-z0-9_])[A-Z][A-Z0-9_]*=\S+`)

	// reRedacted recognises a match that ends in an existing placeholder, so
	// later rules do not redact a placeholder a rule already wrote.
	reRedacted    = regexp.MustCompile(`\[REDACTED(?::[^\]\s]*)?\]$`)
	rePlaceholder = regexp.MustCompile(`\[REDACTED(?::[^\]\s]*)?\]`)
)

// regexRule builds a line rule from a pattern. keep returns how many leading
// bytes of a match to preserve (a boundary character consumed by the
// pattern); repl builds the replacement for the remainder. A match whose text
// is already redacted, or that an allow pattern covers, is left alone.
func regexRule(id string, class Class, re *regexp.Regexp, keep func(m string) int, repl func(m string) string, allow []*regexp.Regexp) rule {
	return rule{id: id, class: class, line: func(s string) (string, int, error) {
		locs := re.FindAllStringIndex(s, -1)
		if len(locs) == 0 {
			return s, 0, nil
		}
		spans := placeholderSpans(s)
		var b strings.Builder
		last, n := 0, 0
		for _, loc := range locs {
			m := s[loc[0]:loc[1]]
			k := 0
			if keep != nil {
				k = keep(m)
			}
			body := m[k:]
			if reRedacted.MatchString(body) || insideSpan(spans, loc[0]+k) || allowed(allow, body) {
				continue
			}
			b.WriteString(s[last : loc[0]+k])
			b.WriteString(repl(body))
			last = loc[1]
			n++
		}
		if n == 0 {
			return s, 0, nil
		}
		b.WriteString(s[last:])
		return b.String(), n, nil
	}}
}

// placeholderSpans finds placeholders already written into s. A later rule
// must not re-match text inside one: "known-secret:ab12" would look like a
// credential assignment.
func placeholderSpans(s string) [][]int {
	if !strings.Contains(s, "[REDACTED") {
		return nil
	}
	return rePlaceholder.FindAllStringIndex(s, -1)
}

func insideSpan(spans [][]int, pos int) bool {
	for _, sp := range spans {
		if pos >= sp[0] && pos < sp[1] {
			return true
		}
	}
	return false
}

func allowed(allow []*regexp.Regexp, s string) bool {
	for _, a := range allow {
		if a.MatchString(s) {
			return true
		}
	}
	return false
}

// keepBoundary preserves a single leading boundary character, when the match
// has one; a match at the start of the line has none.
func keepBoundary(prefix *regexp.Regexp) func(string) int {
	return func(m string) int {
		if loc := prefix.FindStringIndex(m); loc != nil && loc[0] == 0 {
			return loc[1]
		}
		return 0
	}
}

// literalRule replaces every occurrence of each needle.
func literalRule(id string, class Class, needles []string, with func(needle string) string) rule {
	return rule{id: id, class: class, line: func(s string) (string, int, error) {
		n := 0
		for _, needle := range needles {
			if c := strings.Count(s, needle); c > 0 {
				s = strings.ReplaceAll(s, needle, with(needle))
				n += c
			}
		}
		return s, n, nil
	}}
}

func hasLetterAndDigit(s string) (letter, digit bool) {
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= '0' && c <= '9':
			digit = true
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
			letter = true
		}
	}
	return
}

func shannon(s string) float64 {
	var freq [256]int
	for i := 0; i < len(s); i++ {
		freq[s[i]]++
	}
	var h float64
	n := float64(len(s))
	for _, f := range freq {
		if f == 0 {
			continue
		}
		p := float64(f) / n
		h -= p * math.Log2(p)
	}
	return h
}

// Defaults for the high-entropy rule. The threshold is above the 4.0 bits/char
// ceiling of a hex digest, so git shas and content hashes are preserved.
const (
	DefaultEntropyThreshold = 4.3
	DefaultMinTokenLen      = 40

	// Strict mode redacts when in doubt: 40-character hex digests (about 3.7
	// bits/char) and shorter tokens are caught.
	StrictEntropyThreshold = 3.5
	StrictMinTokenLen      = 24
)

func entropyRule(allow []*regexp.Regexp, re *regexp.Regexp, threshold float64, mark Placeholder) rule {
	return rule{id: RuleHighEntropy, class: Soft, line: func(s string) (string, int, error) {
		locs := re.FindAllStringIndex(s, -1)
		if len(locs) == 0 {
			return s, 0, nil
		}
		spans := placeholderSpans(s)
		var b strings.Builder
		last, n := 0, 0
		for _, loc := range locs {
			m := s[loc[0]:loc[1]]
			if insideSpan(spans, loc[0]) {
				continue
			}
			l, d := hasLetterAndDigit(m)
			if !l || !d || shannon(m) < threshold || allowed(allow, m) {
				continue
			}
			b.WriteString(s[last:loc[0]])
			b.WriteString(mark(RuleHighEntropy, m))
			last = loc[1]
			n++
		}
		if n == 0 {
			return s, 0, nil
		}
		b.WriteString(s[last:])
		return b.String(), n, nil
	}}
}

// Boundary prefixes used by keepBoundary: a single non-token character.
var boundary = regexp.MustCompile(`^[^A-Za-z0-9_-]`)
var envBoundary = regexp.MustCompile(`^[^A-Za-z0-9_]`)

// tuning carries the settings builtinRules needs beyond secrets and allowlists.
type tuning struct {
	mark      Placeholder
	threshold float64
	minLen    int
	custom    []rule
}

// builtinRules returns the ordered rule set. The private-key block rule is
// stateful across lines and is handled separately by the Redactor.
func builtinRules(secrets []string, home string, allow map[string][]*regexp.Regexp, t tuning) []rule {
	mark := markerFor(t.mark)
	if t.threshold == 0 {
		t.threshold = DefaultEntropyThreshold
	}
	if t.minLen == 0 {
		t.minLen = DefaultMinTokenLen
	}
	whole := func(id string) func(string) string {
		return func(m string) string { return mark(id, m) }
	}
	kb := keepBoundary(boundary)
	rules := []rule{
		literalRule(RuleKnownSecret, Hard, secrets, func(n string) string { return mark(RuleKnownSecret, n) }),
		regexRule(RuleAuthHeader, Hard, reAuthHeader, nil, whole(RuleAuthHeader), nil),
		regexRule(RuleBearerToken, Hard, reBearer, nil, whole(RuleBearerToken), nil),
		regexRule(RuleOpenAIKey, Hard, reOpenAI, kb, whole(RuleOpenAIKey), nil),
		regexRule(RuleAWSAccessKey, Hard, reAWS, kb, whole(RuleAWSAccessKey), nil),
		regexRule(RuleGitHubToken, Hard, reGitHub, kb, whole(RuleGitHubToken), nil),
		regexRule(RuleSlackToken, Hard, reSlack, kb, whole(RuleSlackToken), nil),
		regexRule(RuleCredentialAsg, Hard, reCredential, nil, whole(RuleCredentialAsg), nil),
	}
	rules = append(rules, t.custom...)
	if home != "" && home != "/" {
		rules = append(rules, literalRule(RuleHomePath, Soft, []string{home}, func(string) string { return "~" }))
	}
	entropyRe := regexp.MustCompile(fmt.Sprintf(`[A-Za-z0-9+/=_-]{%d,}`, t.minLen))
	rules = append(rules,
		regexRule(RuleUserPath, Soft, reUserPath, keepBoundary(regexp.MustCompile(`^[^/]`)), func(string) string { return "~" }, allow[RuleUserPath]),
		regexRule(RuleEmail, Soft, reEmail, nil, whole(RuleEmail), allow[RuleEmail]),
		regexRule(RuleIPv4, Soft, reIPv4, nil, whole(RuleIPv4), allow[RuleIPv4]),
		regexRule(RuleEnvDump, Soft, reEnvDump, keepBoundary(envBoundary), func(m string) string {
			name, val, _ := strings.Cut(m, "=")
			return name + "=" + mark(RuleEnvDump, val)
		}, allow[RuleEnvDump]),
		entropyRule(allow[RuleHighEntropy], entropyRe, t.threshold, mark),
	)
	return rules
}

// RuleInfo documents a built-in rule for `jevkit redact explain`.
type RuleInfo struct {
	ID      string
	Class   Class
	Pattern string
	Summary string
}

// Describe returns the documentation for a built-in rule id.
func Describe(id string) (RuleInfo, bool) {
	classes := map[string]Class{}
	for _, r := range Rules() {
		classes[r.ID] = r.Class
	}
	class, ok := classes[id]
	if !ok {
		return RuleInfo{}, false
	}
	pat, sum := "", ""
	switch id {
	case RuleKnownSecret:
		pat, sum = "(literal values)", "Redacts the live API key, the values of env vars named like *KEY*, *TOKEN*, *SECRET*, *PASSWORD*, *CREDENTIAL*, and your `literals` and `env_values`."
	case RulePrivateKey:
		pat, sum = pemBegin.String()+" ... "+pemEnd.String(), "Redacts whole PEM private key blocks, across lines."
	case RuleAuthHeader:
		pat, sum = reAuthHeader.String(), "Redacts Authorization header values."
	case RuleBearerToken:
		pat, sum = reBearer.String(), "Redacts bearer tokens."
	case RuleOpenAIKey:
		pat, sum = reOpenAI.String(), "Redacts OpenAI-style sk- API keys."
	case RuleAWSAccessKey:
		pat, sum = reAWS.String(), "Redacts AWS access key ids."
	case RuleGitHubToken:
		pat, sum = reGitHub.String(), "Redacts GitHub tokens."
	case RuleSlackToken:
		pat, sum = reSlack.String(), "Redacts Slack tokens."
	case RuleCredentialAsg:
		pat, sum = reCredential.String(), "Redacts values assigned to password/secret/token/api-key style names."
	case RuleHomePath:
		pat, sum = "(your home directory)", "Rewrites your home directory to ~."
	case RuleUserPath:
		pat, sum = reUserPath.String(), "Rewrites /Users/<name> and /home/<name> paths to ~."
	case RuleEmail:
		pat, sum = reEmail.String(), "Redacts email addresses."
	case RuleIPv4:
		pat, sum = reIPv4.String(), "Redacts IPv4 addresses."
	case RuleEnvDump:
		pat, sum = reEnvDump.String(), "Redacts the value in NAME=value environment dump lines."
	case RuleHighEntropy:
		pat = fmt.Sprintf("[A-Za-z0-9+/=_-]{min_token_length,} with Shannon entropy >= entropy_threshold (defaults: %d chars, %.1f bits/char; strict: %d chars, %.1f)", DefaultMinTokenLen, DefaultEntropyThreshold, StrictMinTokenLen, StrictEntropyThreshold)
		sum = "Redacts long random-looking tokens. The defaults keep 40-char git shas; strict mode redacts them."
	}
	return RuleInfo{ID: id, Class: class, Pattern: pat, Summary: sum}, true
}
