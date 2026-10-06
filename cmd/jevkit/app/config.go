package app

import (
	"errors"

	"github.com/JoshJancula/jevkit/internal/redact/config"
)

// load resolves the layered config, reporting a failure to stderr.
func (a *App) LoadConfig(cmd string) (*config.Config, bool) {
	cfg, err := config.Load(a.LoadOptions())
	if err != nil {
		a.Errf("jevkit %s: %v\n", cmd, UnwrapReason(err))
		return nil, false
	}
	return cfg, true
}

// unwrapReason prefers the wrapped config error, which names file and key.
func UnwrapReason(err error) error {
	var ce *config.Error
	if errors.As(err, &ce) {
		return ce
	}
	return err
}
