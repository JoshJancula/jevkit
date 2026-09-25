// Package globmatch compiles the small, anchored glob language used by local policies.
package globmatch

import (
	"fmt"
	"regexp"
	"strings"
)

// Compile supports * (any run) and ? (one character). All other characters
// are literal. Patterns that match every string are rejected.
func Compile(glob string) (*regexp.Regexp, error) {
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

// Segments returns the full subject and each shell segment. The caller may
// match either form, preserving the existing never_send behavior.
func Segments(subject string) []string {
	out := []string{strings.TrimSpace(subject)}
	for _, seg := range regexp.MustCompile(`[;&|\n]+`).Split(subject, -1) {
		if seg = strings.TrimSpace(seg); seg != "" {
			out = append(out, seg)
		}
	}
	return out
}
