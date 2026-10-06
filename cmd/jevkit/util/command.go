// Package util holds the small single-file jevkit commands.
package util

import (
	"github.com/spf13/cobra"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
)

// App carries the shared CLI state and helpers for these commands.
type App struct{ *app.App }

// Commands returns every command this package owns.
func Commands(a *app.App) []*cobra.Command {
	return (&App{App: a}).commands()
}

func (a *App) commands() []*cobra.Command {
	return []*cobra.Command{
		a.keyCmd(),
		a.modelCmd(),
		a.doctorCmd(),
		a.versionCmd(),
		a.upgradeCmd(),
		a.askCmd(),
		a.compactCmd(),
		a.runtimeCmd(),
		a.installCmd(),
		a.uninstallCmd(),
	}
}
