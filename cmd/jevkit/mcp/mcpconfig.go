package mcp

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/breaker"
	"github.com/JoshJancula/jevkit/internal/jev"
	"github.com/JoshJancula/jevkit/internal/keystore"
	jevmcp "github.com/JoshJancula/jevkit/internal/mcp"
	"github.com/JoshJancula/jevkit/internal/registry"
)

// mcpPreflight lists reasons the server would answer available:false or fail.
// None of them is fatal: agents fall back to native behavior.
func (a *App) mcpPreflight(ctx context.Context) []string {
	var problems []string
	cfg, err := a.JevConfig()
	if err != nil {
		problems = append(problems, fmt.Sprintf("model setting: %v", err))
	}
	if cfg.Transport != jev.TransportFixture && a.Store().Source(ctx) == keystore.SourceNone {
		problems = append(problems, "no API key found; tools will return available:false (run `jevkit key set`)")
	}
	br := a.Breaker
	if br == nil {
		br = breaker.New(a.StateHome())
	}
	if st := br.Load(); st.Open {
		problems = append(problems, fmt.Sprintf("circuit breaker is open (%s); tools will return available:false", app.OrUnknown(st.Reason)))
	}
	if _, err := registry.Load(); err != nil {
		problems = append(problems, fmt.Sprintf("registry failed to load: %v", err))
	}
	return problems
}

func (a *App) mcpStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "report whether the MCP server can reach Jev (key source, breaker)",
		Long: `Report the key source and the preflight checks the MCP server relies on.
Failed checks are reported with a reason but never change the exit status: the
server still starts and returns available:false. The key is never printed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			a.Outf("server:         jevkit mcp start (stdio)\n")
			a.Outf("key source:     %s\n", a.Store().Source(ctx))
			problems := a.mcpPreflight(ctx)
			if len(problems) == 0 {
				a.Outf("preflight:      ok\n")
				return nil
			}
			for _, p := range problems {
				a.Outf("preflight:      WARN %s\n", p)
			}
			return nil
		},
	}
}

func (a *App) mcpConfigCmd() *cobra.Command {
	var merge, name, command string
	c := &cobra.Command{
		Use:   "config",
		Short: "print (or --merge into a client config) the MCP server entry",
		Long: `Print a client config (such as .mcp.json) with the jevkit server entry, or
merge the entry into an existing file with --merge, keeping all other
settings. Merging is idempotent. The entry never contains an API key: the
server resolves it itself. Failed preflight checks are warnings on stderr and
do not change the exit status.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			for _, p := range a.mcpPreflight(cmd.Context()) {
				a.Errf("jevkit: warning: %s\n", p)
			}
			if merge == "" {
				doc, err := jevmcp.ConfigDocument(name, command)
				if err != nil {
					return app.Failf("%v", err)
				}
				a.Outf("%s", doc)
				return nil
			}
			changed, err := jevmcp.WriteConfigFile(merge, name, command)
			if err != nil {
				return app.Failf("merge %s: %v", merge, err)
			}
			if changed {
				a.Outf("updated %s\n", merge)
			} else {
				a.Outf("%s already up to date\n", merge)
			}
			return nil
		},
	}
	f := c.Flags()
	f.StringVar(&merge, "merge", "", "merge the entry into this client config file (created if missing)")
	f.StringVar(&name, "name", jevmcp.DefaultServerName, "server name under mcpServers")
	f.StringVar(&command, "command", "jevkit", "command that launches the server")
	return c
}
