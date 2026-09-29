package usage

import (
	"github.com/spf13/cobra"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/usage"
)

func (a *App) usageCmd() *cobra.Command {
	var format string
	var includeFixture bool
	var source string
	c := &cobra.Command{
		Use:   "usage [--format text|json] [--source all|jev|runtime] [--include-fixture]",
		Short: "summarize Jev calls and agent runtime usage (reads local files only)",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if format != usage.FormatText && format != usage.FormatJSON {
				return app.Usagef("unknown --format %q (want text or json)", format)
			}
			if source != "all" && source != "jev" && source != "runtime" {
				return app.Usagef("unknown --source %q (want all, jev or runtime)", source)
			}
			recs, err := usage.ReadRecords(usage.Path(a.StateHome()))
			if err != nil {
				return app.Failf("read usage log: %v", err)
			}
			sum := usage.Aggregate(recs, usage.Filter{IncludeFixture: includeFixture}, a.Getenv)
			runs, err := a.AllUsageRuns()
			if err != nil {
				return app.Failf("read runtime usage: %v", err)
			}
			if err := renderUsageReport(a, format, source, sum, AggregateRuntime(runs)); err != nil {
				return app.Failf("%v", err)
			}
			return nil
		},
	}
	c.Flags().StringVar(&format, "format", usage.FormatText, "output format: text or json")
	c.Flags().StringVar(&source, "source", "all", "usage source: all, jev or runtime")
	c.Flags().BoolVar(&includeFixture, "include-fixture", false, "count offline fixture-transport calls too")
	return c
}
