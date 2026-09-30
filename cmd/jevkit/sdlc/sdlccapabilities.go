package sdlc

import (
	"os/exec"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/sdlc/enrollment"
	"github.com/JoshJancula/jevkit/internal/sdlc/worker"
)

func (a *App) sdlcAgentsCapabilitiesCmd() *cobra.Command {
	var runtime string
	var details bool
	c := &cobra.Command{
		Use: "capabilities", Aliases: []string{"c", "caps"},
		Short: "show how to configure agent tools and runtime options",
		Long: `Show the personal roster path, supported tool settings, and YAML examples
for each agent. Use --runtime to focus on one CLI, or --details to inspect
CLI versions, permission defaults, hooks, and execution boundaries.

Short forms: "sdlc agents c" and "sdlc agents caps".
Hooks and MCP preferences: "sdlc i" or "sdlc int".`,
		Example: "  jevkit sdlc agents c\n  jevkit sdlc agents c -r claude\n  jevkit sdlc agents caps --details",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			matrices := worker.RuntimeMatrices()
			names := make([]string, 0, len(matrices))
			for name := range matrices {
				names = append(names, name)
			}
			sort.Strings(names)
			if runtime != "" {
				if !slices.Contains(names, runtime) {
					return app.Usagef("unknown runtime %q; choose %s", runtime, strings.Join(names, ", "))
				}
				names = []string{runtime}
			}
			lookPath := a.LookPath
			if lookPath == nil {
				lookPath = exec.LookPath
			}
			reach := make(map[string]string, len(names))
			var rows [][]string
			for _, name := range names {
				m := matrices[name]
				binary := sdlcRuntimeBinaries[name]
				status := "ready"
				if resolved, err := lookPath(binary); err != nil {
					status, reach[name] = "missing", binary+" not found on PATH"
				} else if version, err := worker.ProbeVersion(cmd.Context(), resolved, m.VersionArgs); err != nil {
					status, reach[name] = "probe failed", resolved+": "+err.Error()
				} else {
					reach[name] = resolved + " (" + version + ")"
				}
				tools := "auto only"
				if enrollment.ToolPolicySupported(name) {
					tools = "auto + mapping"
				}
				mode := "unavailable"
				if m.ReadOnlyExecution {
					mode = strings.TrimPrefix(m.ReadOnlyApprovals, sdlcRuntimeBinaries[name]+" ")
					mode = strings.TrimPrefix(mode, "--permission-mode ")
					mode = strings.TrimPrefix(mode, "--mode ")
					mode = strings.TrimPrefix(mode, "sandbox_mode=")
				}
				rows = append(rows, []string{name, status, tools, mode})
			}
			a.Heading("AGENT CONFIGURATION")
			a.Outf("  Edit your personal roster: %s\n\n", sdlcFileURL(a.sdlcRosterPath()))
			a.Table([]string{"RUNTIME", "CLI", "TOOLS", "READ-ONLY MODE"}, rows)
			a.Outf("  auto works on every runtime; mapping means shell / web / delegate.\n")
			a.Outf("  CLI status comes from version probes; --details shows paths and errors.\n")
			a.Outf("  Read-only modes above apply to runtime/model entries.\n")
			a.printSDLCToolConfiguration(runtime)
			if slices.Contains(names, "antigravity") {
				a.Outf("\n  Antigravity's default writable mode uses --dangerously-skip-permissions.\n")
			}
			if slices.Contains(names, "opencode") {
				a.Outf("  OpenCode cannot satisfy a Jevkit-enforced read-only requirement.\n")
			}
			a.Outf("\n  Hooks / MCP: jevkit sdlc i\n")
			if !details {
				a.Outf("  Runtime diagnostics: jevkit sdlc agents c --details\n")
				return nil
			}
			for _, name := range names {
				a.printSDLCRuntimeDetails(matrices[name], reach[name])
			}
			return nil
		},
	}
	c.Flags().StringVarP(&runtime, "runtime", "r", "", "show configuration for one runtime")
	c.Flags().BoolVar(&details, "details", false, "include CLI versions, hook coverage, and execution diagnostics")
	return c
}

func (a *App) printSDLCToolConfiguration(runtime string) {
	filtered := runtime != ""
	if runtime == "" {
		runtime = "codex"
	}
	a.Outf("\n")
	a.Heading("CONFIGURE ONE AGENT")
	a.Outf("  Add or edit an entry under agents: in the roster shown above.\n\n")
	a.Outf("  - id: focused-builder\n    via: runtime\n    runtime: %s\n    model: YOUR_MODEL\n    roles: [implementer]\n    rubric: Implement approved changes.\n", runtime)
	if enrollment.ToolPolicySupported(runtime) {
		a.Outf("    tools:\n      shell: true\n      web: false\n      delegate: false\n")
		a.Outf("\n  Codex / Claude: false disables a built-in tool family.\n")
		a.Outf("  true or an omitted field inherits runtime settings and approvals.\n")
		a.Outf("  To inherit everything, replace the mapping with tools: auto.\n")
	} else {
		a.Outf("    tools: auto\n\n")
	}
	a.Outf("  Cursor / OpenCode / Antigravity accept tools: auto; mappings are rejected.\n")
	a.Outf("  Omit tools to inherit too. These controls do not filter MCP or shell networking.\n")
	a.Outf("\n")
	a.Heading("SELECT A NATIVE AGENT")
	if filtered && (runtime == "codex" || runtime == "cursor") {
		a.Outf("  %s has no direct named-agent selection in this adapter.\n", runtime)
	}
	a.Outf("  On a Claude / OpenCode / Antigravity entry, set:\n\n")
	a.Outf("    agent: security-reviewer\n    tools: auto\n")
	a.Outf("\n  Configure that definition's tools in its native runtime.\n")
	a.Outf("  Jevkit uses --agent NAME; restriction mappings are rejected here.\n")
	a.Outf("\n")
	a.Heading("EXTRA CLI OPTIONS")
	exampleArgs := map[string]string{
		"codex": "--full-auto", "claude": "--effort high", "cursor": "--force",
		"opencode": "--variant high", "antigravity": "--sandbox",
	}
	a.Outf("  Every CLI entry can add runtimeArgs. For this %s entry, for example:\n\n", runtime)
	a.Outf("    runtimeArgs: '%s'\n", exampleArgs[runtime])
	a.Outf("\n  Quotes keep values together; no shell execution or variable expansion.\n")
	a.Outf("  Permission flags replace default modes; explicit readOnly and tools still apply.\n")
}

func (a *App) printSDLCRuntimeDetails(m worker.RuntimeMatrix, reach string) {
	a.Outf("\n")
	a.Heading(strings.ToUpper(m.Runtime) + " DETAILS")
	a.Outf("  CLI reach: %s\n", reach)
	if m.ReadOnlyExecution {
		a.Outf("  Read-only execution: enforced (%s)\n", m.ReadOnlyApprovals)
	} else {
		a.Outf("  Read-only execution: not enforced (%s)\n", m.ReadOnlyUnenforceable)
	}
	a.Outf("  Writable execution: %v (%s)\n", m.WritableExecution, m.WritableApprovals)
	if m.PermissionBypassArgument != "" {
		a.Outf("  Permission-bypass argument: %s\n", m.PermissionBypassArgument)
	}
	a.Outf("  Shell hook coverage: %v\n", m.ShellHookCoverage)
	a.Outf("  Session resume: %v\n", m.SessionResume)
	a.Outf("  Workdir scoped: %v\n", m.WorkdirScoped)
	a.Outf("  Cancellation / child cleanup: %v / %v\n", m.Cancellation, m.ChildCleanup)
}
