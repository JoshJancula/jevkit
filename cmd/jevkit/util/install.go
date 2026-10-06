package util

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/agents"
)

func (a *App) installCmd() *cobra.Command {
	var scope string
	var dryRun bool
	var binary string
	var componentsRaw string
	var injectionGuard bool
	c := &cobra.Command{
		Use:   "install <agent|all>",
		Short: "install a jevkit runtime bundle into a coding agent",
		Long: `Install selected jevkit integration components for one supported agent
(claude, codex, opencode, cursor, antigravity) or every supported agent detected on PATH
(all). The default plugin bundle includes hooks and MCP. Use --components mcp
or --components hooks when you only want one of them. Edits are idempotent and marker-based. A first install keeps a
.jevkit-original backup so uninstall can restore byte-exact originals.
Use --dry-run to print a unified diff without writing.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.CodeErr(a.runInstall(args[0], scope, binary, componentsRaw, dryRun, false, injectionGuard))
		},
	}
	f := c.Flags()
	f.StringVar(&scope, "scope", "project", `install scope: "project" or "user"`)
	f.BoolVar(&dryRun, "dry-run", false, "print planned diffs without writing")
	f.StringVar(&binary, "binary", "", "jevkit binary path written into hooks (default: this executable or \"jevkit\")")
	f.StringVar(&componentsRaw, "components", "plugin", "components: plugin (hooks + MCP), mcp, hooks; comma-separated")
	f.BoolVar(&injectionGuard, "injection-guard", false, "install broad pre-tool and post-tool hooks for prompt-injection review")
	return c
}

func (a *App) uninstallCmd() *cobra.Command {
	var scope string
	var dryRun bool
	var componentsRaw string
	c := &cobra.Command{
		Use:   "uninstall <agent|all>",
		Short: "remove selected jevkit components from a coding agent",
		Long: `Restore agent config from the .jevkit-original backup taken on first
install (or strip managed entries when no backup exists). Use --dry-run to
preview without writing.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.CodeErr(a.runInstall(args[0], scope, "", componentsRaw, dryRun, true, false))
		},
	}
	f := c.Flags()
	f.StringVar(&scope, "scope", "project", `uninstall scope: "project" or "user"`)
	f.BoolVar(&dryRun, "dry-run", false, "print planned diffs without writing")
	f.StringVar(&componentsRaw, "components", "plugin", "components: plugin (hooks + MCP), mcp, hooks; comma-separated")
	return c
}

func (a *App) runInstall(target, scope, binary, componentsRaw string, dryRun, uninstall, injectionGuard bool) int {
	target = strings.ToLower(strings.TrimSpace(target))
	scope = strings.ToLower(strings.TrimSpace(scope))
	if scope != "project" && scope != "user" {
		a.Errf("jevkit: --scope must be \"project\" or \"user\"\n")
		return app.ExitUsage
	}
	components, err := parseComponents(componentsRaw)
	if err != nil {
		a.Errf("jevkit: %v\n", err)
		return app.ExitUsage
	}
	list, err := agents.SelectAgents(target, a.LookPathFunc(), target == "all")
	if err != nil {
		a.Errf("jevkit: %v\n", err)
		return app.ExitFail
	}
	if !uninstall {
		list = agents.SupportedInstallAgents(list)
		if len(list) == 0 {
			a.Errf("jevkit: no supported agents detected on PATH; pass an agent name explicitly\n")
			return app.ExitFail
		}
	}
	opts := agents.InstallOptions{
		WorkDir:        a.WorkDir,
		ConfigDir:      a.UserHome(),
		Scope:          scope,
		Binary:         a.ResolveBinary(binary),
		DryRun:         dryRun,
		InjectionGuard: injectionGuard,
	}
	if scope == "project" && opts.WorkDir == "" {
		a.Errf("jevkit: project scope requires a working directory\n")
		return app.ExitFail
	}
	if scope == "user" && opts.ConfigDir == "" {
		a.Errf("jevkit: user scope requires a home directory\n")
		return app.ExitFail
	}

	verb := "install"
	if uninstall {
		verb = "uninstall"
	}
	var failed int
	for _, agent := range list {
		var rep agents.ApplyReport
		var err error
		if uninstall {
			rep, err = agents.UninstallAgentComponents(agent, opts, components)
		} else {
			rep, err = agents.InstallAgentComponents(agent, opts, components)
		}
		if err != nil {
			a.Errf("jevkit: %s %s: %v\n", verb, agent.Name(), err)
			failed++
			continue
		}
		diffs := agents.FormatPreviews(rep.Previews)
		if dryRun {
			if diffs == "" {
				a.Outf("%s %s (%s): no changes\n", verb, agent.Name(), scope)
			} else {
				a.Outf("%s %s (%s) dry-run:\n%s", verb, agent.Name(), scope, diffs)
			}
			continue
		}
		if diffs == "" {
			a.Outf("%s %s (%s): already up to date\n", verb, agent.Name(), scope)
		} else {
			a.Outf("%s %s (%s): ok\n", verb, agent.Name(), scope)
		}
	}
	if failed > 0 {
		return app.ExitFail
	}
	return app.ExitOK
}

func parseComponents(raw string) (agents.Components, error) {
	var out agents.Components
	seen := map[string]bool{}
	for _, value := range strings.Split(raw, ",") {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" || seen[value] {
			return agents.Components{}, fmt.Errorf("--components must be a comma-separated list of plugin, mcp, hooks")
		}
		seen[value] = true
		switch value {
		case "plugin":
			out = agents.DefaultComponents()
		case "mcp":
			out.MCP = true
		case "hooks":
			out.Hooks = true
		default:
			return agents.Components{}, fmt.Errorf("unknown component %q (want plugin, mcp, hooks)", value)
		}
	}
	if !out.Hooks && !out.MCP {
		return agents.Components{}, fmt.Errorf("--components must select plugin, mcp, or hooks")
	}
	return out, nil
}
