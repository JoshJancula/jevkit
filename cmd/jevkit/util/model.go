package util

import (
	"errors"
	"os"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/spf13/cobra"
)

func (a *App) modelCmd() *cobra.Command {
	return a.Group("model", "select the Jev model used by Jevkit",
		a.modelSetCmd(), a.modelStatusCmd(), a.modelClearCmd())
}

func (a *App) modelSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set MODEL",
		Short: "save a user-wide Jev model selection",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			model := args[0]
			if !app.ValidModelName(model) {
				return app.Usagef("model must be a name of 1 to 128 letters, digits, dots, underscores, colons, slashes, @ or hyphens")
			}
			path := a.ModelPath()
			if path == "" {
				return app.Failf("no user config directory; set JEVKIT_CONFIG_DIR")
			}
			if err := os.MkdirAll(a.ConfigDir, 0o700); err != nil {
				return app.Failf("create config directory: %v", err)
			}
			if err := app.WriteAtomic(path, []byte(model+"\n"), 0o600); err != nil {
				return app.Failf("save model setting: %v", err)
			}
			a.Outf("Saved Jev model %s.\n", model)
			if a.Getenv("JEVKIT_MODEL") != "" {
				a.Outf("JEVKIT_MODEL currently overrides this setting.\n")
			}
			return nil
		},
	}
}

func (a *App) modelStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "show the effective Jev model and where it comes from",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			model, source, err := a.ModelSelection()
			if err != nil {
				return app.Failf("%v", err)
			}
			a.Outf("model: %s\nsource: %s\n", model, source)
			return nil
		},
	}
}

func (a *App) modelClearCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clear",
		Short: "remove the saved user model selection",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			path := a.ModelPath()
			if path == "" {
				return app.Failf("no user config directory; set JEVKIT_CONFIG_DIR")
			}
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return app.Failf("clear model setting: %v", err)
			}
			a.Outf("Cleared the saved Jev model.\n")
			return nil
		},
	}
}
