package compact

import (
	"regexp"
	"strings"
)

var (
	gitDiffHeaderRE = regexp.MustCompile(`^diff --git `)
	gitDiffHunkRE   = regexp.MustCompile(`^@@\s`)
	gitDiffStatRE   = regexp.MustCompile(`^\s+(.+?)\s+\|\s+(\d+)\s+([+-]+)\s*$`)
	grepStyleRE     = regexp.MustCompile(`^(.+?)(?::(\d+))?:(.+)$`)
	lsLongRE        = regexp.MustCompile(`^[-drwxlstSugT.@+]+\s+`)
	treeLineRE      = regexp.MustCompile("^[|\\\\`\\-\\s]*[|`\\\\]")
)

func fraction(lines []string, pred func(string) bool, frac float64) bool {
	n := 0
	for _, l := range lines {
		if pred(l) {
			n++
		}
	}
	return float64(n) >= float64(len(lines))*frac
}

func shapeGitDiff(text string) bool {
	for _, l := range splitLines(text) {
		if gitDiffHeaderRE.MatchString(l) || gitDiffHunkRE.MatchString(l) || gitDiffStatRE.MatchString(l) {
			return true
		}
	}
	return false
}

func shapeGrep(text string) bool {
	lines := splitLines(text)
	return len(lines) >= 3 && fraction(lines, grepStyleRE.MatchString, 0.7)
}

func shapePathList(text string) bool {
	var lines []string
	for _, l := range splitLines(text) {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) < 5 {
		return false
	}
	return fraction(lines, func(l string) bool {
		return strings.HasPrefix(l, "./") || strings.HasPrefix(l, "/") || strings.Contains(l, "/")
	}, 0.8)
}

func shapeTree(text string) bool {
	lines := splitLines(text)
	return len(lines) >= 5 && fraction(lines, treeLineRE.MatchString, 0.5)
}

func shapeLSLong(text string) bool {
	lines := splitLines(text)
	return len(lines) >= 5 && fraction(lines, lsLongRE.MatchString, 0.6)
}

// detectSourceShape names a source-output family from output shape alone. It
// is only consulted when the command is empty, so that unlabeled diff, grep,
// path-list, tree and ls output is never shortened. Ambiguous shapes return
// "" (as ralph does).
func detectSourceShape(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	var hits []string
	for _, d := range []struct {
		family string
		fn     func(string) bool
	}{
		{FamilyGitDiff, shapeGitDiff},
		{FamilyGrep, shapeGrep},
		{FamilyTree, shapeTree},
		{FamilyLS, shapeLSLong},
		{FamilyFind, shapePathList},
	} {
		if d.fn(text) {
			hits = append(hits, d.family)
		}
	}
	if len(hits) == 1 {
		return hits[0]
	}
	return ""
}
