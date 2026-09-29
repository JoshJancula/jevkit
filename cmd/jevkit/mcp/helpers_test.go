package mcp

import (
	"testing"

	"github.com/spf13/cobra"

	"github.com/JoshJancula/jevkit/cmd/jevkit/internal/testkit"
)

func cliApp(t *testing.T) (*App, *testkit.FakeKeyring, *testkit.FakeJev) {
	a, keyring, jev := testkit.CLIApp(t)
	return &App{App: a}, keyring, jev
}

// testCommands builds this package's commands (plus any it drives in tests)
// on the same App the test inspects.
func testCommands(a *App) func() []*cobra.Command {
	return func() []*cobra.Command {
		return []*cobra.Command{a.mcpCmd()}
	}
}

func run(a *App, stdin string, args ...string) (code int, stdout, stderr string) {
	return testkit.RunWith(a.App, testCommands(a), stdin, args...)
}
