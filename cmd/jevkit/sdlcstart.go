package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/OWNER/jevkit/internal/breaker"
	"github.com/OWNER/jevkit/internal/jev"
	"github.com/OWNER/jevkit/internal/keystore"
	"github.com/OWNER/jevkit/internal/redact"
	"github.com/OWNER/jevkit/internal/redact/config"
	"github.com/OWNER/jevkit/internal/registry"
	"github.com/OWNER/jevkit/internal/sdlc/adaptive"
	"github.com/OWNER/jevkit/internal/sdlc/ledger"
	"github.com/OWNER/jevkit/internal/sdlc/route"
	"github.com/OWNER/jevkit/internal/sdlc/seed"
	"github.com/OWNER/jevkit/internal/sdlc/spec"
	"github.com/OWNER/jevkit/internal/sdlc/stageflow"
)

// sdlcRunsDir is where run ledgers live under StateDir.
func (a *App) sdlcRunsDir() string { return filepath.Join(a.stateHome(), "jevkit", "sdlc", "runs") }

// sdlcWorkflowDir is the project's authored-workflow directory. There is no
// built-in embedded graph set or user-level workflow directory yet (that is
// the plan's phase 7); only .jevkit/sdlc/*.yaml under the project is
// resolved today.
func (a *App) sdlcWorkflowDir() string { return filepath.Join(a.WorkDir, ".jevkit", "sdlc") }

// sdlcRouter wires a route.Router the same way mcpServer wires the MCP
// server: same keystore, breaker, registry and redaction config, so
// availability and shadow-mode behave identically across both surfaces.
func (a *App) sdlcRouter() (*route.Router, error) {
	reg, err := registry.Load()
	if err != nil {
		return nil, err
	}
	cfg, err := a.jevConfig()
	if err != nil {
		return nil, err
	}
	store := a.store()
	br := a.Breaker
	if br == nil {
		br = breaker.New(a.stateHome())
	}
	var client route.Asker
	keyFn := func() (string, error) { k, _, err := store.Resolve(context.Background()); return k, err }
	if a.NewJev != nil {
		client = a.recordJev(a.NewJev(cfg, keyFn), cfg)
	} else {
		c := jev.New(cfg, keyFn)
		c.Breaker = br
		client = a.recordJev(c, cfg)
	}
	loadRedactor := func() (*redact.Redactor, error) {
		rc, err := config.Load(a.loadOptions())
		if err != nil {
			return nil, err
		}
		if k, err := keyFn(); err == nil {
			rc.Options.Key = k
		}
		return rc.Redactor()
	}
	return route.New(route.Config{
		Decider: &registry.Decider{Registry: reg, StateDir: a.stateHome(), Getenv: a.getenv},
		Client:  client,
		Redact: func(text string) (string, []redact.Hit, error) {
			r, err := loadRedactor()
			if err != nil {
				return "", nil, err
			}
			res, err := r.Apply(text)
			return res.Text, res.Hits, err
		},
		Unavailable: func(ctx context.Context) string {
			switch {
			case br.IsOpen():
				return "breaker-open"
			case cfg.Transport != jev.TransportFixture && store.Source(ctx) == keystore.SourceNone:
				return "no-key"
			}
			return ""
		},
	})
}

// availableWorkflow is one workflow sdlc start can select among. Path is
// empty for a built-in that has not been written out with `sdlc create`.
type availableWorkflow struct {
	Name    string
	Path    string
	Builtin bool
	W       *spec.Workflow
}

// sdlcAvailableWorkflows lists every workflow sdlc start can select among:
// workflow YAML files under the project's SDLC directory, plus every embedded
// built-in the project hasn't already defined under the same name (a
// project's own file always wins — a built-in is usable as-is, but a project
// that wants to diverge from it just names its file the same as the
// built-in, no `sdlc create` required first). A project file that fails to
// load is a hard error naming it: unlike native agent discovery, an authored
// workflow the project checked in is expected to be valid, not best-effort.
func (a *App) sdlcAvailableWorkflows() ([]availableWorkflow, error) {
	dir := a.sdlcWorkflowDir()
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && isProjectWorkflowFile(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	out := make([]availableWorkflow, 0, len(names)+len(spec.BuiltinNames()))
	seen := map[string]bool{}
	for _, name := range names {
		path := filepath.Join(dir, name)
		w, err := a.loadWorkflowFile(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, availableWorkflow{Name: w.Name, Path: path, W: w})
		seen[w.Name] = true
	}
	for _, name := range spec.BuiltinNames() {
		if seen[name] {
			continue
		}
		w, _, err := spec.Builtin(name)
		if err != nil {
			return nil, err
		}
		out = append(out, availableWorkflow{Name: name, Builtin: true, W: w})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// The SDLC directory also holds configuration YAML. Those files are never
// selectable workflows, even though they share the same extension.
func isProjectWorkflowFile(name string) bool {
	ext := filepath.Ext(name)
	if ext != ".yaml" && ext != ".yml" {
		return false
	}
	switch strings.TrimSuffix(name, ext) {
	case "agents", "policy", "roster":
		return false
	}
	return true
}

// resolveWorkflowByName resolves a positional workflow argument, in order: a
// literal existing file path, <name> under the project's workflow
// directory, then an embedded built-in named name.
func (a *App) resolveWorkflowByName(name string) (availableWorkflow, error) {
	if st, err := os.Stat(name); err == nil && !st.IsDir() {
		w, err := a.loadWorkflowFile(name)
		if err != nil {
			return availableWorkflow{}, err
		}
		return availableWorkflow{Name: w.Name, Path: name, W: w}, nil
	}
	if name != "agents" && name != "policy" && name != "roster" {
		for _, ext := range []string{".yaml", ".yml"} {
			path := filepath.Join(a.sdlcWorkflowDir(), name+ext)
			if st, err := os.Stat(path); err == nil && !st.IsDir() {
				w, err := a.loadWorkflowFile(path)
				if err != nil {
					return availableWorkflow{}, fmt.Errorf("%s: %w", path, err)
				}
				return availableWorkflow{Name: w.Name, Path: path, W: w}, nil
			}
		}
	}
	if w, ok, err := spec.Builtin(name); ok {
		if err != nil {
			return availableWorkflow{}, err
		}
		return availableWorkflow{Name: name, Builtin: true, W: w}, nil
	}
	return availableWorkflow{}, fmt.Errorf("workflow %q: not a file, not found at %s, and not a built-in (%s)",
		name, filepath.Join(a.sdlcWorkflowDir(), name+".yaml"), strings.Join(spec.BuiltinNames(), ", "))
}

func (a *App) newRunID(now time.Time) string {
	if a.NewRunID != nil {
		return a.NewRunID()
	}
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("run-%s-%s", now.UTC().Format("20060102T150405Z"), hex.EncodeToString(b[:]))
}

func (a *App) sdlcStartCmd() *cobra.Command {
	var task, taskFile, format, profile, sessionStrategy string
	var files []string
	var selectOnly, delegate, auto bool
	c := &cobra.Command{
		Use:    "start [task-kind|workflow] --task \"...\" [--file path[=artifact]]...",
		Hidden: true,
		Short:  "check setup and create a run for an SDLC task",
		Long: `Start checks agent enrollment and policy, then saves a run and prints its ID.
It does not execute an agent. Use sdlc run for the ordinary one-command path.
Start is useful when you want to inspect the run before any agent executes,
or when a host integration will execute its assignments.

Choose feature, bugfix, review, or release for the adaptive flow; no workflow
file is needed.

To execute an adaptive run with enrolled CLI agents:
  jevkit sdlc resume RUN_ID

Custom stage workflows can ask authored Jev questions and route the same
enrolled agents. Host integrations use next and report to execute agents.`,
		Example: `  jevkit sdlc start feature --task "add rate limiting to the public API"
  jevkit sdlc resume RUN_ID
  jevkit sdlc start --task "the login page 500s when the session cookie is stale"
  jevkit sdlc start --task-file ./issue-4821.md
  jevkit sdlc start feature --task "implement this plan" --file ~/.claude/plans/rate-limiting.md=plan.md
  jevkit sdlc start --task "bump deps" --select-only`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a.sdlcAutoChoice = auto
			defer func() { a.sdlcAutoChoice = false }()
			if sessionStrategy != "" {
				if err := validateSessionStrategy(sessionStrategy); err != nil {
					return err
				}
				a.sdlcSessionChoice = sessionStrategy
				defer func() { a.sdlcSessionChoice = "" }()
			}
			if cmd.Flags().Changed("delegate-builtins") {
				a.sdlcDelegateChoice = &delegate
				defer func() { a.sdlcDelegateChoice = nil }()
			}
			var name string
			if len(args) == 1 {
				name = args[0]
			}
			return a.sdlcStart(cmd.Context(), name, task, taskFile, files, selectOnly, format, profile)
		},
	}
	c.Flags().StringVar(&task, "task", "", "the task statement, inline")
	c.Flags().StringVar(&taskFile, "task-file", "", "path to a file holding the task statement, or - for stdin")
	c.Flags().StringArrayVar(&files, "file", nil, "seed a pre-existing document as a run artifact: path or path=artifact (repeatable)")
	c.Flags().BoolVar(&selectOnly, "select-only", false, "classify and print the outcome; create no run")
	c.Flags().StringVar(&format, "format", "text", "output format: text or json")
	c.Flags().StringVar(&profile, "policy", "", "adaptive policy profile: lean, collaborative or assured (default lean)")
	c.Flags().StringVar(&sessionStrategy, "session-strategy", "", "session policy: auto, fresh, resume or compact")
	c.Flags().BoolVar(&delegate, "delegate-builtins", false, "enable or disable policy-permitted automatic built-in delegation")
	c.Flags().BoolVar(&auto, "auto", false, "continue through implementation without human plan approval")
	return c
}

func (a *App) sdlcStart(ctx context.Context, workflowName, task, taskFile string, fileArgs []string, selectOnly bool, format, profile string) error {
	var restore func()
	var err error
	workflowName, taskFile, fileArgs, restore, err = a.sdlcProjectInputs(workflowName, taskFile, fileArgs)
	if err != nil {
		return failf("%v", err)
	}
	defer restore()
	return a.sdlcStartWithRunID(ctx, workflowName, task, taskFile, fileArgs, selectOnly, format, profile, nil, false)
}

// sdlcStartWithRunID is shared by start and run.
func (a *App) sdlcStartWithRunID(ctx context.Context, workflowName, task, taskFile string, fileArgs []string, selectOnly bool, format, profile string, created *string, drive bool) error {
	if format != "text" && format != "json" {
		return usagef("--format must be text or json")
	}
	if (task == "") == (taskFile == "") {
		return usagef("exactly one of --task or --task-file is required")
	}
	if taskFile != "" {
		t, err := readInputSource(a, taskFile)
		if err != nil {
			return err
		}
		task = t
	}
	if task == "" {
		return usagef("the task must not be empty")
	}

	parsedFiles := make([]seed.FileArg, 0, len(fileArgs))
	for _, raw := range fileArgs {
		fa, err := seed.ParseFileArg(raw)
		if err != nil {
			return usagef("%v", err)
		}
		parsedFiles = append(parsedFiles, fa)
	}
	previousAllowRead := a.sdlcAllowRead
	a.sdlcAllowRead = nil
	if taskFile != "" && taskFile != "-" {
		if absolute, err := filepath.Abs(taskFile); err == nil {
			a.sdlcAllowRead = append(a.sdlcAllowRead, absolute)
		}
	}
	for _, file := range parsedFiles {
		if absolute, err := filepath.Abs(file.Path); err == nil {
			a.sdlcAllowRead = append(a.sdlcAllowRead, absolute)
		}
	}
	defer func() { a.sdlcAllowRead = previousAllowRead }()
	if !selectOnly {
		if drive && workflowName == "" {
			if _, err := a.sdlcAvailableWorkflows(); err != nil {
				return failf("%v", err)
			}
		}
		if profile == "" {
			profile = "lean"
		}
		pf, err := a.sdlcPreflight(profile, a.cliReach())
		if err != nil {
			return failf("sdlc setup: %v", err)
		}
		if len(pf.Missing) > 0 {
			return failf("policy %s cannot start: %s", profile, strings.Join(pf.Missing, "; "))
		}
		if drive && workflowName == "" {
			wf, selection, err := a.resolveWorkflow(ctx, "", task)
			if err != nil {
				return err
			}
			var startErr error
			if wf.Builtin {
				startErr = a.startAdaptive(wf.Name, task, parsedFiles, profile, pf, format, created, drive)
			} else {
				startErr = a.startStageFlow(wf, task, parsedFiles, profile, pf, format, created, drive)
			}
			if startErr != nil {
				return startErr
			}
			if created != nil && *created != "" {
				store := ledger.Open(a.sdlcRunsDir(), *created)
				run, err := store.ReadRun()
				if err != nil {
					return err
				}
				run.SelectionReason = "selected " + wf.Name + " from task"
				if err := store.WriteRun(run); err != nil {
					return err
				}
				d := ledger.Decision{RunID: run.RunID, Kind: "workflow-selection", Stage: run.Adaptive.Stage, Trigger: "task supplied without workflow", Choice: wf.Name, Next: "start workflow"}
				if selection != nil {
					d.Confidence = &selection.Decision.Confidence
					d.Outcome = selection.Decision.Decision
					d.Detail = selection.Decision.Reason
					if !selection.Available {
						d.Outcome = "Jev unavailable; policy fallback"
					}
				} else {
					d.Outcome = "only eligible workflow"
				}
				if all, err := a.sdlcAvailableWorkflows(); err == nil {
					d.Rubrics = map[string]string{}
					for _, item := range all {
						d.Candidates = append(d.Candidates, ledger.Candidate{ID: item.Name})
						d.Rubrics[item.Name] = item.W.Description
					}
				}
				if err := a.recordDecision(store, d); err != nil {
					return err
				}
			}
			return nil
		}
		if workflowName != "" && !a.useAdaptiveStart(workflowName) {
			wf, err := a.resolveWorkflowByName(workflowName)
			if err != nil {
				return failf("%v", err)
			}
			return a.startStageFlow(wf, task, parsedFiles, profile, pf, format, created, drive)
		}
		if a.useAdaptiveStart(workflowName) || profile != "lean" {
			if workflowName == "" {
				kind, err := a.selectAdaptiveKind(ctx, task)
				if err != nil {
					return err
				}
				workflowName = kind
			}
			return a.startAdaptive(workflowName, task, parsedFiles, profile, pf, format, created, drive)
		}
	}

	wf, dec, err := a.resolveWorkflow(ctx, workflowName, task)
	if err != nil {
		return err
	}
	if dec != nil && !dec.Available && format != "json" {
		a.errf("jevkit: sdlc start: workflow selection: jev unavailable (%s); used declared default\n", dec.Decision.Reason)
	}

	if selectOnly {
		return a.printSelection(wf, dec, format)
	}
	pf, err := a.sdlcPreflight(profile, a.cliReach())
	if err != nil {
		return failf("sdlc setup: %v", err)
	}
	if len(pf.Missing) > 0 {
		return failf("policy %s cannot start: %s", profile, strings.Join(pf.Missing, "; "))
	}
	if wf.Builtin {
		return a.startAdaptive(wf.Name, task, parsedFiles, profile, pf, format, created, drive)
	}
	return a.startStageFlow(wf, task, parsedFiles, profile, pf, format, created, drive)
}

func (a *App) startStageFlow(wf availableWorkflow, task string, files []seed.FileArg, profile string, pf preflight, format string, created *string, drive bool) error {
	if err := a.validateSpawnTargets(wf); err != nil {
		return failf("%v", err)
	}
	if len(pf.Missing) > 0 {
		return failf("policy %s cannot start: %s", profile, strings.Join(pf.Missing, "; "))
	}
	if len(files) > 0 {
		return usagef("custom stage workflows do not accept --file; describe the task with --task or --task-file")
	}
	p, _, err := a.sdlcEnrollment()
	if err != nil {
		return failf("%v", err)
	}
	st, err := adaptive.New(wf.Name, profile, pf.Quorum, p.MaxConcurrent, p.MaxRevisions)
	if err != nil {
		return failf("%v", err)
	}
	st.MaxAssignments, st.MaxEstimatedCostUSD = p.MaxAssignments, p.MaxEstimatedCostUSD
	flow, err := stageflow.New(*wf.W, &st)
	if err != nil {
		return failf("%v", err)
	}
	raw, err := os.ReadFile(wf.Path)
	if err != nil {
		return failf("read workflow: %v", err)
	}
	now := a.now()
	runID := a.newRunID(now)
	ts := now.UTC().Format(time.RFC3339)
	run := ledger.Run{RunID: runID, WorkDir: a.WorkDir, AllowRead: append([]string(nil), a.sdlcAllowRead...), Workflow: wf.Name, GraphSHA256: fmt.Sprintf("%x", sha256.Sum256(raw)), Task: task, CreatedAt: ts, UpdatedAt: ts, Adaptive: &st, StageFlow: &flow, TreeUsage: &ledger.TreeUsage{}, SessionStrategy: a.sdlcSessionChoice, RequirePlanApproval: !a.sdlcAutoChoice}
	run.DelegateBuiltins, err = a.sdlcDelegationAllowed(p)
	if err != nil {
		return err
	}
	if err := ledger.Open(a.sdlcRunsDir(), runID).WriteRun(run); err != nil {
		return failf("create run: %v", err)
	}
	if created != nil {
		*created = runID
	}
	if format == "json" {
		data, _ := json.MarshalIndent(run, "", "  ")
		a.outf("%s\n", data)
	} else {
		verb := "created"
		if drive {
			verb = "started"
		}
		a.outf("run %s %s (workflow %s, policy %s; first stage %s)\n", runID, verb, wf.Name, profile, flow.Current)
		a.outf("  Limits: %s; %d stage transitions\n", sdlcLimitSummary(p), flow.Workflow.MaxSteps)
		if !drive {
			a.outf("  Continue: jevkit sdlc resume %s\n", runID)
		}
	}
	return nil
}

func (a *App) selectAdaptiveKind(ctx context.Context, task string) (string, error) {
	criteria := map[string]string{}
	for _, name := range adaptive.TaskKinds {
		criteria[name] = adaptive.TaskKindDescription(name)
	}
	r, err := a.sdlcRouter()
	if err != nil {
		return "", failf("%v", err)
	}
	res, err := r.Decide(ctx, "sdlc.workflow-selection", task, route.CriteriaFromRubrics(criteria))
	if err != nil {
		return "", failf("%v", err)
	}
	if (res.Decision.Decision == registry.Act || res.Decision.Decision == registry.Gather) && res.Decision.Chosen != nil {
		if _, ok := criteria[*res.Decision.Chosen]; ok {
			return *res.Decision.Chosen, nil
		}
	}
	a.errf("jevkit: sdlc start: task kind uncertain; using feature context (specify feature, bugfix, review or release to choose)\n")
	return "feature", nil
}

// A project-authored workflow takes precedence over a built-in task kind.
func (a *App) useAdaptiveStart(name string) bool {
	if name != "" {
		if _, err := os.Stat(name); err == nil {
			return false
		}
		for _, ext := range []string{".yaml", ".yml"} {
			if !isProjectWorkflowFile(name + ext) {
				continue
			}
			if _, err := os.Stat(filepath.Join(a.sdlcWorkflowDir(), name+ext)); err == nil {
				return false
			}
		}
		return true
	}
	entries, err := os.ReadDir(a.sdlcWorkflowDir())
	if err != nil {
		return true
	}
	for _, e := range entries {
		if !e.IsDir() && isProjectWorkflowFile(e.Name()) {
			return false
		}
	}
	return true
}

func (a *App) startAdaptive(name, task string, files []seed.FileArg, profile string, pf preflight, format string, created *string, drive bool) error {
	if name == "" {
		name = "feature"
	}
	switch name {
	case "feature", "bugfix", "review", "release":
	default:
		return usagef("adaptive task kind must be feature, bugfix, review or release")
	}
	p, _, err := a.sdlcEnrollment()
	if err != nil {
		return failf("%v", err)
	}
	st, err := adaptive.New(name, profile, pf.Quorum, p.MaxConcurrent, p.MaxRevisions)
	if err != nil {
		return failf("%v", err)
	}
	st.MaxAssignments = p.MaxAssignments
	st.MaxEstimatedCostUSD = p.MaxEstimatedCostUSD
	contents := map[string][]byte{}
	for _, f := range files {
		artifact := f.Artifact
		if artifact == "" {
			artifact = filepath.Base(f.Path)
		}
		clean := filepath.Clean(artifact)
		if artifact == "" || artifact == "." || filepath.IsAbs(artifact) || clean != artifact || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return usagef("--file needs a valid artifact name")
		}
		if _, dup := contents[artifact]; dup {
			return usagef("duplicate artifact %q", artifact)
		}
		data, err := os.ReadFile(f.Path)
		if err != nil {
			if strings.HasPrefix(f.Path, filepath.Base(a.WorkDir)+string(filepath.Separator)) {
				local := strings.TrimPrefix(f.Path, filepath.Base(a.WorkDir)+string(filepath.Separator))
				if _, localErr := os.Stat(local); localErr == nil {
					return failf("seed %s: %v; from %s use --file %s", f.Path, err, a.WorkDir, local)
				}
			}
			return failf("seed %s: %v", f.Path, err)
		}
		contents[artifact] = data
		if artifact == "spec.md" || artifact == "plan.md" {
			st.PlanRevision = fmt.Sprintf("%x", sha256.Sum256(data))
			st.Stage = adaptive.Implementing
		}
		if artifact == "patch.diff" {
			st.DiffRevision = fmt.Sprintf("%x", sha256.Sum256(data))
			st.Stage = adaptive.Assessing
		}
	}
	now := a.now()
	runID := a.newRunID(now)
	store := ledger.Open(a.sdlcRunsDir(), runID)
	ts := now.UTC().Format(time.RFC3339)
	run := ledger.Run{RunID: runID, WorkDir: a.WorkDir, AllowRead: append([]string(nil), a.sdlcAllowRead...), Workflow: name, Task: task, CreatedAt: ts, UpdatedAt: ts, Adaptive: &st, TreeUsage: &ledger.TreeUsage{}, SessionStrategy: a.sdlcSessionChoice, RequirePlanApproval: !a.sdlcAutoChoice}
	run.DelegateBuiltins, err = a.sdlcDelegationAllowed(p)
	if err != nil {
		return err
	}
	if drive && a.SdlcExecutor == nil {
		if _, err := gitWorktreeRoot(a.WorkDir); err != nil {
			return failf("SDLC CLI runs require a Git repository; run from the project directory or pass --task-file inside that repository")
		}
		if !gitHasHEAD(a.WorkDir) {
			return failf("SDLC CLI runs require an initial Git commit in %s", a.WorkDir)
		}
	}
	if err := store.WriteRun(run); err != nil {
		return failf("create run: %v", err)
	}
	for artifact, data := range contents {
		if err := store.WriteArtifact(artifact, data); err != nil {
			return failf("store artifact %s: %v", artifact, err)
		}
	}
	if created != nil {
		*created = runID
	}
	if format == "json" {
		data, _ := json.MarshalIndent(run, "", "  ")
		a.outf("%s\n", data)
	} else {
		verb := "created"
		if drive {
			verb = "started"
		}
		a.outf("run %s %s (task kind %s, policy %s; first step %s)\n", runID, verb, name, profile, st.Role())
		a.outf("  Limits: %s\n", sdlcLimitSummary(p))
		if !drive {
			a.outf("  Continue: jevkit sdlc resume %s\n", runID)
		}
	}
	return nil
}

// resolveWorkflow resolves the workflow to start: a named one skips
// selection entirely (no Jev call), matching "named workflow skips selection
// entirely" — dec is nil in that case. An omitted name selects among every
// workflow under .jevkit/sdlc/, via Jev when there is more than one.
func (a *App) resolveWorkflow(ctx context.Context, name, task string) (availableWorkflow, *route.Result, error) {
	if name != "" {
		wf, err := a.resolveWorkflowByName(name)
		return wf, nil, err
	}
	all, err := a.sdlcAvailableWorkflows()
	if err != nil {
		return availableWorkflow{}, nil, failf("%v", err)
	}
	if len(all) == 0 {
		return availableWorkflow{}, nil, failf("no workflows available: define one under %s, or name one explicitly", a.sdlcWorkflowDir())
	}
	if len(all) == 1 {
		return all[0], nil, nil
	}

	criteria := make(map[string]string, len(all))
	byName := make(map[string]availableWorkflow, len(all))
	for _, wf := range all {
		criteria[wf.Name] = wf.W.Description
		byName[wf.Name] = wf
	}
	r, err := a.sdlcRouter()
	if err != nil {
		return availableWorkflow{}, nil, failf("%v", err)
	}
	res, err := r.Decide(ctx, "sdlc.workflow-selection", task, route.CriteriaFromRubrics(criteria))
	if err != nil {
		return availableWorkflow{}, nil, failf("%v", err)
	}

	switch res.Decision.Decision {
	case registry.Act:
		wf, ok := byName[*res.Decision.Chosen]
		if !ok {
			return availableWorkflow{}, nil, failf("internal error: chosen workflow %q not found", *res.Decision.Chosen)
		}
		return wf, &res, nil
	case registry.Gather:
		wf, ok := byName[*res.Decision.Chosen]
		if !ok {
			return availableWorkflow{}, nil, refuseWorkflowSelection(all, res)
		}
		if a.Confirm == nil {
			return availableWorkflow{}, nil, refuseWorkflowSelection(all, res)
		}
		ok2, err := a.Confirm(fmt.Sprintf("Use workflow %q (confidence %.2f)?", wf.Name, res.Decision.Confidence))
		if err != nil || !ok2 {
			return availableWorkflow{}, nil, refuseWorkflowSelection(all, res)
		}
		return wf, &res, nil
	default: // fallback
		return availableWorkflow{}, nil, refuseWorkflowSelection(all, res)
	}
}

// refuseWorkflowSelection is the plan's own deliberate exception to
// "in-graph nodes fall back to a default": there is no author-declared
// default across workflows, so low confidence at the top level refuses
// rather than guesses.
func refuseWorkflowSelection(all []availableWorkflow, res route.Result) error {
	names := make([]string, 0, len(all))
	for _, wf := range all {
		names = append(names, wf.Name)
	}
	sort.Strings(names)
	return failf("could not confidently select a workflow (decision=%s, confidence=%.2f): name one explicitly. candidates: %s",
		res.Decision.Decision, res.Decision.Confidence, strings.Join(names, ", "))
}

func (a *App) printSelection(wf availableWorkflow, dec *route.Result, format string) error {
	if format == "json" {
		out := map[string]any{"workflow": wf.Name, "path": wf.Path}
		if dec != nil {
			out["decision"] = dec.Decision
			out["available"] = dec.Available
		}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return failf("%v", err)
		}
		a.outf("%s\n", b)
		return nil
	}
	if dec == nil {
		a.outf("workflow: %s (named explicitly; no selection made)\n", wf.Name)
		return nil
	}
	a.outf("workflow: %s (decision=%s confidence=%.2f available=%v)\n", wf.Name, dec.Decision.Decision, dec.Decision.Confidence, dec.Available)
	return nil
}
