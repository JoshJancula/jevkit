package agents

import (
	"fmt"
	"strings"
)

const diffContext = 3

// UnifiedDiff renders a unified diff of two texts. Empty when equal.
func UnifiedDiff(fromName, toName string, before, after []byte) string {
	a, b := splitDiffLines(string(before)), splitDiffLines(string(after))
	if len(a) == len(b) {
		return lineAlignedDiff(fromName, toName, a, b)
	}
	return wholeFileDiff(fromName, toName, a, b)
}

func lineAlignedDiff(fromName, toName string, a, b []string) string {
	var changed []int
	for i := range a {
		if a[i] != b[i] {
			changed = append(changed, i)
		}
	}
	if len(changed) == 0 {
		return ""
	}
	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s\n", fromName, toName)
	for i := 0; i < len(changed); {
		start := max(changed[i]-diffContext, 0)
		end := changed[i]
		j := i
		for j+1 < len(changed) && changed[j+1]-end <= 2*diffContext {
			j++
			end = changed[j]
		}
		stop := min(end+diffContext+1, len(a))
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", start+1, stop-start, start+1, stop-start)
		k := start
		for k < stop {
			if a[k] == b[k] {
				out.WriteString(" " + a[k] + "\n")
				k++
				continue
			}
			run := k
			for run < stop && a[run] != b[run] {
				run++
			}
			for x := k; x < run; x++ {
				out.WriteString("-" + a[x] + "\n")
			}
			for x := k; x < run; x++ {
				out.WriteString("+" + b[x] + "\n")
			}
			k = run
		}
		i = j + 1
	}
	return out.String()
}

func wholeFileDiff(fromName, toName string, a, b []string) string {
	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s\n@@ -1,%d +1,%d @@\n", fromName, toName, len(a), len(b))
	for _, l := range a {
		out.WriteString("-" + l + "\n")
	}
	for _, l := range b {
		out.WriteString("+" + l + "\n")
	}
	return out.String()
}

func splitDiffLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

func reportPreview(opts InstallOptions, path string, before, after []byte) {
	if opts.Preview != nil {
		opts.Preview(FilePreview{Path: path, Before: before, After: after})
	}
}
