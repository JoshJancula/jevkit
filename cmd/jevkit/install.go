package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/OWNER/jevkit/internal/agents"
)

func (a *App) installCmd() *cobra.Command {
	var scope string
	var dryRun bool
	var binary string
	var componentsRaw string
	c := &cobra.Command{
		Use:   "install <agent|all>",
		Short: "install a jevkit runtime bundle into a coding agent",
		Long: `Install selected jevkit integration components for one agent
(claude, cursor, codex, opencode, antigravity) or every agent detected on PATH
(all). The default plugin bundle includes hooks and MCP. Use --components mcp
or --components hooks when you only want one of them. Edits are idempotent and marker-based. A first install keeps a
.jevkit-original backup so uninstall can restore byte-exact originals.
Use --dry-run to print a unified diff without writing.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return codeErr(a.runInstall(args[0], scope, binary, componentsRaw, dryRun, false))
		},
	}
	f := c.Flags()
	f.StringVar(&scope, "scope", "project", `install scope: "project" or "user"`)
	f.BoolVar(&dryRun, "dry-run", false, "print planned diffs without writing")
	f.StringVar(&binary, "binary", "", "jevkit binary path written into hooks (default: this executable or \"jevkit\")")
	f.StringVar(&componentsRaw, "components", "plugin", "components: plugin (hooks + MCP), mcp, hooks; comma-separated")
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
			return codeErr(a.runInstall(args[0], scope, "", componentsRaw, dryRun, true))
		},
	}
	f := c.Flags()
	f.StringVar(&scope, "scope", "project", `uninstall scope: "project" or "user"`)
	f.BoolVar(&dryRun, "dry-run", false, "print planned diffs without writing")
	f.StringVar(&componentsRaw, "components", "plugin", "components: plugin (hooks + MCP), mcp, hooks; comma-separated")
	return c
}

func (a *App) runInstall(target, scope, binary, componentsRaw string, dryRun, uninstall bool) int {
	scope = strings.ToLower(strings.TrimSpace(scope))
	if scope != "project" && scope != "user" {
		a.errf("jevkit: --scope must be \"project\" or \"user\"\n")
		return exitUsage
	}
	components, err := parseComponents(componentsRaw)
	if err != nil {
		a.errf("jevkit: %v\n", err)
		return exitUsage
	}
	list, err := agents.SelectAgents(target, a.lookPath(), target == "all")
	if err != nil {
		a.errf("jevkit: %v\n", err)
		return exitFail
	}
	opts := agents.InstallOptions{
		WorkDir:   a.WorkDir,
		ConfigDir: a.homeDir(),
		Scope:     scope,
		Binary:    a.resolveBinary(binary),
		DryRun:    dryRun,
	}
	if scope == "project" && opts.WorkDir == "" {
		a.errf("jevkit: project scope requires a working directory\n")
		return exitFail
	}
	if scope == "user" && opts.ConfigDir == "" {
		a.errf("jevkit: user scope requires a home directory\n")
		return exitFail
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
			a.errf("jevkit: %s %s: %v\n", verb, agent.Name(), err)
			failed++
			continue
		}
		diffs := agents.FormatPreviews(rep.Previews)
		if dryRun {
			if diffs == "" {
				a.outf("%s %s (%s): no changes\n", verb, agent.Name(), scope)
			} else {
				a.outf("%s %s (%s) dry-run:\n%s", verb, agent.Name(), scope, diffs)
			}
			continue
		}
		if diffs == "" {
			a.outf("%s %s (%s): already up to date\n", verb, agent.Name(), scope)
		} else {
			a.outf("%s %s (%s): ok\n", verb, agent.Name(), scope)
		}
	}
	if failed > 0 {
		return exitFail
	}
	return exitOK
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

func (a *App) lookPath() func(string) (string, error) {
	if a.LookPath != nil {
		return a.LookPath
	}
	return exec.LookPath
}

func (a *App) homeDir() string {
	if a.HomeDir != "" {
		return a.HomeDir
	}
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return ""
}

func (a *App) resolveBinary(flag string) string {
	if flag != "" {
		return flag
	}
	if a.Binary != "" {
		return a.Binary
	}
	if exe, err := os.Executable(); err == nil {
		if abs, err := filepath.Abs(exe); err == nil {
			return abs
		}
		return exe
	}
	return "jevkit"
}
