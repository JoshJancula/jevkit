package main

import (
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
	c := &cobra.Command{
		Use:   "install <agent|all>",
		Short: "install jevkit hooks and MCP into a coding agent",
		Long: `Merge jevkit hook config and register the MCP server for one agent
(claude, cursor, codex, opencode, antigravity) or every agent detected on PATH
(all). Edits are idempotent and marker-based. A first install keeps a
.jevkit-original backup so uninstall can restore byte-exact originals.
Use --dry-run to print a unified diff without writing.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return codeErr(a.runInstall(args[0], scope, binary, dryRun, false))
		},
	}
	f := c.Flags()
	f.StringVar(&scope, "scope", "project", `install scope: "project" or "user"`)
	f.BoolVar(&dryRun, "dry-run", false, "print planned diffs without writing")
	f.StringVar(&binary, "binary", "", "jevkit binary path written into hooks (default: this executable or \"jevkit\")")
	return c
}

func (a *App) uninstallCmd() *cobra.Command {
	var scope string
	var dryRun bool
	c := &cobra.Command{
		Use:   "uninstall <agent|all>",
		Short: "remove jevkit hooks and MCP from a coding agent",
		Long: `Restore agent config from the .jevkit-original backup taken on first
install (or strip managed entries when no backup exists). Use --dry-run to
preview without writing.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return codeErr(a.runInstall(args[0], scope, "", dryRun, true))
		},
	}
	f := c.Flags()
	f.StringVar(&scope, "scope", "project", `uninstall scope: "project" or "user"`)
	f.BoolVar(&dryRun, "dry-run", false, "print planned diffs without writing")
	return c
}

func (a *App) runInstall(target, scope, binary string, dryRun, uninstall bool) int {
	scope = strings.ToLower(strings.TrimSpace(scope))
	if scope != "project" && scope != "user" {
		a.errf("jevkit: --scope must be \"project\" or \"user\"\n")
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
			rep, err = agents.UninstallAgent(agent, opts)
		} else {
			rep, err = agents.InstallAgent(agent, opts)
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
