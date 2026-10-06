package usage

import (
	"github.com/spf13/cobra"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/usage"
)

func (a *App) usageCmd() *cobra.Command {
	var format string
	var includeFixture bool
	var source string
	var runLimit int
	c := &cobra.Command{
		Use:   "usage [--format text|json] [--source all|jev|runtime] [--runs N] [--include-fixture]",
		Short: "summarize Jev calls and agent runtime usage (reads local files only)",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if format != usage.FormatText && format != usage.FormatJSON {
				return app.Usagef("unknown --format %q (want text or json)", format)
			}
			if source != "all" && source != "jev" && source != "runtime" {
				return app.Usagef("unknown --source %q (want all, jev or runtime)", source)
			}
			if runLimit < 0 {
				return app.Usagef("--runs must be 0 (all) or more, got %d", runLimit)
			}
			recs, err := usage.ReadRecords(usage.Path(a.StateHome()))
			if err != nil {
				return app.Failf("read usage log: %v", err)
			}
			sum := usage.Aggregate(recs, usage.Filter{IncludeFixture: includeFixture}, a.Getenv)
			hookRecords, err := usage.ReadHooks(usage.HookPath(a.StateHome()))
			if err != nil {
				return app.Failf("read hook activity: %v", err)
			}
			runs, err := a.AllUsageRuns()
			if err != nil {
				return app.Failf("read runtime usage: %v", err)
			}
			a.FillElapsedFromLogs(runs)
			runtime := AggregateRuntime(runs)
			runtime.AttachJev(sum.ByRun)
			runtime.CacheDecisions = map[string]int{}
			for _, run := range runs {
				decisions, err := ledger.Open(a.SDLCRunsDir(), run.RunID).ReadDecisions()
				if err != nil {
					return app.Failf("read cache decisions: %v", err)
				}
				for _, d := range decisions {
					if d.Kind == "prompt-cache" {
						runtime.CacheDecisions[d.Choice]++
					}
				}
			}
			if err := renderUsageReport(a, format, source, runLimit, sum, runtime, AggregateHooks(hookRecords)); err != nil {
				return app.Failf("%v", err)
			}
			return nil
		},
	}
	c.Flags().StringVar(&format, "format", usage.FormatText, "output format: text or json")
	c.Flags().StringVar(&source, "source", "all", "usage source: all, jev or runtime")
	c.Flags().IntVar(&runLimit, "runs", 10, "SDLC runs to list, newest first; 0 lists all")
	c.Flags().BoolVar(&includeFixture, "include-fixture", false, "count offline fixture-transport calls too")
	return c
}
