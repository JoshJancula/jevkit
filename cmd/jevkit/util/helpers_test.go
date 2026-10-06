package util

import (
	"testing"

	"github.com/spf13/cobra"

	"github.com/JoshJancula/jevkit/cmd/jevkit/internal/testkit"
)

func newApp(t *testing.T) *App { return &App{App: testkit.NewApp(t)} }

func cliApp(t *testing.T) (*App, *testkit.FakeKeyring, *testkit.FakeJev) {
	a, keyring, jev := testkit.CLIApp(t)
	return &App{App: a}, keyring, jev
}

// testCommands builds this package's commands (plus any it drives in tests)
// on the same App the test inspects.
func testCommands(a *App) func() []*cobra.Command {
	return func() []*cobra.Command {
		return a.commands()
	}
}

func run(a *App, stdin string, args ...string) (code int, stdout, stderr string) {
	return testkit.RunWith(a.App, testCommands(a), stdin, args...)
}

func mustRun(t *testing.T, a *App, stdin string, want int, args ...string) (stdout, stderr string) {
	t.Helper()
	return testkit.MustRunWith(t, a.App, testCommands(a), stdin, want, args...)
}
