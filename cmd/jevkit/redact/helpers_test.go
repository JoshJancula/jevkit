package redact

import (
	"testing"

	"github.com/spf13/cobra"

	"github.com/JoshJancula/jevkit/cmd/jevkit/internal/testkit"
)

func newApp(t *testing.T) *App { return &App{App: testkit.NewApp(t)} }

// testCommands builds this package's commands (plus any it drives in tests)
// on the same App the test inspects.
func testCommands(a *App) func() []*cobra.Command {
	return func() []*cobra.Command {
		return []*cobra.Command{a.redactCmd()}
	}
}

func run(a *App, stdin string, args ...string) (code int, stdout, stderr string) {
	return testkit.RunWith(a.App, testCommands(a), stdin, args...)
}
