package main

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/OWNER/jevkit/internal/sdlc/adaptive"
	"github.com/OWNER/jevkit/internal/sdlc/ledger"
)

func (a *App) sdlcResumeCmd() *cobra.Command {
	var step bool
	c := &cobra.Command{
		Use:   "resume <run-id>",
		Short: "continue an active run by ID",
		Long: `Resume continues an active run until it finishes or pauses. Use --step
to execute just the next question or agent action and inspect the result.

Run starts a new task; run --step leaves an active run ID for resume. A host
integration can also leave an active run. A run paused by a policy limit or
failure has stopped; address the cause and start a new task.`,
		Example: "  jevkit sdlc resume RUN_ID\n  jevkit sdlc resume RUN_ID --step",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.sdlcResume(cmd.Context(), args[0], step)
		},
	}
	c.Flags().BoolVar(&step, "step", false, "execute one question or agent action, then stop")
	return c
}

func (a *App) sdlcResume(ctx context.Context, runID string, step bool) error {
	if !sdlcRunIDRE.MatchString(runID) {
		return usagef("invalid run ID")
	}
	run, err := ledger.Open(a.sdlcRunsDir(), runID).ReadRun()
	if err != nil {
		return failf("read run: %v", err)
	}
	if run.Adaptive == nil {
		return failf("run %s has an unsupported run format", runID)
	}
	if run.Adaptive.Stage == adaptive.Paused {
		return failf("run %s is paused (%s); start a new task after addressing the cause", runID, run.Adaptive.Outcome)
	}
	if run.Adaptive.Stage == adaptive.Done {
		a.outf("run %s is already complete (%s)\n", runID, run.Adaptive.Outcome)
		return nil
	}
	if step {
		return a.sdlcDrive(ctx, runID)
	}
	return a.sdlcDriveUntilDone(ctx, runID)
}
