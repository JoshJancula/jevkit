package route

import (
	"fmt"
	"unicode/utf8"
)

// Bound head/tail-excerpts text to at most maxBytes, keeping both ends
// intact rather than truncating mid-structure — ralph's LOG_READ_MAX_BYTES
// discipline, ported to a single reusable place instead of every caller
// re-deriving its own truncation. maxBytes <= 0 means unbounded. The split is
// adjusted to the nearest rune boundary on each side so neither excerpt ends
// mid-character.
func Bound(text string, maxBytes int) string {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}
	marker := fmt.Sprintf("\n...[%d bytes omitted]...\n", len(text)-maxBytes)
	if len(marker) >= maxBytes {
		// Degenerate: the marker itself doesn't fit the budget. Keep as much
		// of the front as fits; still bounded, just no marker.
		return text[:maxRuneBoundary(text, maxBytes)]
	}
	remaining := maxBytes - len(marker)
	headLen := maxRuneBoundary(text, remaining/2)
	tailStart := minRuneBoundary(text, len(text)-(remaining-headLen))
	if tailStart < headLen {
		tailStart = headLen
	}
	return text[:headLen] + marker + text[tailStart:]
}

// maxRuneBoundary returns the largest index <= n that does not split a rune.
func maxRuneBoundary(s string, n int) int {
	if n <= 0 {
		return 0
	}
	if n >= len(s) {
		return len(s)
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return n
}

// minRuneBoundary returns the smallest index >= n that does not split a rune.
func minRuneBoundary(s string, n int) int {
	if n <= 0 {
		return 0
	}
	if n >= len(s) {
		return len(s)
	}
	for n < len(s) && !utf8.RuneStart(s[n]) {
		n++
	}
	return n
}
