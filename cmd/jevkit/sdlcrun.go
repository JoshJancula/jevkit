package main

import (
	"context"

	"github.com/spf13/cobra"
)

// run is the ordinary one-command path. Lower-level commands remain for host
// integrations.
func (a *App) sdlcRunCmd() *cobra.Command {
	var task, taskFile, profile string
	var files []string
	var step bool
	c := &cobra.Command{
		Use:   "run [task-kind|workflow] --task \"...\" [--file path[=artifact]]...",
		Short: "start and execute an SDLC task in one command",
		Long: `Run checks enrollment and policy, creates a run, then executes it until
it finishes or pauses. It prints the run ID. Use --step to execute only the
first action and leave an active run you can continue with resume RUN_ID.

Choose feature, bugfix, review, or release without creating a workflow file.
If you omit the task kind, Jevkit selects one of those built-in kinds. Use a
named custom stage workflow when your project has authored questions
and routes.

Use resume RUN_ID to continue an active run. Use create NAME only when you
want to author project-specific questions and routes.`,
		Example: `  jevkit sdlc run feature --task "add rate limiting"
  jevkit sdlc run --task "fix stale-session login failure"
  jevkit sdlc run feature --task "add rate limiting" --step
  jevkit sdlc resume RUN_ID
  jevkit sdlc run custom-review --task-file ./issue.md
  jevkit sdlc run feature --task "implement this plan" --file ./plan.md=plan.md`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			return a.sdlcRun(cmd.Context(), name, task, taskFile, files, profile, step)
		},
	}
	c.Flags().StringVar(&task, "task", "", "the task statement, inline")
	c.Flags().StringVar(&taskFile, "task-file", "", "path to a file holding the task statement, or - for stdin")
	c.Flags().StringArrayVar(&files, "file", nil, "seed a pre-existing document as a run artifact: path or path=artifact (repeatable)")
	c.Flags().StringVar(&profile, "policy", "", "SDLC policy profile: lean, collaborative or assured (default lean)")
	c.Flags().BoolVar(&step, "step", false, "execute one question or agent action, then stop")
	return c
}

func (a *App) sdlcRun(ctx context.Context, name, task, taskFile string, files []string, profile string, step bool) error {
	var runID string
	if err := a.sdlcStartWithRunID(ctx, name, task, taskFile, files, false, "text", profile, &runID, true); err != nil {
		return err
	}
	if step {
		return a.sdlcDrive(ctx, runID)
	}
	return a.sdlcDriveUntilDone(ctx, runID)
}
