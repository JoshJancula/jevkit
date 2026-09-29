package sdlc

import (
	"fmt"
	"os"
	"sort"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/usage"
	"github.com/spf13/cobra"
)

func (a *App) sdlcUsageCmd() *cobra.Command {
	return &cobra.Command{Use: "usage [run-id]", Short: "show measured agent usage for a run tree or all saved runs", Args: cobra.MaximumNArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		var runs []ledger.Run
		if len(args) == 1 {
			var err error
			runs, err = a.sdlcTree(args[0])
			if err != nil {
				return err
			}
		} else {
			entries, err := os.ReadDir(a.SDLCRunsDir())
			if err != nil {
				return err
			}
			for _, e := range entries {
				if !e.IsDir() || !app.RunIDPattern.MatchString(e.Name()) {
					continue
				}
				r, err := ledger.Open(a.SDLCRunsDir(), e.Name()).ReadRun()
				if err == nil {
					runs = append(runs, r)
				}
			}
			sort.Slice(runs, func(i, j int) bool { return runs[i].CreatedAt < runs[j].CreatedAt })
		}
		var input, output, cacheRead, cacheCreate, toolCalls int64
		unknownInput, unknownOutput, unknownCacheRead, unknownCacheCreate, unknownCost, unknownToolCalls := 0, 0, 0, 0, 0, 0
		for _, r := range runs {
			seen := map[string]ledger.InvocationUsage{}
			for _, u := range r.Usage {
				seen[u.Invocation] = u
			}
			keys := make([]string, 0, len(seen))
			for key := range seen {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				u := seen[key]
				_, _ = fmt.Fprintf(a.Stdout, "%s %s %s/%s via %s/%s: input %s, output %s, cache-read %s, cache-creation %s, tool calls %s",
					r.RunID, u.Invocation, u.Agent, u.Role, u.Runtime, u.Model,
					tokenCount(u.InputTokens), tokenCount(u.OutputTokens), tokenCount(u.CacheReadTokens), tokenCount(u.CacheCreationTokens), tokenCount(u.ToolCalls))
				if u.CostUSD != nil {
					_, _ = fmt.Fprintf(a.Stdout, ", cost $%.4f", *u.CostUSD)
				} else {
					_, _ = fmt.Fprintf(a.Stdout, ", cost unknown")
					unknownCost++
				}
				if u.UsageProvenance != "" {
					_, _ = fmt.Fprintf(a.Stdout, " [%s]", u.UsageProvenance)
				}
				_, _ = fmt.Fprintln(a.Stdout)
				if u.InputTokens == nil {
					unknownInput++
				} else {
					input += *u.InputTokens
				}
				if u.ToolCalls == nil {
					unknownToolCalls++
				} else {
					toolCalls += *u.ToolCalls
				}
				if u.OutputTokens == nil {
					unknownOutput++
				} else {
					output += *u.OutputTokens
				}
				if u.CacheReadTokens == nil {
					unknownCacheRead++
				} else {
					cacheRead += *u.CacheReadTokens
				}
				if u.CacheCreationTokens == nil {
					unknownCacheCreate++
				} else {
					cacheCreate += *u.CacheCreationTokens
				}
			}
		}
		_, _ = fmt.Fprintf(a.Stdout, "Measured total: input %s (%d unknown), output %s (%d unknown), cache-read %s (%d unknown), cache-creation %s (%d unknown), tool calls %s (%d unknown), cost (%d unknown)\n",
			app.FormatInt(input), unknownInput, app.FormatInt(output), unknownOutput, app.FormatInt(cacheRead), unknownCacheRead, app.FormatInt(cacheCreate), unknownCacheCreate, app.FormatInt(toolCalls), unknownToolCalls, unknownCost)
		records, err := usage.ReadRecords(usage.Path(a.StateHome()))
		if err != nil {
			return err
		}
		ids := map[string]bool{}
		for _, r := range runs {
			ids[r.RunID] = true
		}
		var linked []usage.Record
		for _, rec := range records {
			if ids[rec.RunID] {
				linked = append(linked, rec)
			}
		}
		_, _ = fmt.Fprintln(a.Stdout)
		return usage.Render(a.Stdout, usage.Aggregate(linked, usage.Filter{}, a.Getenv), usage.FormatText, false)
	}}
}

func tokenCount(v *int64) string {
	if v == nil {
		return "unknown"
	}
	return app.FormatInt(*v)
}
