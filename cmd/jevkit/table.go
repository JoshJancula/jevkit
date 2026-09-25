package main

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

// writeTable renders a small table; callers decide whether cells have color.
func writeTable(w io.Writer, headers []string, rows [][]string) {
	widths := make([]int, len(headers))
	for i, header := range headers {
		widths[i] = textWidth(header)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) && textWidth(cell) > widths[i] {
				widths[i] = textWidth(cell)
			}
		}
	}

	border := func(left, middle, right, fill string) {
		_, _ = fmt.Fprint(w, left)
		for i, width := range widths {
			if i > 0 {
				_, _ = fmt.Fprint(w, middle)
			}
			_, _ = fmt.Fprint(w, strings.Repeat(fill, width+2))
		}
		_, _ = fmt.Fprintln(w, right)
	}
	line := func(row []string) {
		_, _ = fmt.Fprint(w, "│")
		for i, width := range widths {
			cell := ""
			if i < len(row) {
				cell = row[i]
			}
			_, _ = fmt.Fprintf(w, " %s%s │", cell, strings.Repeat(" ", width-textWidth(cell)))
		}
		_, _ = fmt.Fprintln(w)
	}

	border("┌", "┬", "┐", "─")
	line(headers)
	border("├", "┼", "┤", "─")
	for _, row := range rows {
		line(row)
	}
	border("└", "┴", "┘", "─")
}

var ansiSGR = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// textWidth ignores SGR color codes so styled cells stay aligned.
func textWidth(s string) int { return utf8.RuneCountInString(ansiSGR.ReplaceAllString(s, "")) }
