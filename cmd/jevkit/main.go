// Command jevkit is the jevkit CLI entrypoint.
package main

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
)

// version is overridden at build time via -ldflags.
var version = "dev"

func main() {
	wd, _ := os.Getwd()
	app := &App{
		Stdin:     os.Stdin,
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
		Environ:   os.Environ(),
		WorkDir:   wd,
		ConfigDir: defaultConfigDir(os.Getenv),
		StateDir:  defaultStateDir(os.Getenv, runtime.GOOS),
		Version:   version,
		Dial:      (&net.Dialer{}).DialContext,
	}
	os.Exit(app.Run(os.Args[1:]))
}

// defaultConfigDir is $JEVKIT_CONFIG_DIR, else $JEVKIT_CONFIG_HOME (the name
// the keystore documents), else <user config dir>/jevkit.
func defaultConfigDir(getenv func(string) string) string {
	if d := getenv("JEVKIT_CONFIG_DIR"); d != "" {
		return d
	}
	if d := getenv("JEVKIT_CONFIG_HOME"); d != "" {
		return d
	}
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "jevkit")
	}
	return ""
}

// defaultStateDir is $JEVKIT_STATE_DIR, else <state home>/jevkit, where the
// state home is $XDG_STATE_HOME, %LOCALAPPDATA% on Windows, or ~/.local/state.
func defaultStateDir(getenv func(string) string, goos string) string {
	if d := getenv("JEVKIT_STATE_DIR"); d != "" {
		return d
	}
	if d := getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "jevkit")
	}
	if goos == "windows" {
		if d := getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "jevkit")
		}
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".local", "state", "jevkit")
	}
	return ""
}
