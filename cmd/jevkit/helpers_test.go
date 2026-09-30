package main

import (
	"testing"

	"github.com/spf13/cobra"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/cmd/jevkit/internal/testkit"
)

func newApp(t *testing.T) *app.App { return testkit.NewApp(t) }

func cliApp(t *testing.T) (*app.App, *testkit.FakeKeyring, *testkit.FakeJev) {
	return testkit.CLIApp(t)
}

func testCommands(a *app.App) func() []*cobra.Command {
	return func() []*cobra.Command { return commands(a) }
}

func run(a *app.App, stdin string, args ...string) (code int, stdout, stderr string) {
	return testkit.RunWith(a, testCommands(a), stdin, args...)
}

func mustRun(t *testing.T, a *app.App, stdin string, want int, args ...string) (stdout, stderr string) {
	t.Helper()
	return testkit.MustRunWith(t, a, testCommands(a), stdin, want, args...)
}
