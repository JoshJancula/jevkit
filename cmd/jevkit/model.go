package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/OWNER/jevkit/internal/jev"
)

var modelNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,127}$`)

func (a *App) modelPath() string {
	if a.ConfigDir == "" {
		return ""
	}
	return filepath.Join(a.ConfigDir, "model")
}

func validModelName(model string) bool { return modelNamePattern.MatchString(model) }

func (a *App) storedModel() (string, error) {
	path := a.modelPath()
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read model setting: %w", err)
	}
	model := strings.TrimSpace(string(data))
	if !validModelName(model) {
		return "", fmt.Errorf("invalid model setting in %s", path)
	}
	return model, nil
}

// modelSelection chooses the process override, user setting, then built-in default.
func (a *App) modelSelection() (model, source string, err error) {
	if model = a.getenv("JEVKIT_MODEL"); model != "" {
		if !validModelName(model) {
			return "", "", fmt.Errorf("invalid JEVKIT_MODEL value")
		}
		return model, "JEVKIT_MODEL", nil
	}
	model, err = a.storedModel()
	if err != nil {
		return "", "", err
	}
	if model != "" {
		return model, a.modelPath(), nil
	}
	return jev.DefaultModel, "built-in default", nil
}

func (a *App) jevConfig() (jev.Config, error) {
	cfg := jev.ConfigFromEnv(a.getenv)
	model, _, err := a.modelSelection()
	if err != nil {
		return cfg, err
	}
	cfg.Model = model
	return cfg, nil
}

func (a *App) modelCmd() *cobra.Command {
	return a.group("model", "select the Jev model used by Jevkit",
		a.modelSetCmd(), a.modelStatusCmd(), a.modelClearCmd())
}

func (a *App) modelSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set MODEL",
		Short: "save a user-wide Jev model selection",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			model := args[0]
			if !validModelName(model) {
				return usagef("model must be a name of 1 to 128 letters, digits, dots, underscores, colons, slashes, @ or hyphens")
			}
			path := a.modelPath()
			if path == "" {
				return failf("no user config directory; set JEVKIT_CONFIG_DIR")
			}
			if err := os.MkdirAll(a.ConfigDir, 0o700); err != nil {
				return failf("create config directory: %v", err)
			}
			if err := writeAtomic(path, []byte(model+"\n"), 0o600); err != nil {
				return failf("save model setting: %v", err)
			}
			a.outf("Saved Jev model %s.\n", model)
			if a.getenv("JEVKIT_MODEL") != "" {
				a.outf("JEVKIT_MODEL currently overrides this setting.\n")
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
			model, source, err := a.modelSelection()
			if err != nil {
				return failf("%v", err)
			}
			a.outf("model: %s\nsource: %s\n", model, source)
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
			path := a.modelPath()
			if path == "" {
				return failf("no user config directory; set JEVKIT_CONFIG_DIR")
			}
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return failf("clear model setting: %v", err)
			}
			a.outf("Cleared the saved Jev model.\n")
			return nil
		},
	}
}
