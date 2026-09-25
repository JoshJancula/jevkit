package usage

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
)

// Format names an output format.
const (
	FormatText = "text"
	FormatJSON = "json"
)

// Render writes s as text or indented JSON. With omitEmpty, text output for a
// summary with no calls is empty.
func Render(w io.Writer, s Summary, format string, omitEmpty bool) error {
	switch format {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(s)
	case FormatText, "":
		return renderText(w, s, omitEmpty)
	}
	return fmt.Errorf("usage: unknown format %q (want text or json)", format)
}

func renderText(w io.Writer, s Summary, omitEmpty bool) error {
	if s.Calls == 0 {
		if omitEmpty {
			return nil
		}
		_, err := fmt.Fprintln(w, "Jev (TypeSafe AI) usage: no recorded calls")
		return err
	}
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format+"\n", a...) }
	p("Jev (TypeSafe AI) usage")
	p("  calls: %d (measured %d, usage unavailable %d)", s.Calls, s.CallsMeasured, s.CallsUnavailable)
	p("  tokens: input %s, output %s", formatInt(s.InputTokens), formatInt(s.OutputTokens))
	if s.Cost != nil {
		p("  cost: ~$%.6f (%s)", s.Cost.EstimatedUSD, s.Cost.Note)
	}
	group := func(title string, m map[string]*Tokens) {
		if len(m) == 0 {
			return
		}
		names := make([]string, 0, len(m))
		for k := range m {
			names = append(names, k)
		}
		sort.Slice(names, func(i, j int) bool {
			if a, b := m[names[i]].Calls, m[names[j]].Calls; a != b {
				return a > b
			}
			return names[i] < names[j]
		})
		p("  %s:", title)
		for _, k := range names {
			t := m[k]
			p("    %-32s calls %-5d in %-8s out %s", k, t.Calls, formatInt(t.InputTokens), formatInt(t.OutputTokens))
		}
	}
	group("by question set", s.ByQuestionSet)
	group("by model", s.ByModel)
	group("by agent", s.ByAgent)
	return nil
}

func formatInt(n int) string {
	digits := strconv.Itoa(n)
	start := 0
	if digits[0] == '-' {
		start = 1
	}
	groups := (len(digits) - start - 1) / 3
	if groups == 0 {
		return digits
	}

	formatted := make([]byte, 0, len(digits)+groups)
	formatted = append(formatted, digits[:start]...)
	for i := start; i < len(digits); i++ {
		if i > start && (len(digits)-i)%3 == 0 {
			formatted = append(formatted, ',')
		}
		formatted = append(formatted, digits[i])
	}
	return string(formatted)
}
