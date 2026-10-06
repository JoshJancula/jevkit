package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// exitError carries a process exit code out of a cobra RunE. An empty msg
// means the command already reported the failure itself.
type ExitError struct {
	Code int
	msg  string
}

func (e *ExitError) Error() string { return e.msg }

// failf is a runtime failure (exit 1); usagef is a usage error (exit 2).
func Failf(format string, args ...any) error {
	return &ExitError{Code: ExitFail, msg: fmt.Sprintf(format, args...)}
}

func Usagef(format string, args ...any) error {
	return &ExitError{Code: ExitUsage, msg: fmt.Sprintf(format, args...)}
}

// codeErr turns a handler's exit code into an error; zero is success.
func CodeErr(code int) error {
	if code == ExitOK {
		return nil
	}
	return &ExitError{Code: code}
}

// Run dispatches args (without the program name) and returns the exit code.
// Cobra's own errors (unknown command, bad flag, bad arguments) are usage
// errors.
func (a *App) Run(args []string, commands ...*cobra.Command) int {
	return a.RunContext(context.Background(), args, commands...)
}

// RunContext dispatches a command with a caller-owned cancellation context.
// commands are the top-level subcommands; the root owns global flags and help.
func (a *App) RunContext(ctx context.Context, args []string, commands ...*cobra.Command) int {
	root := a.RootCmd(commands...)
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return ExitOK
	}
	var ee *ExitError
	if errors.As(err, &ee) {
		if ee.msg != "" {
			a.Errf("jevkit: %s\n", ee.msg)
		}
		return ee.Code
	}
	a.Errf("jevkit: %v\n\n%s", err, root.UsageString())
	return ExitUsage
}

// group makes a command that only holds subcommands.
func (a *App) Group(use, short string, subs ...*cobra.Command) *cobra.Command {
	c := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a.Errf("%s", cmd.UsageString())
			return CodeErr(ExitUsage)
		},
	}
	c.AddCommand(subs...)
	return c
}

// getenv reads the injected environment (KEY=value pairs).
func (a *App) Getenv(key string) string {
	prefix := key + "="
	for _, kv := range a.Environ {
		if v, ok := strings.CutPrefix(kv, prefix); ok {
			return v
		}
	}
	return ""
}

func (a *App) RootCmd(commands ...*cobra.Command) *cobra.Command {
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
		Version:       a.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a.Errf("%s", cmd.UsageString())
			return CodeErr(ExitUsage)
		},
	}
	root.SetOut(a.Stdout)
	root.SetErr(a.Stderr)
	root.SetIn(a.Stdin)
	root.SetVersionTemplate("jevkit {{.Version}}\n")
	root.PersistentFlags().BoolVar(&a.Yolo, "yolo", a.Yolo, "lift path guard only; killswitch and Jev scoring still apply")
	root.PersistentFlags().StringVar(&a.SecurityPolicy, "security-policy", a.SecurityPolicy, "select a named security policy (env: JEVKIT_SECURITY_POLICY)")
	root.SetHelpFunc(func(cmd *cobra.Command, _ []string) {
		w := cmd.OutOrStdout()
		description := cmd.Long
		if description == "" {
			description = cmd.Short
		}
		if description != "" {
			_, _ = fmt.Fprintf(w, "%s\n\n", a.HelpText(w, description))
		}
		_, _ = fmt.Fprint(w, a.HelpText(w, cmd.UsageString()))
	})
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(commands...)
	return root
}
