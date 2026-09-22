package main

import (
	"context"
	"strings"

	"github.com/spf13/cobra"

	"github.com/OWNER/jevkit/internal/compact"
	internalexec "github.com/OWNER/jevkit/internal/exec"
	"github.com/OWNER/jevkit/internal/jev"
)

func (a *App) execCmd() *cobra.Command {
	return &cobra.Command{
		Use: "exec -- <command> [args...]", Short: "run a command and compact its output", DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// DisableFlagParsing preserves the separator itself, unlike Cobra's
			// normal parser. It is syntax for this command, not child argv.
			if len(args) > 0 && args[0] == "--" {
				args = args[1:]
			}
			return codeErr(a.exec(cmd.Context(), args))
		},
	}
}

func (a *App) exec(ctx context.Context, args []string) int {
	enabled := a.getenv("JEVKIT_COMPACT") == "1"
	var asker Asker
	if enabled {
		cfg := jev.ConfigFromEnv(a.getenv)
		asker = a.newJev(cfg, func() (string, error) { key, _, err := a.store().Resolve(context.Background()); return key, err })
	}
	return internalexec.Run(ctx, args, internalexec.Options{
		Stdout: a.Stdout, Stderr: a.Stderr, Asker: asker,
		JevOptions: compact.JevOptions{Enabled: enabled, Shadow: strings.EqualFold(a.getenv("JEVKIT_COMPACT_SHADOW"), "1") || strings.EqualFold(a.getenv("JEVKIT_COMPACT_SHADOW"), "true"), StateDir: a.stateHome()},
	})
}
