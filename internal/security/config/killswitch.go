package config

import (
	"fmt"
	"regexp"

	"github.com/OWNER/jevkit/internal/globmatch"
)

type Killswitch struct {
	patterns []string
	regexps  []*regexp.Regexp
}

func NewKillswitch(patterns []string) (*Killswitch, error) {
	k := &Killswitch{}
	for _, pattern := range patterns {
		re, err := globmatch.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("killswitch %q: %w", pattern, err)
		}
		k.patterns = append(k.patterns, pattern)
		k.regexps = append(k.regexps, re)
	}
	return k, nil
}

func (k *Killswitch) Match(command string) (string, bool) {
	if k == nil {
		return "", false
	}
	for _, candidate := range globmatch.Segments(command) {
		for i, re := range k.regexps {
			if re.MatchString(candidate) {
				return k.patterns[i], true
			}
		}
	}
	return "", false
}
