package usage

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/usage"
)

type RuntimeTotals struct {
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
	Totals         RuntimeTotals             `json:"totals"`
	ByRuntime      map[string]*RuntimeTotals `json:"by_runtime"`
	ByModel        map[string]*RuntimeTotals `json:"by_model"`
	ByRole         map[string]*RuntimeTotals `json:"by_role"`
	CacheDecisions map[string]int            `json:"cache_decisions,omitempty"`
}

type usageSources struct {
	Jev          *usage.Summary        `json:"jev,omitempty"`
	Runtime      *runtimeSummary       `json:"runtime,omitempty"`
	HookActivity map[string]HookCounts `json:"hook_activity,omitempty"`
}

type HookCounts struct {
	PreTool  int `json:"pre_tool"`
	PostTool int `json:"post_tool"`
	Other    int `json:"other"`
	NonOK    int `json:"non_ok"`
}

func AggregateHooks(recs []usage.HookInvocation) map[string]HookCounts {
	counts := map[string]HookCounts{}
	for _, rec := range recs {
		key := rec.Agent
		if key == "" {
			key = "(unknown)"
		}
		c := counts[key]
		switch rec.Event {
		case "pre-tool":
			c.PreTool++
		case "post-tool":
			c.PostTool++
		default:
			c.Other++
		}
		if rec.Outcome != "ok" {
			c.NonOK++
		}
		counts[key] = c
	}
	return counts
}

// Top-level call totals are retained for readers of the v1 JSON shape;
// sources holds the authoritative, separate v2 accounting.
type UsageReport struct {
	Kind          string       `json:"kind"`
	SchemaVersion int          `json:"schema_version"`
	Calls         int          `json:"calls"`
	InputTokens   int          `json:"input_tokens"`
	OutputTokens  int          `json:"output_tokens"`
	Sources       usageSources `json:"sources"`
}

func (a *App) AllUsageRuns() ([]ledger.Run, error) {
	entries, err := os.ReadDir(a.SDLCRunsDir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var runs []ledger.Run
	for _, entry := range entries {
		if !entry.IsDir() || !app.RunIDPattern.MatchString(entry.Name()) {
			continue
		}
		if run, err := ledger.Open(a.SDLCRunsDir(), entry.Name()).ReadRun(); err == nil {
			runs = append(runs, run)
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].CreatedAt < runs[j].CreatedAt })
	return runs, nil
}

func AddRuntime(t *RuntimeTotals, u ledger.InvocationUsage) {
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

func AggregateRuntime(runs []ledger.Run) runtimeSummary {
	s := runtimeSummary{ByRuntime: map[string]*RuntimeTotals{}, ByModel: map[string]*RuntimeTotals{}, ByRole: map[string]*RuntimeTotals{}}
	for _, run := range runs {
		seen := map[string]ledger.InvocationUsage{}
		for _, u := range run.Usage {
			seen[u.Invocation] = u
		}
		for _, u := range seen {
			AddRuntime(&s.Totals, u)
			for _, pair := range []struct {
				group map[string]*RuntimeTotals
				key   string
			}{
				{s.ByRuntime, u.Runtime}, {s.ByModel, u.Model}, {s.ByRole, u.Role},
			} {
				key := pair.key
				if key == "" {
					key = "(unknown)"
				}
				if pair.group[key] == nil {
					pair.group[key] = &RuntimeTotals{}
				}
				AddRuntime(pair.group[key], u)
			}
		}
	}
	return s
}

func (a *App) renderJevUsage(s usage.Summary) {
	if s.Attempts == 0 {
		_, _ = fmt.Fprintf(a.Stdout, "%s: no recorded calls\n", a.Styled(a.Stdout, app.ANSICyan, "Jev (TypeSafe AI) usage"))
		return
	}

	a.Heading("Jev (TypeSafe AI) usage")
	_, _ = fmt.Fprintf(a.Stdout, "  successful calls: %d; transport attempts: %d (measured %d, usage unavailable %d, failed %d)\n", s.Calls, s.Attempts, s.AttemptsMeasured, s.AttemptsUnavailable, s.FailedAttempts)
	_, _ = fmt.Fprintf(a.Stdout, "  tokens: input %s, output %s\n", app.UsageCount(int64(s.InputTokens), s.AttemptsUnavailable), app.UsageCount(int64(s.OutputTokens), s.AttemptsUnavailable))
	if s.Cost != nil {
		_, _ = fmt.Fprintf(a.Stdout, "  cost: ~$%.6f (%s from measured usage", s.Cost.EstimatedUSD, s.Cost.Note)
		if s.AttemptsUnavailable > 0 {
			_, _ = fmt.Fprintf(a.Stdout, "; %d attempts have unavailable usage", s.AttemptsUnavailable)
		}
		_, _ = fmt.Fprintln(a.Stdout, ")")
	} else {
		_, _ = fmt.Fprintln(a.Stdout, "  cost: — (usage unavailable)")
	}

	for _, group := range []struct {
		title string
		items map[string]*usage.Tokens
	}{
		{"By question set", s.ByQuestionSet}, {"By model", s.ByModel}, {"By agent", s.ByAgent}, {"By origin", s.ByOrigin},
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

		a.Heading(group.title)
		rows := make([][]string, 0, len(keys))
		for _, key := range keys {
			t := group.items[key]
			rows = append(rows, []string{key, fmt.Sprint(t.Calls), fmt.Sprint(t.Attempts), app.UsageCount(int64(t.InputTokens), t.Unavailable), app.UsageCount(int64(t.OutputTokens), t.Unavailable)})
		}
		a.Table([]string{"NAME", "CALLS", "ATTEMPTS", "INPUT", "OUTPUT"}, rows)
	}
}

func (a *App) renderRuntime(s runtimeSummary) {
	a.Heading("Agent runtime usage")
	_, _ = fmt.Fprintf(a.Stdout, "  invocations: %d\n", s.Totals.Invocations)
	_, _ = fmt.Fprintf(a.Stdout, "  tool calls: %s\n", app.UsageCount(s.Totals.ToolCalls, s.Totals.UnknownToolCalls))
	_, _ = fmt.Fprintf(a.Stdout, "  tokens: input %s, output %s\n", app.UsageCount(s.Totals.InputTokens, s.Totals.UnknownInput), app.UsageCount(s.Totals.OutputTokens, s.Totals.UnknownOutput))
	_, _ = fmt.Fprintf(a.Stdout, "  cache: read %s, creation %s\n", app.UsageCount(s.Totals.CacheReadTokens, s.Totals.UnknownCacheRead), app.UsageCount(s.Totals.CacheCreationTokens, s.Totals.UnknownCacheCreation))
	if s.Totals.SuppliedCostUSD != nil {
		_, _ = fmt.Fprintf(a.Stdout, "  supplied cost: $%.6f", *s.Totals.SuppliedCostUSD)
		if s.Totals.UnknownCost > 0 {
			_, _ = fmt.Fprintf(a.Stdout, " (+%d unavailable)", s.Totals.UnknownCost)
		}
		_, _ = fmt.Fprintln(a.Stdout)
	} else {
		if s.Totals.UnknownCost > 0 {
			_, _ = fmt.Fprintf(a.Stdout, "  supplied cost: — (%d unavailable)\n", s.Totals.UnknownCost)
		} else {
			_, _ = fmt.Fprintln(a.Stdout, "  supplied cost: —")
		}
	}
	if len(s.CacheDecisions) > 0 {
		_, _ = fmt.Fprintf(a.Stdout, "  prompt cache decisions: enabled %d, disabled %d, unmanaged %d\n", s.CacheDecisions["enabled"], s.CacheDecisions["disabled"], s.CacheDecisions["unmanaged"])
	}

	for _, group := range []struct {
		title string
		items map[string]*RuntimeTotals
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

		a.Heading(group.title)
		rows := make([][]string, 0, len(keys))
		for _, key := range keys {
			t := group.items[key]
			rows = append(rows, []string{
				key, fmt.Sprint(t.Invocations), app.UsageCount(t.ToolCalls, t.UnknownToolCalls),
				app.UsageCount(t.InputTokens, t.UnknownInput), app.UsageCount(t.OutputTokens, t.UnknownOutput),
				app.UsageCount(t.CacheReadTokens, t.UnknownCacheRead), app.UsageCount(t.CacheCreationTokens, t.UnknownCacheCreation),
			})
		}
		a.Table([]string{"NAME", "INVOCATIONS", "TOOL CALLS", "INPUT", "OUTPUT", "CACHE READ", "CACHE CREATE"}, rows)
	}
}

func renderUsageReport(a *App, format, source string, jev usage.Summary, runtime runtimeSummary, hooks map[string]HookCounts) error {
	w := a.Stdout
	if format == usage.FormatJSON {
		report := UsageReport{Kind: "usage", SchemaVersion: 2}
		if source == "all" || source == "jev" {
			report.Sources.Jev = &jev
			report.Sources.HookActivity = hooks
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
		if len(hooks) > 0 {
			a.Heading("Hook dispatches (separate from Jev calls)")
			keys := make([]string, 0, len(hooks))
			for key := range hooks {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			rows := make([][]string, 0, len(keys))
			for _, key := range keys {
				c := hooks[key]
				rows = append(rows, []string{key, fmt.Sprint(c.PreTool), fmt.Sprint(c.PostTool), fmt.Sprint(c.Other), fmt.Sprint(c.NonOK)})
			}
			a.Table([]string{"AGENT", "PRE TOOL", "POST TOOL", "OTHER", "NON OK"}, rows)
		}
	}
	if source == "all" {
		_, _ = fmt.Fprintln(w)
	}
	if source == "all" || source == "runtime" {
		a.renderRuntime(runtime)
	}
	return nil
}
