package main

import (
	"fmt"
	"strings"
)

const diffContext = 3

// unifiedDiff renders a unified diff of two texts with the same number of
// lines (redaction never adds or removes a line). It returns "" when the texts
// are equal.
func unifiedDiff(fromName, toName, from, to string) string {
	a, b := splitLines(from), splitLines(to)
	if len(a) != len(b) {
		// Not reachable for redaction output; fall back to a whole-file diff.
		return wholeDiff(fromName, toName, a, b)
	}
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

func wholeDiff(fromName, toName string, a, b []string) string {
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

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
