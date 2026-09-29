package sdlc

import (
	"testing"

	"github.com/spf13/cobra"

	"github.com/JoshJancula/jevkit/cmd/jevkit/internal/testkit"
	redactcmd "github.com/JoshJancula/jevkit/cmd/jevkit/redact"
	securitycmd "github.com/JoshJancula/jevkit/cmd/jevkit/security"
	usagecmd "github.com/JoshJancula/jevkit/cmd/jevkit/usage"
	"github.com/JoshJancula/jevkit/cmd/jevkit/util"
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
		return append([]*cobra.Command{a.sdlcCmd(), usagecmd.Command(a.App), securitycmd.Command(a.App), redactcmd.Command(a.App)}, util.Commands(a.App)...)
	}
}

func run(a *App, stdin string, args ...string) (code int, stdout, stderr string) {
	return testkit.RunWith(a.App, testCommands(a), stdin, args...)
}
