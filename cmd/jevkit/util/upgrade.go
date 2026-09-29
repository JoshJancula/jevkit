package util

import (
	"os"
	"path/filepath"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/upgrade"
	"github.com/spf13/cobra"
)

func (a *App) upgradeCmd() *cobra.Command {
	var check bool
	return &cobra.Command{Use: "upgrade", Short: "check for or install the latest release", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		exe, err := os.Executable()
		if err != nil {
			return app.Failf("locate executable: %v", err)
		}
		if command := upgrade.Managed(exe); command != "" {
			a.Outf("jevkit is package-managed; run: %s\n", command)
			return nil
		}
		c := upgrade.Client{BaseURL: "https://api.github.com/repos/JoshJancula/jevkit"}
		r, err := c.Latest()
		if err != nil {
			return app.Failf("check latest release: %v", err)
		}
		a.Outf("latest: %s (current: %s)\n", r.TagName, a.Version)
		if check {
			return nil
		}
		dir := filepath.Dir(exe)
		candidate := filepath.Join(dir, ".jevkit-upgrade-download")
		defer func() { _ = os.Remove(candidate) }()
		if err := c.Download(r, candidate); err != nil {
			return app.Failf("download verified release: %v", err)
		}
		if err := upgrade.Replace(exe, candidate); err != nil {
			return app.Failf("replace executable: %v", err)
		}
		a.Outf("upgraded to %s\n", r.TagName)
		return nil
	}}
}

func (a *App) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "print the jevkit version",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			a.Outf("jevkit %s\n", a.Version)
			return nil
		},
	}
}
