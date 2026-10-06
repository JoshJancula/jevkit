package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/JoshJancula/jevkit/internal/jev"
)

func (a *App) ModelPath() string {
	if a.ConfigDir == "" {
		return ""
	}
	return filepath.Join(a.ConfigDir, "model")
}

func ValidModelName(model string) bool { return modelNamePattern.MatchString(model) }

func (a *App) storedModel() (string, error) {
	path := a.ModelPath()
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
	if !ValidModelName(model) {
		return "", fmt.Errorf("invalid model setting in %s", path)
	}
	return model, nil
}

// modelSelection chooses the process override, user setting, then built-in default.
func (a *App) ModelSelection() (model, source string, err error) {
	if model = a.Getenv("JEVKIT_MODEL"); model != "" {
		if !ValidModelName(model) {
			return "", "", fmt.Errorf("invalid JEVKIT_MODEL value")
		}
		return model, "JEVKIT_MODEL", nil
	}
	model, err = a.storedModel()
	if err != nil {
		return "", "", err
	}
	if model != "" {
		return model, a.ModelPath(), nil
	}
	return jev.DefaultModel, "built-in default", nil
}

func (a *App) JevConfig() (jev.Config, error) {
	cfg := jev.ConfigFromEnv(a.Getenv)
	model, _, err := a.ModelSelection()
	if err != nil {
		return cfg, err
	}
	cfg.Model = model
	return cfg, nil
}

var modelNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,127}$`)
