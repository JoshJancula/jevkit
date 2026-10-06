package usage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/sdlc/worker"
	"github.com/JoshJancula/jevkit/internal/usage"
)

type RuntimeTotals struct {
	Invocations          int      `json:"invocations"`
	ElapsedMS            int64    `json:"elapsed_ms"`
	UnknownElapsed       int      `json:"unknown_elapsed"`
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
	// ContextInputTokens is input including cached input, which runtimes
	// report differently: Codex counts cached reads inside input, the others
	// report them separately. It is comparable across runtimes.
	ContextInputTokens int64 `json:"context_input_tokens"`
	// cachedContext is the context input of invocations that reported cache
	// reads: the denominator for their cache hit rate.
	cachedContext int64
	runtimes      map[string]bool
}

// RunTotals is one SDLC run's usage, with its fan-out child runs folded in.
type RunTotals struct {
	RunID     string `json:"run_id"`
	Task      string `json:"task,omitempty"`
	CreatedAt string `json:"created_at"`
	JevCalls  int    `json:"jev_calls"`
	RuntimeTotals
}

type runtimeSummary struct {
	Totals         RuntimeTotals             `json:"totals"`
	ByRuntime      map[string]*RuntimeTotals `json:"by_runtime"`
	ByModel        map[string]*RuntimeTotals `json:"by_model"`
	ByRole         map[string]*RuntimeTotals `json:"by_role"`
	ByRun          []*RunTotals              `json:"by_run"`
	CacheDecisions map[string]int            `json:"cache_decisions,omitempty"`
	// rootOf maps each run to the root run it is reported under.
	rootOf map[string]string
}

// AttachJev counts the successful Jev calls each SDLC run made, keyed by run
// as usage.Summary.ByRun is, into the run it is reported under.
func (s *runtimeSummary) AttachJev(byRun map[string]*usage.Tokens) {
	totals := map[string]*RunTotals{}
	for _, t := range s.ByRun {
		totals[t.RunID] = t
	}
	for id, calls := range byRun {
		root, ok := s.rootOf[id]
		if !ok {
			root = id
		}
		if t := totals[root]; t != nil {
			t.JevCalls += calls.Calls
		}
	}
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

// FillElapsedFromLogs estimates the time of invocations recorded before it
// was tracked, from the start in their log metadata to their last logged
// line. Invocations whose logs were pruned stay unreported.
func (a *App) FillElapsedFromLogs(runs []ledger.Run) {
	for i := range runs {
		dir := filepath.Join(a.SDLCRunsDir(), runs[i].RunID, "logs")
		for j := range runs[i].Usage {
			u := &runs[i].Usage[j]
			if u.ElapsedMS != nil {
				continue
			}
			if ms, ok := loggedElapsed(dir, u.Invocation); ok {
				u.ElapsedMS = &ms
			}
		}
	}
}

func loggedElapsed(dir, invocation string) (int64, bool) {
	if invocation == "" || filepath.Base(invocation) != invocation {
		return 0, false
	}
	raw, err := os.ReadFile(filepath.Join(dir, invocation+".json"))
	if err != nil {
		return 0, false
	}
	var meta worker.LogMeta
	if json.Unmarshal(raw, &meta) != nil {
		return 0, false
	}
	started, err := time.Parse(time.RFC3339, meta.StartedAt)
	if err != nil {
		return 0, false
	}
	f, err := os.Open(filepath.Join(dir, invocation+".lines.jsonl"))
	if err != nil {
		return 0, false
	}
	defer func() { _ = f.Close() }()
	// The last line is all that is needed; lines files can be large.
	const tail = 64 << 10
	if info, err := f.Stat(); err == nil && info.Size() > tail {
		if _, err := f.Seek(info.Size()-tail, io.SeekStart); err != nil {
			return 0, false
		}
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return 0, false
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	var last worker.LogLine
	if json.Unmarshal(lines[len(lines)-1], &last) != nil || last.At == 0 {
		return 0, false
	}
	ms := time.Unix(0, last.At).Sub(started).Milliseconds()
	return ms, ms > 0
}

func AddRuntime(t *RuntimeTotals, u ledger.InvocationUsage) {
	t.Invocations++
	if t.runtimes == nil {
		t.runtimes = map[string]bool{}
	}
	t.runtimes[u.Runtime] = true
	if u.ElapsedMS == nil {
		t.UnknownElapsed++
	} else {
		t.ElapsedMS += *u.ElapsedMS
	}
	if u.ToolCalls == nil {
		t.UnknownToolCalls++
	} else {
		t.ToolCalls += *u.ToolCalls
	}
	if u.InputTokens == nil {
		t.UnknownInput++
	} else {
		t.InputTokens += *u.InputTokens
		context := *u.InputTokens
		if u.Runtime != "codex" {
			context += derefZero(u.CacheReadTokens) + derefZero(u.CacheCreationTokens)
		}
		t.ContextInputTokens += context
		if u.CacheReadTokens != nil {
			t.cachedContext += context
		}
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

// modelKey names an invocation's model. An agent that pins none runs its
// runtime's default.
func modelKey(u ledger.InvocationUsage) string {
	if u.Model == "" && u.Runtime != "" {
		return "(" + u.Runtime + " default)"
	}
	return u.Model
}

func derefZero(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func AggregateRuntime(runs []ledger.Run) runtimeSummary {
	s := runtimeSummary{ByRuntime: map[string]*RuntimeTotals{}, ByModel: map[string]*RuntimeTotals{}, ByRole: map[string]*RuntimeTotals{}}
	parents := map[string]string{}
	for _, run := range runs {
		parents[run.RunID] = run.ParentRunID
	}
	byRun := map[string]*RunTotals{}
	s.rootOf = map[string]string{}
	for _, run := range runs {
		root := run.RunID
		for hops := 0; parents[root] != "" && hops < len(runs); hops++ {
			if _, known := parents[parents[root]]; !known {
				break
			}
			root = parents[root]
		}
		s.rootOf[run.RunID] = root
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
				{s.ByRuntime, u.Runtime}, {s.ByModel, modelKey(u)}, {s.ByRole, u.Role},
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
			if byRun[root] == nil {
				byRun[root] = &RunTotals{RunID: root}
			}
			AddRuntime(&byRun[root].RuntimeTotals, u)
		}
	}
	for _, run := range runs {
		if t := byRun[run.RunID]; t != nil {
			t.Task, t.CreatedAt = run.Task, run.CreatedAt
			s.ByRun = append(s.ByRun, t)
		}
	}
	sort.SliceStable(s.ByRun, func(i, j int) bool { return s.ByRun[i].CreatedAt > s.ByRun[j].CreatedAt })
	return s
}

// The text report opens with agent time and what Jev was used for, then
// breaks agent usage down by model, role and SDLC run. Totals for tokens,
// cost, tool calls and hook dispatches, and the other Jev breakdowns, are in
// --format json.

const usageLabelWidth = 13

type usageView struct {
	*App
	partial, missing bool
}

func (v *usageView) field(label, value string) {
	v.Outf("  %-*s%s\n", usageLabelWidth, label, value)
}

func (v *usageView) table(title string, headers []string, rows [][]string, left int) {
	right := make([]bool, len(headers))
	colored := make([]string, len(headers))
	for i, header := range headers {
		right[i] = i >= left
		colored[i] = v.Styled(v.Stdout, app.ANSICyan, header)
	}
	var b strings.Builder
	app.WriteTableAligned(&b, colored, rows, right)
	v.Outf("\n%s\n", v.Styled(v.Stdout, app.ANSICyan, title))
	for _, line := range strings.SplitAfter(strings.TrimSuffix(b.String(), "\n"), "\n") {
		v.Outf("  %s", line)
	}
	v.Outf("\n")
}

// cell is a table count that unknown of entries did not report: the total
// when all did, "—" when none did, and the partial total marked "*".
func (v *usageView) cell(known int64, unknown, entries int, format func(int64) string) string {
	switch {
	case unknown == 0:
		return format(known)
	case unknown >= entries && known == 0:
		v.missing = true
		return "—"
	default:
		v.partial = true
		return format(known) + "*"
	}
}

func (v *usageView) cost(t RuntimeTotals) string {
	if t.SuppliedCostUSD == nil {
		v.missing = true
		return "—"
	}
	if t.UnknownCost > 0 {
		v.partial = true
		return usageUSD(*t.SuppliedCostUSD) + "*"
	}
	return usageUSD(*t.SuppliedCostUSD)
}

// cacheHit is the share of context input served from cache, over the
// invocations that reported cache reads.
func cacheHit(t RuntimeTotals) string {
	if t.cachedContext == 0 {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", 100*float64(t.CacheReadTokens)/float64(t.cachedContext))
}

func unreported(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(" (%d unreported)", n)
}

// usageShort abbreviates large counts to three significant digits.
func usageShort(n int64) string {
	for _, unit := range []struct {
		size   float64
		suffix string
	}{{1e9, "B"}, {1e6, "M"}, {1e3, "K"}} {
		if n >= 10_000 && float64(n) >= unit.size {
			v := float64(n) / unit.size
			if v < 100 {
				return strconv.FormatFloat(v, 'f', 1, 64) + unit.suffix
			}
			return strconv.FormatFloat(v, 'f', 0, 64) + unit.suffix
		}
	}
	return app.FormatInt(n)
}

func usageDuration(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	switch {
	case ms == 0:
		return "0s"
	case d < time.Second:
		return "<1s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// usageUSD shows cents for real amounts and two significant digits for the
// fractions of a cent that Jev calls cost.
func usageUSD(v float64) string {
	if v == 0 || v >= 0.01 {
		return fmt.Sprintf("$%.2f", v)
	}
	decimals := int(math.Ceil(-math.Log10(v))) + 1
	return "$" + strconv.FormatFloat(v, 'f', decimals, 64)
}

func taskSummary(task string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(task), "\n")
	if r := []rune(line); len(r) > 40 {
		return strings.TrimSpace(string(r[:39])) + "…"
	}
	return line
}

// jevPurposeOrder breaks ties between purposes with equal call counts.
var jevPurposeOrder = []string{usage.PurposeSDLC, usage.PurposeCompaction, usage.PurposeSecurity, usage.PurposeMCP, usage.PurposeCLI, usage.PurposeOther}

func (v *usageView) overview(source string, jev usage.Summary, runtime runtimeSummary) {
	v.Heading("Usage")
	if source != "jev" {
		t := runtime.Totals
		if t.Invocations == 0 {
			v.field("agent time", "no SDLC agent invocations recorded")
		} else {
			runs := len(runtime.ByRun)
			plural := "s"
			if runs == 1 {
				plural = ""
			}
			v.field("agent time", fmt.Sprintf("%s%s across %s invocations in %d SDLC run%s",
				usageDuration(t.ElapsedMS), unreported(t.UnknownElapsed), app.FormatInt(int64(t.Invocations)), runs, plural))
		}
	}
	if source == "runtime" {
		return
	}
	if jev.Attempts == 0 {
		v.field("Jev calls", "no recorded calls")
		return
	}
	calls := app.FormatInt(int64(jev.Calls))
	if jev.FailedAttempts > 0 {
		calls += fmt.Sprintf(" (+%d failed)", jev.FailedAttempts)
	}
	if jev.Cost != nil {
		calls += " · ~" + usageUSD(jev.Cost.EstimatedUSD)
	}
	v.field("Jev calls", calls)
	purposes := make([]string, 0, len(jevPurposeOrder))
	for _, p := range jevPurposeOrder {
		if t := jev.ByPurpose[p]; t != nil && t.Calls > 0 {
			purposes = append(purposes, p)
		}
	}
	sort.SliceStable(purposes, func(i, j int) bool { return jev.ByPurpose[purposes[i]].Calls > jev.ByPurpose[purposes[j]].Calls })
	parts := make([]string, len(purposes))
	for i, p := range purposes {
		parts[i] = app.FormatInt(int64(jev.ByPurpose[p].Calls)) + " " + p
	}
	if len(parts) > 0 {
		v.field("Jev used for", strings.Join(parts, " · "))
	}
}

// byTime orders a breakdown by time spent, then invocations, then name.
func byTime(items map[string]*RuntimeTotals) []string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := items[keys[i]], items[keys[j]]
		if a.ElapsedMS != b.ElapsedMS {
			return a.ElapsedMS > b.ElapsedMS
		}
		if a.Invocations != b.Invocations {
			return a.Invocations > b.Invocations
		}
		return keys[i] < keys[j]
	})
	return keys
}

func (v *usageView) breakdowns(s runtimeSummary, runLimit int) {
	if s.Totals.Invocations == 0 {
		return
	}
	n := func(t RuntimeTotals) []string {
		return []string{
			app.FormatInt(int64(t.Invocations)),
			v.cell(t.ElapsedMS, t.UnknownElapsed, t.Invocations, usageDuration),
			v.cell(t.ContextInputTokens, t.UnknownInput, t.Invocations, usageShort),
			v.cell(t.OutputTokens, t.UnknownOutput, t.Invocations, usageShort),
		}
	}

	rows := [][]string{}
	for _, key := range byTime(s.ByModel) {
		t := s.ByModel[key]
		runtimes := make([]string, 0, len(t.runtimes))
		for r := range t.runtimes {
			runtimes = append(runtimes, r)
		}
		sort.Strings(runtimes)
		row := append([]string{key, strings.Join(runtimes, ", ")}, n(*t)...)
		rows = append(rows, append(row, cacheHit(*t), v.cost(*t)))
	}
	v.table("By model", []string{"MODEL", "RUNTIME", "INVOCATIONS", "TIME", "IN", "OUT", "CACHE HIT", "COST"}, rows, 2)

	rows = rows[:0:0]
	for _, key := range byTime(s.ByRole) {
		t := s.ByRole[key]
		rows = append(rows, append(append([]string{key}, n(*t)...), v.cost(*t)))
	}
	v.table("By role", []string{"ROLE", "INVOCATIONS", "TIME", "IN", "OUT", "COST"}, rows, 1)

	shown := s.ByRun
	if runLimit > 0 && len(shown) > runLimit {
		shown = shown[:runLimit]
	}
	rows = rows[:0:0]
	for _, t := range shown {
		rows = append(rows, append(append([]string{t.RunID, taskSummary(t.Task)}, n(t.RuntimeTotals)...), v.cost(t.RuntimeTotals), app.FormatInt(int64(t.JevCalls))))
	}
	title := "By SDLC run"
	if len(shown) < len(s.ByRun) {
		title = fmt.Sprintf("By SDLC run (latest %d of %d)", len(shown), len(s.ByRun))
	}
	v.table(title, []string{"RUN", "TASK", "INVOCATIONS", "TIME", "IN", "OUT", "COST", "JEV CALLS"}, rows, 2)

	notes := []string{"IN includes cached input"}
	if v.partial {
		notes = append(notes, "* some invocations didn't report this")
	}
	if v.missing {
		notes = append(notes, "— none did")
	}
	if len(shown) < len(s.ByRun) {
		notes = append(notes, "--runs 0 lists every run")
	}
	v.Outf("  %s\n", v.Styled(v.Stdout, app.ANSIDim, strings.Join(notes, " · ")))
}

func renderUsageReport(a *App, format, source string, runLimit int, jev usage.Summary, runtime runtimeSummary, hooks map[string]HookCounts) error {
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
	v := &usageView{App: a}
	v.overview(source, jev, runtime)
	if source != "jev" {
		v.breakdowns(runtime, runLimit)
	}
	return nil
}
