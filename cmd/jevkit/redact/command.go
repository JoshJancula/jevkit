package redact

import (
	"github.com/spf13/cobra"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
)

// App carries the shared CLI state and helpers for redact commands.
type App struct{ *app.App }

// Command returns the redact command.
func Command(a *app.App) *cobra.Command {
	return (&App{App: a}).redactCmd()
}
