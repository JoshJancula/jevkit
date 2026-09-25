package main

import (
	"context"
	"fmt"
	"github.com/OWNER/jevkit/internal/sdlc/ledger"
	"io"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// run is the ordinary one-command path. Lower-level commands remain for host
// integrations.
func (a *App) sdlcRunCmd() *cobra.Command {
	var task, taskFile, planFile, profile, sessionStrategy string
	var files []string
	var step, silent, delegate, auto bool
	c := &cobra.Command{
		Use:   "run [task-kind|workflow] --task \"...\" [--file path[=artifact]]...",
		Short: "start an SDLC task and review its plan before implementation",
		Long: `Run checks enrollment and policy, creates a run, then executes it until
it finishes or pauses. In a terminal, it shows the plan and asks whether to
approve it or request changes before implementation. Without a terminal, the
run pauses and can be approved with resume RUN_ID --approve-plan. Use --auto
for the previous fully autonomous behavior. It prints the run ID. Use --step to execute
only the first action and leave an active run you can continue with resume RUN_ID.

Choose feature, bugfix, review, or release without creating a workflow file.
If you omit the task kind, Jevkit selects one of those built-in kinds. Use a
named custom stage workflow when your project has authored questions
and routes.

Use resume RUN_ID to continue an active run. Use create NAME only when you
want to author project-specific questions and routes.`,
		Example: `  jevkit sdlc run feature --task "add rate limiting"
  jevkit sdlc resume RUN_ID --approve-plan
  jevkit sdlc run bugfix --task "fix stale-session login failure" --auto
  jevkit sdlc run --task "fix stale-session login failure"
  jevkit sdlc run feature --plan-file ./prepared-plan.md
  jevkit sdlc run feature --task "add rate limiting" --step
  jevkit sdlc resume RUN_ID
  jevkit sdlc run custom-review --task-file ./issue.md
  jevkit sdlc run feature --task "implement this plan" --file ./plan.md=plan.md`,
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
			if planFile != "" {
				files = append([]string{planFile + "=plan.md"}, files...)
				if task == "" && taskFile == "" {
					task = "Implement the supplied plan."
				}
			}
			if cmd.Flags().Changed("delegate-builtins") {
				a.sdlcDelegateChoice = &delegate
				defer func() { a.sdlcDelegateChoice = nil }()
			}
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			if silent {
				return a.sdlcRunSilent(cmd.Context(), name, task, taskFile, files, profile, step)
			}
			return a.sdlcRun(cmd.Context(), name, task, taskFile, files, profile, step)
		},
	}
	c.Flags().StringVar(&task, "task", "", "the task statement, inline")
	c.Flags().StringVar(&taskFile, "task-file", "", "path to a file holding the task statement, or - for stdin")
	c.Flags().StringVar(&planFile, "plan-file", "", "use an existing plan as plan.md and review it before implementation")
	c.Flags().StringArrayVar(&files, "file", nil, "seed a pre-existing document as a run artifact: path or path=artifact (repeatable)")
	c.Flags().StringVar(&profile, "policy", "", "SDLC policy profile: lean, collaborative or assured (default lean)")
	c.Flags().StringVar(&sessionStrategy, "session-strategy", "", "session policy: auto, fresh, resume or compact")
	c.Flags().BoolVar(&step, "step", false, "execute one question or agent action, then stop")
	c.Flags().BoolVar(&auto, "auto", false, "continue through implementation without human plan approval")
	c.Flags().BoolVar(&silent, "silent", false, "show only run ID and final status")
	c.Flags().BoolVar(&delegate, "delegate-builtins", false, "enable or disable policy-permitted automatic built-in delegation")
	return c
}

func (a *App) sdlcRunSilent(ctx context.Context, name, task, taskFile string, files []string, profile string, step bool) error {
	name, taskFile, files, restore, err := a.sdlcProjectInputs(name, taskFile, files)
	if err != nil {
		return failf("%v", err)
	}
	defer restore()
	out := a.Stdout
	a.Stdout = io.Discard
	defer func() { a.Stdout = out }()
	var id string
	if err := a.sdlcStartWithRunID(ctx, name, task, taskFile, files, false, "text", profile, &id, true); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "%s\n", id)
	completed := ""
	if initial, err := ledger.Open(a.sdlcRunsDir(), id).ReadRun(); err == nil && initial.Adaptive != nil {
		completed = initial.Adaptive.Stage
	}
	if step {
		err = a.sdlcDrive(ctx, id)
	} else {
		err = a.sdlcDriveUntilDone(ctx, id)
	}
	a.sdlcSilentStatus(out, id, step, completed)
	return err
}

func (a *App) sdlcSilentStatus(out io.Writer, id string, step bool, completed string) {
	r, err := ledger.Open(a.sdlcRunsDir(), id).ReadRun()
	if err != nil {
		return
	}
	label := "run"
	if step {
		label = "step"
	}
	if r.Adaptive != nil {
		_, _ = fmt.Fprintf(out, "%s %s: ", label, id)
		if step && completed != "" {
			_, _ = fmt.Fprintf(out, "completed %s; now ", completed)
		}
		_, _ = fmt.Fprint(out, r.Adaptive.Stage)
		if r.Adaptive.Outcome != "" {
			_, _ = fmt.Fprintf(out, " (%s)", r.Adaptive.Outcome)
		}
		_, _ = fmt.Fprintln(out)
	}
}

func (a *App) sdlcRun(ctx context.Context, name, task, taskFile string, files []string, profile string, step bool) error {
	name, taskFile, files, restore, err := a.sdlcProjectInputs(name, taskFile, files)
	if err != nil {
		return failf("%v", err)
	}
	defer restore()
	var runID string
	if err := a.sdlcStartWithRunID(ctx, name, task, taskFile, files, false, "text", profile, &runID, true); err != nil {
		return err
	}
	if a.sdlcInteractive() {
		drive := func() error {
			worker := a.sdlcDashboardWorker()
			if step {
				return worker.sdlcDrive(ctx, runID)
			}
			return worker.sdlcDriveUntilDone(ctx, runID)
		}
		if !step {
			return a.sdlcInteractiveDrive(ctx, runID, drive, false)
		}
		return a.sdlcWatchDrive(ctx, runID, drive, func(strategy string) error {
			return a.sdlcDashboardRetry(ctx, runID, strategy)
		})
	}
	a.sdlcProgress = &sdlcProgress{root: runID, out: a.Stdout, seen: map[string]int{}, last: map[string]string{}}
	defer func() { a.sdlcProgress = nil }()
	a.progressFlush()
	a.sdlcSuppressLegacy = true
	defer func() { a.sdlcSuppressLegacy = false }()
	var driveErr error
	if step {
		driveErr = a.sdlcDrive(ctx, runID)
	} else {
		driveErr = a.sdlcDriveUntilDone(ctx, runID)
	}
	a.sdlcFinalSummary(a.Stdout, runID, driveErr)
	return driveErr
}

func (a *App) sdlcInteractive() bool {
	input, inputOK := a.Stdin.(*os.File)
	output, outputOK := a.Stdout.(*os.File)
	return inputOK && outputOK && term.IsTerminal(int(input.Fd())) && term.IsTerminal(int(output.Fd()))
}

func (a *App) sdlcDashboardWorker() *App {
	worker := *a
	worker.Stdout = io.Discard
	worker.sdlcProgress = nil
	worker.sdlcSuppressLegacy = true
	return &worker
}
