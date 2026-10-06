package security

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/registry"
	"github.com/JoshJancula/jevkit/internal/security"
	securityconfig "github.com/JoshJancula/jevkit/internal/security/config"
)

func (a *App) securityCheckCmd() *cobra.Command {
	return &cobra.Command{Use: "check <command>", Short: "check a shell command", Long: "Evaluate a shell command against the killswitch and path guard, then optional Jev command-risk scoring when the effective policy or JEVKIT_SECURITY_SCORING enables it. Scoring uses the redacted command and can deny only in enforce mode for a severe or critical score with a registry Act confidence decision; Jev errors and timeouts fail open. Shadow mode prevents registry blocking.", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := securityconfig.Load(a.SecurityLoadOptions())
			if err != nil {
				return app.Failf("%v", err)
			}
			if cfg.JevScoring {
				cfg.Asker = a.SecurityAsker(cmd.Context())
			}
			reg, err := registry.Load()
			if err != nil {
				return app.Failf("%v", err)
			}
			decider := &registry.Decider{Registry: reg, StateDir: a.StateHome(), Getenv: a.Getenv}
			decision, err := security.Evaluate(cmd.Context(), cfg, security.Request{Command: args[0], Cwd: a.WorkDir, Workspace: a.WorkDir, Runtime: "cli"}, decider)
			if err != nil {
				return app.Failf("%v", err)
			}
			a.Outf("%s\n", securityCheckOutput(decision.Deny, decision.Reason))
			return nil
		}}
}

func (a *App) securityTestCmd() *cobra.Command {
	return &cobra.Command{Use: "test", Short: "run policy examples", Long: "Run the policy examples with Jev scoring forced off. These tests exercise the deterministic killswitch and path-guard rules.", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := securityconfig.Load(a.SecurityLoadOptions())
			if err != nil {
				return app.Failf("%v", err)
			}
			cfg.JevScoring = false
			for i, test := range cfg.Tests {
				decision, err := security.Evaluate(context.Background(), cfg, security.Request{Command: test.Command, Cwd: a.WorkDir, Workspace: a.WorkDir}, nil)
				if err != nil {
					return app.Failf("test %d: %v", i+1, err)
				}
				if decision.Deny != test.Deny {
					return app.Failf("test %d: got %s", i+1, securityCheckOutput(decision.Deny, decision.Reason))
				}
			}
			a.Outf("%d security tests passed\n", len(cfg.Tests))
			return nil
		}}
}
