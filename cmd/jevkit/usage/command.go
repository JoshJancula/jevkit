package usage

import (
	"github.com/spf13/cobra"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
)

// App carries the shared CLI state and helpers for usage commands.
type App struct{ *app.App }

// Command returns the usage command.
func Command(a *app.App) *cobra.Command {
	return (&App{App: a}).usageCmd()
}
