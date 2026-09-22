package main

import (
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/OWNER/jevkit/internal/usage"
)

// stateHome is the directory holding the "jevkit" state directory that the
// usage and breaker packages append themselves.
func (a *App) stateHome() string {
	if filepath.Base(a.StateDir) == "jevkit" {
		return filepath.Dir(a.StateDir)
	}
	return a.StateDir
}

func (a *App) usageCmd() *cobra.Command {
	var format string
	var includeFixture bool
	c := &cobra.Command{
		Use:   "usage [--format text|json] [--include-fixture]",
		Short: "summarize recorded Jev calls and estimated cost (reads local files only)",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if format != usage.FormatText && format != usage.FormatJSON {
				return usagef("unknown --format %q (want text or json)", format)
			}
			recs, err := usage.ReadRecords(usage.Path(a.stateHome()))
			if err != nil {
				return failf("read usage log: %v", err)
			}
			sum := usage.Aggregate(recs, usage.Filter{IncludeFixture: includeFixture}, a.getenv)
			if err := usage.Render(a.Stdout, sum, format, false); err != nil {
				return failf("%v", err)
			}
			return nil
		},
	}
	c.Flags().StringVar(&format, "format", usage.FormatText, "output format: text or json")
	c.Flags().BoolVar(&includeFixture, "include-fixture", false, "count offline fixture-transport calls too")
	return c
}
