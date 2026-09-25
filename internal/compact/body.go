package compact

import (
	"fmt"
	"strings"
)

type OutKind uint8

const (
	OutVerbatim OutKind = iota
	OutMarker
	OutHeader
)

type OutLine struct {
	Kind        OutKind
	Text        string
	SrcIdx      int
	Omitted     int
	Locus       string
	Outcome     string
	ContentKind string
	Level       int
}

// Body is assembled only from source indices and local, registered accounting
// constructors. Classifier answers never enter an append method.
type Body struct{ lines []OutLine }

func (b *Body) AppendVerbatim(srcIdx int, orig []string) {
	if srcIdx >= 0 && srcIdx < len(orig) {
		b.lines = append(b.lines, OutLine{Kind: OutVerbatim, Text: orig[srcIdx], SrcIdx: srcIdx})
	}
}

func (b *Body) AppendMarker(omitted int) {
	if omitted > 0 {
		b.lines = append(b.lines, OutLine{Kind: OutMarker, Text: fmt.Sprintf("[jevkit] %d original line(s) omitted", omitted), SrcIdx: -1, Omitted: omitted})
	}
}

func (b *Body) AppendHeader(locus, outcome, kind string, level int) {
	b.lines = append(b.lines, OutLine{Kind: OutHeader, Text: fmt.Sprintf("[jevkit] locus=%s outcome=%s kind=%s level=%d", locus, outcome, kind, level), SrcIdx: -1, Locus: locus, Outcome: outcome, ContentKind: kind, Level: level})
}

func (b *Body) Verify(orig []string) bool {
	if len(b.lines) == 0 || b.lines[0].Kind != OutHeader {
		return false
	}
	nextSource := 0
	for pos, line := range b.lines {
		switch line.Kind {
		case OutVerbatim:
			if line.SrcIdx != nextSource || line.SrcIdx >= len(orig) || line.Text != orig[line.SrcIdx] {
				return false
			}
			nextSource++
		case OutMarker:
			if line.Omitted <= 0 || nextSource+line.Omitted > len(orig) || line.SrcIdx != -1 || line.Text != fmt.Sprintf("[jevkit] %d original line(s) omitted", line.Omitted) {
				return false
			}
			nextSource += line.Omitted
		case OutHeader:
			if pos != 0 || line.SrcIdx != -1 || line.Level < 0 || line.Level >= len(budgetLines) ||
				!validOutcomes[line.Outcome] || !validKinds[line.ContentKind] || !validLocus(line.Locus) ||
				line.Text != fmt.Sprintf("[jevkit] locus=%s outcome=%s kind=%s level=%d", line.Locus, line.Outcome, line.ContentKind, line.Level) {
				return false
			}
		default:
			return false
		}
	}
	return nextSource == len(orig)
}

func validLocus(value string) bool {
	for _, label := range locusOrder {
		if value == label {
			return true
		}
	}
	return false
}

func (b *Body) String() string {
	parts := make([]string, len(b.lines))
	for i, line := range b.lines {
		parts[i] = line.Text
	}
	return strings.Join(parts, "\n")
}
