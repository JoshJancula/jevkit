package security

import (
	"regexp"
	"strings"
)

var injectionPhrases = []*regexp.Regexp{
	regexp.MustCompile(`(?i)ignore (all )?(previous|prior|above) instructions`),
	regexp.MustCompile(`(?i)(system|developer) (message|prompt|instructions)`),
	regexp.MustCompile(`(?i)(send|upload|exfiltrate).{0,50}(secret|token|key|credential)`),
	regexp.MustCompile(`(?i)(do not|don't) (tell|inform|show) (the )?user`),
}

func injectionHits(body string) (hits []string, strong bool) {
	for _, r := range injectionPhrases {
		if r.MatchString(body) {
			hits = append(hits, r.String())
		}
	}
	for _, marker := range []string{"[im_start]", "[start_header_id]", "[eot_id]", "<|im_start|>", "<|start_header_id|>", "<|eot_id|>"} {
		if strings.Contains(body, marker) {
			hits = append(hits, "chat-template token")
			strong = true
			break
		}
	}
	for _, r := range body {
		if r >= 0xE0000 && r <= 0xE007F {
			hits = append(hits, "unicode tag character")
			strong = true
			break
		}
	}
	return hits, strong
}
