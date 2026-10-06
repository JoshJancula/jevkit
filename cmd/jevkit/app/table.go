package app

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

// writeTable renders a small table; callers decide whether cells have color.
func WriteTable(w io.Writer, headers []string, rows [][]string) {
	WriteTableAligned(w, headers, rows, nil)
}

// WriteTableAligned is WriteTable with the columns marked in right
// right-aligned, so counts line up by place value.
func WriteTableAligned(w io.Writer, headers []string, rows [][]string, right []bool) {
	widths := make([]int, len(headers))
	for i, header := range headers {
		widths[i] = TextWidth(header)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) && TextWidth(cell) > widths[i] {
				widths[i] = TextWidth(cell)
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
			pad := strings.Repeat(" ", width-TextWidth(cell))
			if i < len(right) && right[i] {
				_, _ = fmt.Fprintf(w, " %s%s │", pad, cell)
			} else {
				_, _ = fmt.Fprintf(w, " %s%s │", cell, pad)
			}
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

var ANSISGR = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// textWidth ignores SGR color codes so styled cells stay aligned.
func TextWidth(s string) int { return utf8.RuneCountInString(ANSISGR.ReplaceAllString(s, "")) }
