package config

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/OWNER/jevkit/internal/globmatch"
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
	return globmatch.Compile(glob)
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

// Match reports the first pattern that matches subject, a command line or a
// path. A command line is also checked one shell segment at a time, so
// `cd app && cat .env` is caught by `cat .env*`.
func (n *NeverSend) Match(subject string) (pattern string, ok bool) {
	if n == nil {
		return "", false
	}
	cands := globmatch.Segments(subject)
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
