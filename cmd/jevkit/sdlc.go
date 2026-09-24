package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/OWNER/jevkit/internal/sdlc/adaptive"
	"github.com/OWNER/jevkit/internal/sdlc/catalog"
	"github.com/OWNER/jevkit/internal/sdlc/enrollment"
	"github.com/OWNER/jevkit/internal/sdlc/spec"
)

// sdlcCmd groups the SDLC workflow commands.
func (a *App) sdlcCmd() *cobra.Command {
	c := a.group("sdlc", "run work with enrolled agents and optional decision workflows",
		a.sdlcValidateCmd(), a.sdlcExplainCmd(), a.sdlcListCmd(), a.sdlcCreateCmd(),
		a.sdlcAgentsCmd(), a.sdlcDoctorCmd(), a.sdlcRunCmd(), a.sdlcResumeCmd(),
		a.sdlcInitCmd(), a.sdlcStartCmd(), a.sdlcNextCmd(), a.sdlcReportCmd(), a.sdlcDriveCmd())
	c.Long = `Run an SDLC task with enrolled agents.

Normal CLI flow:
  jevkit sdlc agents
  jevkit sdlc doctor --policy lean
  jevkit sdlc run feature --task "..."

Run starts and executes a new task. Resume continues an active run by ID.
Create writes an optional custom workflow file with authored questions and
routes. Built-in task kinds need no workflow file. Agents discover is optional
inventory; you can add your own agent directly.`
	return c
}

// sdlcCatalogPaths returns the paths sdlcAgentsCmd loads from: the project's
// and user's native Claude Code agent directories, the project runtime
// ledger, and the user-level ledger override.
func (a *App) sdlcCatalogPaths() (projectAgents, userAgents, projectLedger, userLedger string) {
	if a.WorkDir != "" {
		projectAgents = filepath.Join(a.WorkDir, ".claude", "agents")
		projectLedger = filepath.Join(a.WorkDir, ".jevkit", "sdlc", "agents.yaml")
	}
	if a.HomeDir != "" {
		userAgents = filepath.Join(a.HomeDir, ".claude", "agents")
	}
	if a.ConfigDir != "" {
		userLedger = filepath.Join(a.ConfigDir, "sdlc", "agents.yaml")
	}
	return
}

// loadCatalog builds the merged agent catalog from every configured source.
func (a *App) loadCatalog() (*catalog.Catalog, error) {
	projectAgents, userAgents, projectLedgerPath, userLedgerPath := a.sdlcCatalogPaths()
	native := catalog.DiscoverNative(projectAgents, userAgents)
	var ledgers []catalog.LedgerFile
	if projectLedgerPath != "" {
		l, err := catalog.LoadLedger(projectLedgerPath)
		if err != nil {
			return nil, err
		}
		ledgers = append(ledgers, l)
	}
	c, err := catalog.Merge(native, ledgers...)
	if err != nil {
		return nil, err
	}
	if userLedgerPath != "" {
		override, err := catalog.LoadLedger(userLedgerPath)
		if err != nil {
			return nil, err
		}
		if len(override.Agents) > 0 {
			if err := c.ApplyUserOverride(override); err != nil {
				return nil, err
			}
		}
	}
	return c, nil
}

func (a *App) sdlcAgentsCmd() *cobra.Command {
	list := &cobra.Command{
		Use:   "agents",
		Short: "show your agent roster and manage agent setup",
		Long: `Show your personal SDLC roster and create editable agent examples when
the roster does not exist. Each agent declares roles it can perform and a
rubric describing when Jev should choose it. Several agents can share a role;
Jev selects among eligible agents for each SDLC step. Entries with
disabled: true cannot receive work.`,
		Example: "  jevkit sdlc agents",
		Args:    cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if err := a.ensureSDLCRoster(); err != nil {
				return failf("%v", err)
			}
			roster, err := enrollment.LoadRoster(a.sdlcRosterPath())
			if err != nil {
				return failf("%v", err)
			}
			a.heading("SDLC AGENTS")
			a.outf("  Roster: %s\n", sdlcFileURL(a.sdlcRosterPath()))
			a.outf("\n")
			a.heading("DEFAULT CLI RUN (lean policy)")
			a.outf("  Needs 1 planner, 1 implementer, and 1 assessor.\n")
			a.outf("  Multiple agents can share a role; Jev chooses among them by rubric.\n")
			a.outf("  One agent can also fill several roles.\n")
			reachable := a.cliReach().Runtimes
			runtimes := make([]string, 0, len(reachable))
			for runtime := range reachable {
				runtimes = append(runtimes, runtime)
			}
			sort.Strings(runtimes)
			if len(runtimes) == 0 {
				a.outf("  CLI apps on PATH: none; install codex, claude, cursor-agent, or opencode first.\n")
			} else {
				a.outf("  CLI apps on PATH: %s\n", strings.Join(runtimes, ", "))
			}
			if len(roster.Agents) == 0 {
				a.outf("\n")
				a.heading("YOUR AGENTS")
				a.outf("  none\n\n")
				a.heading("ADD AN AGENT")
				exampleRuntime := "codex"
				if len(runtimes) > 0 && !slices.Contains(runtimes, "codex") {
					exampleRuntime = runtimes[0]
				}
				a.outf("  jevkit sdlc agents add %s --model MODEL --rubric \"General work\" --role all\n", exampleRuntime)
				a.outf("  Or name one: jevkit sdlc agents add my-reviewer --runtime codex --model MODEL --rubric \"Review code\" --role assessor\n")
				a.outf("  MODEL is a model supported by that CLI. Add writes to this roster file.\n")
				a.outf("  Optional inventory: jevkit sdlc agents discover\n")
				a.outf("  Verify setup: jevkit sdlc doctor --policy lean\n")
				return nil
			}
			a.outf("\n")
			a.heading("YOUR AGENTS")
			needsSetup := false
			rows := make([][]string, 0, len(roster.Agents))
			for _, ag := range roster.Agents {
				binding := ag.Via
				if ag.Via == "runtime" {
					binding = ag.Runtime + " / " + ag.Model
					if ag.RuntimeAgent != "" {
						binding += " / " + ag.RuntimeAgent
					}
				} else if ag.Via == "native" {
					binding += " / " + ag.Subagent
				}
				status := "configured"
				if ag.Disabled {
					status = "inactive template"
					needsSetup = true
				} else if !ag.Ready() {
					status = "needs model"
					needsSetup = true
				}
				if status == "configured" {
					status = a.styled(a.Stdout, ansiGreen, status)
				} else {
					status = a.styled(a.Stdout, ansiYellow, status)
				}
				rows = append(rows, []string{ag.ID, strings.Join(ag.Roles, ", "), binding, status})
			}
			a.table([]string{"AGENT", "ROLES", "BINDING", "STATUS"}, rows)
			a.outf("\n")
			a.heading("WHEN TO CHOOSE")
			for _, ag := range roster.Agents {
				a.outf("  %-20s %s\n", ag.ID, firstLine(ag.Rubric))
			}
			if needsSetup {
				a.outf("\n")
				a.heading("EDIT THE ROSTER")
				a.outf("  Set a supported model for each CLI, then disabled: false for each agent to use.\n")
			}
			a.outf("\nCheck eligibility: jevkit sdlc doctor --policy lean\n")
			return nil
		},
	}
	list.AddCommand(a.sdlcAgentsAddCmd(), a.sdlcAgentsDiscoverCmd(), a.sdlcAgentsEnrollCmd())
	return list
}

func (a *App) sdlcListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "show task kinds, optional workflows, and setup status",
		Example: "  jevkit sdlc list",
		Args:    cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			all, err := a.sdlcAvailableWorkflows()
			if err != nil {
				return failf("%v", err)
			}
			lean, leanErr := a.sdlcPreflight("lean", a.cliReach())
			leanIssue := ""
			if leanErr != nil {
				leanIssue = leanErr.Error()
			} else if len(lean.Missing) > 0 {
				leanIssue = strings.Join(lean.Missing, "; ")
			}
			type projectItem struct{ name, path, status, detail string }
			var projects []projectItem
			projectByName := map[string]string{}
			for _, wf := range all {
				if wf.Builtin {
					continue
				}
				startName := strings.TrimSuffix(filepath.Base(wf.Path), filepath.Ext(wf.Path))
				projectByName[startName] = wf.Path
				entry := projectItem{name: startName, path: sdlcDisplayPath(a.WorkDir, wf.Path), status: "ready"}
				if err := a.validateSpawnTargets(wf); err != nil {
					entry.status, entry.detail = "blocked", err.Error()
				} else if leanErr != nil {
					entry.status, entry.detail = "blocked", "Project policy does not allow a lean run"
				} else if leanIssue != "" {
					entry.status = "setup"
				}
				if wf.Name != startName {
					if entry.detail != "" {
						entry.detail += "; "
					}
					entry.detail += "YAML name: " + wf.Name
				}
				projects = append(projects, entry)
			}
			sort.Slice(projects, func(i, j int) bool { return projects[i].name < projects[j].name })
			a.heading("TASK KINDS")
			kindRows := make([][]string, 0, len(adaptive.TaskKinds))
			for _, name := range adaptive.TaskKinds {
				override := ""
				if projectByName[name] != "" {
					override = " (project stage workflow)"
				}
				kindRows = append(kindRows, []string{name, adaptive.TaskKindSummary(name) + override})
			}
			a.table([]string{"KIND", "PURPOSE"}, kindRows)
			a.outf("\n")
			a.heading("OPTIONAL PROJECT WORKFLOWS (lean policy)")
			a.outf("  Create one when you want to author questions and routes.\n")
			if len(projects) == 0 {
				a.outf("  none\n")
			} else {
				rows := make([][]string, 0, len(projects))
				for _, entry := range projects {
					status := entry.status
					if status == "ready" {
						status = a.styled(a.Stdout, ansiGreen, status)
					} else {
						status = a.styled(a.Stdout, ansiYellow, status)
					}
					rows = append(rows, []string{entry.name, status, entry.path})
				}
				a.table([]string{"NAME", "STATUS", "FILE"}, rows)
				for _, entry := range projects {
					if entry.detail != "" {
						a.outf("  %s: %s\n", entry.name, entry.detail)
					}
				}
			}
			if leanIssue != "" {
				a.outf("\n")
				a.heading("SETUP")
				if strings.Contains(leanIssue, "no active agents") {
					a.outf("  Edit the planner, implementer, and assessor entries in: %s\n", sdlcFileURL(a.sdlcRosterPath()))
					a.outf("  Set each model and disabled: false, then run: jevkit sdlc doctor --policy lean\n")
				} else if strings.Contains(leanIssue, "no agents enrolled") {
					a.outf("  Default lean needs a planner, implementer, and assessor.\n")
					a.outf("  One reachable CLI agent can cover all three roles:\n")
					a.outf("  jevkit sdlc agents add codex --model MODEL --rubric \"General work\" --role all\n")
					a.outf("  MODEL must be supported by the codex CLI.\n")
					a.outf("  Optional inventory: jevkit sdlc agents discover\n")
					a.outf("  Verify: jevkit sdlc doctor --policy lean\n")
				} else {
					a.outf("  Lean policy needs attention.\n  Run: jevkit sdlc doctor --policy lean\n")
				}
			}
			exampleKind := ""
			for _, name := range []string{"feature", "bugfix", "review", "release"} {
				if projectByName[name] == "" {
					exampleKind = name
					break
				}
			}
			if exampleKind == "" {
				for _, entry := range projects {
					exampleKind = entry.name
					break
				}
			}
			a.outf("\n")
			a.heading("RUN A TASK")
			if exampleKind != "" {
				a.outf("  jevkit sdlc run %s --task \"...\"\n", exampleKind)
			} else {
				a.outf("  jevkit sdlc run --task \"...\"  (selects a built-in task kind)\n")
			}
			if len(projects) > 0 {
				a.outf("  Custom stages: jevkit sdlc run NAME --task \"...\"\n")
			}
			a.outf("  Existing run:  jevkit sdlc resume RUN_ID\n")
			return nil
		},
	}
}

func sdlcDisplayPath(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return rel
	}
	return path
}

func (a *App) sdlcCreateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "create <name>",
		Short: "write a custom workflow YAML you can edit and run",
		Long: `Create a starter YAML file under .jevkit/sdlc/ for a project-specific
decision flow. Edit its question prompts, answer choices, routes, and work
objectives. Work stages use standard planner, implementer, or assessor roles;
agent enrollment and project policy still control who can perform them. A
choice can also route to a spawn stage that runs another SDLC workflow.

Built-in feature, bugfix, review, and release use their own adaptive flow.
See docs/SDLC-WORKFLOWS.md for the stage format and a worked example.

Choose a new name; create does not overwrite a built-in task kind.`,
		Example: "  jevkit sdlc create custom-review\n  jevkit sdlc explain custom-review\n  jevkit sdlc run custom-review --task \"...\"",
		Args:    cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name := args[0]
			if !regexp.MustCompile(`^[a-z][a-z0-9-]*$`).MatchString(name) {
				return usagef("workflow name must use lowercase letters, digits and hyphens, starting with a letter")
			}
			if !isProjectWorkflowFile(name + ".yaml") {
				return usagef("%q is reserved for SDLC configuration; choose another workflow name", name)
			}
			for _, kind := range adaptive.TaskKinds {
				if kind == name {
					return usagef("%q is a built-in task kind; use sdlc run %s directly, or choose a distinct custom workflow name", name, name)
				}
			}
			raw := []byte(fmt.Sprintf(`# Custom SDLC stages: Jev answers questions; enrolled agents do work.
# A choice may route to a spawn stage that runs another SDLC, such as bugfix.
# See docs/SDLC-WORKFLOWS.md for an example.
version: 1
name: %s
description: Ask whether the request is ready, then plan, implement, and assess.
entry: scope
maxSteps: 20
stages:
  - id: scope
    question:
      prompt: Is there enough information to begin this task?
      options:
        ready: The task has a clear goal and enough context to plan.
        unclear: A requirement or constraint needs clarification first.
      routes: { ready: plan, unclear: needs-context }
      fallback: needs-context

  - id: plan
    work:
      role: planner
      objective: Plan the requested change and its acceptance checks.
      routes: { planned: implement, answer: done, no-change: done }

  - id: implement
    work:
      role: implementer
      objective: Implement the approved plan in the repository.
      routes: { changed: assess, answer: done, no-change: done }

  - id: assess
    work:
      role: assessor
      objective: Assess the exact diff against the plan.
      routes: { approved: done, changes-required: implement }

  - id: needs-context
    finish: paused
  - id: done
    finish: succeeded
`, name))
			path := filepath.Join(a.sdlcWorkflowDir(), name+".yaml")
			if _, err := os.Stat(path); err == nil {
				return failf("%s already exists", path)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return failf("create workflow directory: %v", err)
			}
			if err := writeAtomic(path, raw, 0o600); err != nil {
				return failf("write workflow: %v", err)
			}
			a.outf("created stage workflow %s\nInspect: jevkit sdlc explain %s\nValidate: jevkit sdlc validate %s\nRun: jevkit sdlc run %s --task \"...\"\n", path, name, name, name)
			return nil
		},
	}
}

// Keep old scripts working without showing two names for the same action.
func (a *App) sdlcInitCmd() *cobra.Command {
	c := a.sdlcCreateCmd()
	c.Use = "init <name>"
	c.Hidden = true
	return c
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}

func (a *App) loadWorkflowFile(path string) (*spec.Workflow, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var shape struct {
		Stages []yaml.Node `yaml:"stages"`
	}
	if err := yaml.Unmarshal(raw, &shape); err != nil {
		return nil, fmt.Errorf("parse workflow: %w", err)
	}
	if len(shape.Stages) == 0 {
		return nil, fmt.Errorf("project workflow must define stages; create one with `jevkit sdlc create NAME`")
	}
	w, err := spec.Load(raw)
	if err != nil {
		return nil, err
	}
	return w, nil
}

func (a *App) sdlcValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "validate <workflow.yaml|name>",
		Short:   "check a workflow file and its decision routes",
		Example: "  jevkit sdlc validate .jevkit/sdlc/ship-feature.yaml\n  jevkit sdlc validate feature",
		Args:    cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			wf, err := a.resolveWorkflowByName(args[0])
			if err != nil {
				return failf("%v", err)
			}
			if wf.Builtin {
				a.outf("%s: built-in adaptive task kind; no workflow file to validate\n", wf.Name)
				return nil
			}
			if err := a.validateSpawnTargets(wf); err != nil {
				return failf("%v", err)
			}
			w := wf.W
			a.outf("%s: ok (%d stages, entry %s, max %d steps)\n", w.Name, len(w.Stages), w.Entry, w.MaxSteps)
			return nil
		},
	}
}

func (a *App) sdlcExplainCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "explain <workflow.yaml|name>",
		Short: "show a compact flow chart for a task kind or project workflow",
		Long: `For a built-in task kind, show the adaptive plan, implement, and
assess loop. For a custom stage workflow, show each authored Jev question,
answer route, SDLC work role, and child workflow.`,
		Example: "  jevkit sdlc explain feature\n  jevkit sdlc explain .jevkit/sdlc/ship-feature.yaml",
		Args:    cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			wf, err := a.resolveWorkflowByName(args[0])
			if err != nil {
				return failf("%v", err)
			}
			if wf.Builtin {
				a.sdlcExplainAdaptive(wf.Name)
				return nil
			}
			a.sdlcExplainStages(wf.W)
			return nil
		},
	}
	return c
}
