package mcp

import (
	"github.com/spf13/cobra"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
)

// App carries the shared CLI state and helpers for mcp commands.
type App struct{ *app.App }

// Command returns the mcp command.
func Command(a *app.App) *cobra.Command {
	return (&App{App: a}).mcpCmd()
}
