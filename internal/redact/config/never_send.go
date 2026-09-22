package config

import (
	"fmt"
	"regexp"
	"strings"
)

// builtinNeverSend is the embedded layer: sources whose output is never sent.
var builtinNeverSend = []string{"cat .env*", "*/secrets/*", "kubectl get secret*"}

// NeverSend matches command lines and paths whose output must never reach
// Jev. It is consulted before any network use.
type NeverSend struct {
	pats []neverPattern
}

type neverPattern struct {
	glob string
	re   *regexp.Regexp
}

// compileNever turns a glob into an anchored regexp: `*` matches any run of
// characters (including `/` and spaces), `?` matches one character, and
// everything else is literal.
func compileNever(glob string) (*regexp.Regexp, error) {
	if strings.TrimSpace(glob) == "" {
		return nil, fmt.Errorf("pattern is empty")
	}
	if strings.Trim(glob, "*?") == "" {
		return nil, fmt.Errorf("pattern %q would match everything", glob)
	}
	var b strings.Builder
	b.WriteString(`(?s)^`)
	for _, r := range glob {
		switch r {
		case '*':
			b.WriteString(`.*`)
		case '?':
			b.WriteString(`.`)
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString(`$`)
	return regexp.Compile(b.String())
}

// NewNeverSend compiles glob patterns.
func NewNeverSend(globs []string) (*NeverSend, error) {
	n := &NeverSend{}
	for _, g := range globs {
		re, err := compileNever(g)
		if err != nil {
			return nil, fmt.Errorf("never_send %q: %w", g, err)
		}
		n.pats = append(n.pats, neverPattern{g, re})
	}
	return n, nil
}

var segmentSplit = regexp.MustCompile(`[;&|\n]+`)

// Match reports the first pattern that matches subject, a command line or a
// path. A command line is also checked one shell segment at a time, so
// `cd app && cat .env` is caught by `cat .env*`.
func (n *NeverSend) Match(subject string) (pattern string, ok bool) {
	if n == nil {
		return "", false
	}
	cands := []string{strings.TrimSpace(subject)}
	for _, seg := range segmentSplit.Split(subject, -1) {
		if seg = strings.TrimSpace(seg); seg != "" {
			cands = append(cands, seg)
		}
	}
	for _, c := range cands {
		for _, p := range n.pats {
			if p.re.MatchString(c) || (!strings.HasPrefix(c, "/") && p.re.MatchString("/"+c)) {
				return p.glob, true
			}
		}
	}
	return "", false
}

// BuiltinNeverSend returns the embedded never_send globs.
func BuiltinNeverSend() []string { return append([]string(nil), builtinNeverSend...) }
