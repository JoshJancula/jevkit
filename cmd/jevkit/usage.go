package main

import (
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/JoshJancula/jevkit/internal/usage"
)

// stateHome is the single resolver for the Jevkit state root, independent of
// how a.StateDir was set: the default (main.go's defaultStateDir, itself
// XDG/Windows/JEVKIT_STATE_DIR-aware) already ends in "jevkit", while an
// explicit JEVKIT_STATE_DIR override may or may not. Every caller below
// joins "jevkit" back on (or delegates to a package like breaker.New,
// usage.Path, or registry.DecisionsPath that does), so stateHome() always
// returns the same directory a "jevkit" segment is about to be appended to.
// Ownership under <stateHome()>/jevkit/:
//   - sdlc/runs/<runID>/run.json, nodes/, artifacts/, events.jsonl,
//     decisions.jsonl, logs/ — internal/sdlc/ledger, one directory per run
//     (see sdlcRunsDir).
//   - usage.jsonl — internal/usage, one shared append-only log for every run
//     and every Jev call (see usage.Path).
//   - decisions.jsonl (top-level, not per-run) — internal/registry, routing
//     decisions outside any run (see registry.DecisionsPath).
//   - breaker.json — internal/breaker, one shared circuit-breaker state.
//   - redaction-audit.jsonl, redaction-review.json — internal/redact/audit,
//     shared audit/review records (see auditPath, reviewPath).
//   - jev-ask-audit.jsonl — internal/mcp, shared MCP audit trail.
//
// External runtime sessions (Claude/Codex/Cursor/OpenCode/Antigravity
// session IDs used to resume a conversation) are not files under this root
// at all: they live in the owning CLI's own native session store and are
// only referenced by ID in ledger.Run.Sessions.
func (a *App) stateHome() string {
	if filepath.Base(a.StateDir) == "jevkit" {
		return filepath.Dir(a.StateDir)
	}
	return a.StateDir
}

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
				return usagef("unknown --format %q (want text or json)", format)
			}
			if source != "all" && source != "jev" && source != "runtime" {
				return usagef("unknown --source %q (want all, jev or runtime)", source)
			}
			recs, err := usage.ReadRecords(usage.Path(a.stateHome()))
			if err != nil {
				return failf("read usage log: %v", err)
			}
			sum := usage.Aggregate(recs, usage.Filter{IncludeFixture: includeFixture}, a.getenv)
			runs, err := a.allUsageRuns()
			if err != nil {
				return failf("read runtime usage: %v", err)
			}
			if err := renderUsageReport(a, format, source, sum, aggregateRuntime(runs)); err != nil {
				return failf("%v", err)
			}
			return nil
		},
	}
	c.Flags().StringVar(&format, "format", usage.FormatText, "output format: text or json")
	c.Flags().StringVar(&source, "source", "all", "usage source: all, jev or runtime")
	c.Flags().BoolVar(&includeFixture, "include-fixture", false, "count offline fixture-transport calls too")
	return c
}
