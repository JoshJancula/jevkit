package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// exitError carries a process exit code out of a cobra RunE. An empty msg
// means the command already reported the failure itself.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

// failf is a runtime failure (exit 1); usagef is a usage error (exit 2).
func failf(format string, args ...any) error {
	return &exitError{code: exitFail, msg: fmt.Sprintf(format, args...)}
}

func usagef(format string, args ...any) error {
	return &exitError{code: exitUsage, msg: fmt.Sprintf(format, args...)}
}

// codeErr turns a handler's exit code into an error; zero is success.
func codeErr(code int) error {
	if code == exitOK {
		return nil
	}
	return &exitError{code: code}
}

// Run dispatches args (without the program name) and returns the exit code.
// Cobra's own errors (unknown command, bad flag, bad arguments) are usage
// errors.
func (a *App) Run(args []string) int {
	return a.RunContext(context.Background(), args)
}

// RunContext dispatches a command with a caller-owned cancellation context.
func (a *App) RunContext(ctx context.Context, args []string) int {
	root := a.rootCmd()
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return exitOK
	}
	var ee *exitError
	if errors.As(err, &ee) {
		if ee.msg != "" {
			a.errf("jevkit: %s\n", ee.msg)
		}
		return ee.code
	}
	a.errf("jevkit: %v\n\n%s", err, root.UsageString())
	return exitUsage
}

func (a *App) rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "jevkit",
		Short: "jevkit: a Jev (TypeSafe AI) toolkit for coding agents",
		Long: `jevkit provides typed Jev decisions, safe redaction, agent integrations,
and SDLC runs with agents you enroll. Ask commands send questions to Jev;
SDLC run commands can assign agents to work on your project.`,
		Example: `  # Check local configuration without sending a request.
  jevkit doctor

  # Ask a typed question from the terminal.
  jevkit ask choice --state "HTTP status: 503" --question "What should happen?" --options "retry,fail"

  # See redaction rules before connecting an agent.
  jevkit redact list

  # Set up agents for an SDLC run.
  jevkit sdlc agents`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a.errf("%s", cmd.UsageString())
			return codeErr(exitUsage)
		},
	}
	root.SetOut(a.Stdout)
	root.SetErr(a.Stderr)
	root.SetIn(a.Stdin)
	root.PersistentFlags().BoolVar(&a.Yolo, "yolo", a.Yolo, "lift path guard only; killswitch and Jev scoring still apply")
	root.PersistentFlags().StringVar(&a.SecurityPolicy, "security-policy", a.SecurityPolicy, "select a named security policy (env: JEVKIT_SECURITY_POLICY)")
	root.SetHelpFunc(func(cmd *cobra.Command, _ []string) {
		w := cmd.OutOrStdout()
		description := cmd.Long
		if description == "" {
			description = cmd.Short
		}
		if description != "" {
			_, _ = fmt.Fprintf(w, "%s\n\n", a.helpText(w, description))
		}
		_, _ = fmt.Fprint(w, a.helpText(w, cmd.UsageString()))
	})
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(
		a.keyCmd(),
		a.modelCmd(),
		a.usageCmd(),
		a.doctorCmd(),
		a.versionCmd(),
		a.upgradeCmd(),
		a.mcpCmd(),
		a.askCmd(),
		a.compactCmd(),
		a.redactCmd(),
		a.securityCmd(),
		a.sdlcCmd(),
		a.runtimeCmd(),
		a.installCmd(),
		a.uninstallCmd(),
	)
	return root
}

// group makes a command that only holds subcommands.
func (a *App) group(use, short string, subs ...*cobra.Command) *cobra.Command {
	c := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a.errf("%s", cmd.UsageString())
			return codeErr(exitUsage)
		},
	}
	c.AddCommand(subs...)
	return c
}

func (a *App) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "print the jevkit version",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			a.outf("jevkit %s\n", a.Version)
			return nil
		},
	}
}

// redactCmd hands everything after "redact" to the redact subcommand
// dispatcher, which owns its own flag parsing.
func (a *App) redactCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "redact",
		Short:              `inspect and fine-tune redaction (run "jevkit redact help" for subcommands)`,
		DisableFlagParsing: true,
		RunE: func(_ *cobra.Command, args []string) error {
			return codeErr(a.redact(args))
		},
	}
}

// getenv reads the injected environment (KEY=value pairs).
func (a *App) getenv(key string) string {
	prefix := key + "="
	for _, kv := range a.Environ {
		if v, ok := strings.CutPrefix(kv, prefix); ok {
			return v
		}
	}
	return ""
}
