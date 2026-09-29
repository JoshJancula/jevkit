package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/usage"
)

type runtimeTotals struct {
	Invocations          int      `json:"invocations"`
	ToolCalls            int64    `json:"tool_calls"`
	UnknownToolCalls     int      `json:"unknown_tool_calls"`
	InputTokens          int64    `json:"input_tokens"`
	OutputTokens         int64    `json:"output_tokens"`
	CacheReadTokens      int64    `json:"cache_read_tokens"`
	CacheCreationTokens  int64    `json:"cache_creation_tokens"`
	UnknownInput         int      `json:"unknown_input"`
	UnknownOutput        int      `json:"unknown_output"`
	UnknownCacheRead     int      `json:"unknown_cache_read"`
	UnknownCacheCreation int      `json:"unknown_cache_creation"`
	UnknownCost          int      `json:"unknown_cost"`
	SuppliedCostUSD      *float64 `json:"supplied_cost_usd,omitempty"`
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
	if u.ToolCalls == nil {
		t.UnknownToolCalls++
	} else {
		t.ToolCalls += *u.ToolCalls
	}
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
	if u.CacheReadTokens == nil {
		t.UnknownCacheRead++
	} else {
		t.CacheReadTokens += *u.CacheReadTokens
	}
	if u.CacheCreationTokens == nil {
		t.UnknownCacheCreation++
	} else {
		t.CacheCreationTokens += *u.CacheCreationTokens
	}
	if u.CostUSD != nil {
		if t.SuppliedCostUSD == nil {
			t.SuppliedCostUSD = new(float64)
		}
		*t.SuppliedCostUSD += *u.CostUSD
	} else {
		t.UnknownCost++
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
		_, _ = fmt.Fprintf(a.Stdout, "%s: no recorded calls\n", a.styled(a.Stdout, ansiCyan, "Jev (TypeSafe AI) usage"))
		return
	}

	a.heading("Jev (TypeSafe AI) usage")
	_, _ = fmt.Fprintf(a.Stdout, "  calls: %d (measured %d, usage unavailable %d)\n", s.Calls, s.CallsMeasured, s.CallsUnavailable)
	_, _ = fmt.Fprintf(a.Stdout, "  tokens: input %s, output %s\n", formatInt(int64(s.InputTokens)), formatInt(int64(s.OutputTokens)))
	if s.Cost != nil {
		_, _ = fmt.Fprintf(a.Stdout, "  cost: ~$%.6f (%s)\n", s.Cost.EstimatedUSD, s.Cost.Note)
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
	_, _ = fmt.Fprintf(a.Stdout, "  invocations: %d\n", s.Totals.Invocations)
	_, _ = fmt.Fprintf(a.Stdout, "  tool calls: %s (%d unknown)\n", formatInt(s.Totals.ToolCalls), s.Totals.UnknownToolCalls)
	_, _ = fmt.Fprintf(a.Stdout, "  tokens: input %s (%d unknown), output %s (%d unknown)\n", formatInt(s.Totals.InputTokens), s.Totals.UnknownInput, formatInt(s.Totals.OutputTokens), s.Totals.UnknownOutput)
	_, _ = fmt.Fprintf(a.Stdout, "  cache: read %s (%d unknown), creation %s (%d unknown)\n", formatInt(s.Totals.CacheReadTokens), s.Totals.UnknownCacheRead, formatInt(s.Totals.CacheCreationTokens), s.Totals.UnknownCacheCreation)
	if s.Totals.SuppliedCostUSD != nil {
		_, _ = fmt.Fprintf(a.Stdout, "  supplied cost: $%.6f (%d unknown)\n", *s.Totals.SuppliedCostUSD, s.Totals.UnknownCost)
	} else {
		_, _ = fmt.Fprintf(a.Stdout, "  supplied cost: unknown (%d unknown)\n", s.Totals.UnknownCost)
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
			rows = append(rows, []string{
				key, fmt.Sprint(t.Invocations), usageCount(t.ToolCalls, t.UnknownToolCalls),
				formatInt(t.InputTokens), formatInt(t.OutputTokens),
				formatInt(t.CacheReadTokens), formatInt(t.CacheCreationTokens),
				fmt.Sprint(t.UnknownInput), fmt.Sprint(t.UnknownOutput),
				fmt.Sprint(t.UnknownCacheRead), fmt.Sprint(t.UnknownCacheCreation),
			})
		}
		a.table([]string{"NAME", "INVOCATIONS", "TOOL CALLS", "INPUT", "OUTPUT", "CACHE READ", "CACHE CREATE", "INPUT UNKNOWN", "OUTPUT UNKNOWN", "CACHE READ UNKNOWN", "CACHE CREATE UNKNOWN"}, rows)
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
		_, _ = fmt.Fprintln(w)
	}
	if source == "all" || source == "runtime" {
		a.renderRuntime(runtime)
	}
	return nil
}
