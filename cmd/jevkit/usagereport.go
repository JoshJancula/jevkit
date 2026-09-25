package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/OWNER/jevkit/internal/sdlc/ledger"
	"github.com/OWNER/jevkit/internal/usage"
)

type runtimeTotals struct {
	Invocations     int      `json:"invocations"`
	InputTokens     int64    `json:"input_tokens"`
	OutputTokens    int64    `json:"output_tokens"`
	UnknownInput    int      `json:"unknown_input"`
	UnknownOutput   int      `json:"unknown_output"`
	SuppliedCostUSD *float64 `json:"supplied_cost_usd,omitempty"`
}

type runtimeSummary struct {
	Totals    runtimeTotals             `json:"totals"`
	ByRuntime map[string]*runtimeTotals `json:"by_runtime"`
	ByModel   map[string]*runtimeTotals `json:"by_model"`
	ByRole    map[string]*runtimeTotals `json:"by_role"`
}

type usageSources struct {
	Jev     *usage.Summary  `json:"jev,omitempty"`
	Runtime *runtimeSummary `json:"runtime,omitempty"`
}

// Top-level call totals are retained for readers of the v1 JSON shape;
// sources holds the authoritative, separate v2 accounting.
type usageReport struct {
	Kind          string       `json:"kind"`
	SchemaVersion int          `json:"schema_version"`
	Calls         int          `json:"calls"`
	InputTokens   int          `json:"input_tokens"`
	OutputTokens  int          `json:"output_tokens"`
	Sources       usageSources `json:"sources"`
}

func (a *App) allUsageRuns() ([]ledger.Run, error) {
	entries, err := os.ReadDir(a.sdlcRunsDir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var runs []ledger.Run
	for _, entry := range entries {
		if !entry.IsDir() || !sdlcRunIDRE.MatchString(entry.Name()) {
			continue
		}
		if run, err := ledger.Open(a.sdlcRunsDir(), entry.Name()).ReadRun(); err == nil {
			runs = append(runs, run)
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].CreatedAt < runs[j].CreatedAt })
	return runs, nil
}

func addRuntime(t *runtimeTotals, u ledger.InvocationUsage) {
	t.Invocations++
	if u.InputTokens == nil {
		t.UnknownInput++
	} else {
		t.InputTokens += *u.InputTokens
	}
	if u.OutputTokens == nil {
		t.UnknownOutput++
	} else {
		t.OutputTokens += *u.OutputTokens
	}
	if u.CostUSD != nil {
		if t.SuppliedCostUSD == nil {
			t.SuppliedCostUSD = new(float64)
		}
		*t.SuppliedCostUSD += *u.CostUSD
	}
}

func aggregateRuntime(runs []ledger.Run) runtimeSummary {
	s := runtimeSummary{ByRuntime: map[string]*runtimeTotals{}, ByModel: map[string]*runtimeTotals{}, ByRole: map[string]*runtimeTotals{}}
	for _, run := range runs {
		seen := map[string]ledger.InvocationUsage{}
		for _, u := range run.Usage {
			seen[u.Invocation] = u
		}
		for _, u := range seen {
			addRuntime(&s.Totals, u)
			for _, pair := range []struct {
				group map[string]*runtimeTotals
				key   string
			}{
				{s.ByRuntime, u.Runtime}, {s.ByModel, u.Model}, {s.ByRole, u.Role},
			} {
				key := pair.key
				if key == "" {
					key = "(unknown)"
				}
				if pair.group[key] == nil {
					pair.group[key] = &runtimeTotals{}
				}
				addRuntime(pair.group[key], u)
			}
		}
	}
	return s
}

func (a *App) renderJevUsage(s usage.Summary) {
	if s.Calls == 0 {
		fmt.Fprintf(a.Stdout, "%s: no recorded calls\n", a.styled(a.Stdout, ansiCyan, "Jev (TypeSafe AI) usage"))
		return
	}

	a.heading("Jev (TypeSafe AI) usage")
	fmt.Fprintf(a.Stdout, "  calls: %d (measured %d, usage unavailable %d)\n", s.Calls, s.CallsMeasured, s.CallsUnavailable)
	fmt.Fprintf(a.Stdout, "  tokens: input %s, output %s\n", formatInt(int64(s.InputTokens)), formatInt(int64(s.OutputTokens)))
	if s.Cost != nil {
		fmt.Fprintf(a.Stdout, "  cost: ~$%.6f (%s)\n", s.Cost.EstimatedUSD, s.Cost.Note)
	}

	for _, group := range []struct {
		title string
		items map[string]*usage.Tokens
	}{
		{"By question set", s.ByQuestionSet}, {"By model", s.ByModel}, {"By agent", s.ByAgent},
	} {
		keys := make([]string, 0, len(group.items))
		for key := range group.items {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			a, b := group.items[keys[i]], group.items[keys[j]]
			if a.Calls != b.Calls {
				return a.Calls > b.Calls
			}
			return keys[i] < keys[j]
		})
		if len(keys) == 0 {
			continue
		}

		a.heading(group.title)
		rows := make([][]string, 0, len(keys))
		for _, key := range keys {
			t := group.items[key]
			rows = append(rows, []string{key, fmt.Sprint(t.Calls), formatInt(int64(t.InputTokens)), formatInt(int64(t.OutputTokens))})
		}
		a.table([]string{"NAME", "CALLS", "INPUT", "OUTPUT"}, rows)
	}
}

func (a *App) renderRuntime(s runtimeSummary) {
	a.heading("Agent runtime usage")
	fmt.Fprintf(a.Stdout, "  invocations: %d\n", s.Totals.Invocations)
	fmt.Fprintf(a.Stdout, "  tokens: input %s (%d unknown), output %s (%d unknown)\n", formatInt(s.Totals.InputTokens), s.Totals.UnknownInput, formatInt(s.Totals.OutputTokens), s.Totals.UnknownOutput)
	if s.Totals.SuppliedCostUSD != nil {
		fmt.Fprintf(a.Stdout, "  supplied cost: $%.6f\n", *s.Totals.SuppliedCostUSD)
	}

	for _, group := range []struct {
		title string
		items map[string]*runtimeTotals
	}{
		{"By runtime", s.ByRuntime}, {"By model", s.ByModel}, {"By role", s.ByRole},
	} {
		keys := make([]string, 0, len(group.items))
		for key := range group.items {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if len(keys) == 0 {
			continue
		}

		a.heading(group.title)
		rows := make([][]string, 0, len(keys))
		for _, key := range keys {
			t := group.items[key]
			rows = append(rows, []string{key, fmt.Sprint(t.Invocations), formatInt(t.InputTokens), formatInt(t.OutputTokens), fmt.Sprint(t.UnknownInput), fmt.Sprint(t.UnknownOutput)})
		}
		a.table([]string{"NAME", "INVOCATIONS", "INPUT", "OUTPUT", "INPUT UNKNOWN", "OUTPUT UNKNOWN"}, rows)
	}
}

func renderUsageReport(a *App, format, source string, jev usage.Summary, runtime runtimeSummary) error {
	w := a.Stdout
	if format == usage.FormatJSON {
		report := usageReport{Kind: "usage", SchemaVersion: 2}
		if source == "all" || source == "jev" {
			report.Sources.Jev = &jev
			report.Calls, report.InputTokens, report.OutputTokens = jev.Calls, jev.InputTokens, jev.OutputTokens
		}
		if source == "all" || source == "runtime" {
			report.Sources.Runtime = &runtime
		}
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	if source == "all" || source == "jev" {
		a.renderJevUsage(jev)
	}
	if source == "all" {
		fmt.Fprintln(w)
	}
	if source == "all" || source == "runtime" {
		a.renderRuntime(runtime)
	}
	return nil
}
