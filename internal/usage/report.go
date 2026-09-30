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
	if s.Attempts == 0 {
		if omitEmpty {
			return nil
		}
		_, err := fmt.Fprintln(w, "Jev (TypeSafe AI) usage: no recorded calls")
		return err
	}
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format+"\n", a...) }
	p("Jev (TypeSafe AI) usage")
	p("  successful calls: %d; transport attempts: %d (measured %d, usage unavailable %d, failed %d)", s.Calls, s.Attempts, s.AttemptsMeasured, s.AttemptsUnavailable, s.FailedAttempts)
	p("  tokens: input %s, output %s", formatCount(s.InputTokens, s.AttemptsUnavailable), formatCount(s.OutputTokens, s.AttemptsUnavailable))
	if s.Cost != nil {
		p("  cost: ~$%.6f (%s from measured usage)", s.Cost.EstimatedUSD, s.Cost.Note)
	} else {
		p("  cost: — (usage unavailable)")
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
			p("    %-32s calls %-5d attempts %-5d in %-8s out %s", k, t.Calls, t.Attempts, formatCount(t.InputTokens, t.Unavailable), formatCount(t.OutputTokens, t.Unavailable))
		}
	}
	group("by question set", s.ByQuestionSet)
	group("by model", s.ByModel)
	group("by agent", s.ByAgent)
	group("by origin", s.ByOrigin)
	return nil
}

func formatCount(known, unavailable int) string {
	if unavailable > 0 {
		if known == 0 {
			return fmt.Sprintf("— (%d unavailable)", unavailable)
		}
		return fmt.Sprintf("%s (+%d unavailable)", formatInt(known), unavailable)
	}
	return formatInt(known)
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
