package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/OWNER/jevkit/internal/keystore"
)

// ralph's keychain entry and file names (bash-lib/jev/jev-key-store.sh).
const (
	ralphKeychainService = "ralph.jev"
	ralphCredentials     = "jev-credentials.json"
	ralphKeyFile         = "jev-api-key"
)

// ralphConfigDir mirrors ralph_model_store_config_dir: $RALPH_CONFIG_HOME,
// else $XDG_CONFIG_HOME/ralph, else ~/.config/ralph.
func (a *App) ralphConfigDir() (string, error) {
	if d := strings.TrimRight(a.getenv("RALPH_CONFIG_HOME"), "/"); d != "" {
		return d, nil
	}
	base := a.getenv("XDG_CONFIG_HOME")
	if base == "" {
		home := a.getenv("HOME")
		if home == "" {
			return "", errors.New("HOME, XDG_CONFIG_HOME or RALPH_CONFIG_HOME must be set to locate ralph's config")
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "ralph"), nil
}

func (a *App) migrateCmd() *cobra.Command {
	var force bool
	c := &cobra.Command{
		Use:   "migrate-from-ralph [--force]",
		Short: "one-time copy of ralph's key and credential config into jevkit",
		Long: `Copy the key configuration ralph's jev integration stored: the credential
command in ~/.config/ralph/jev-credentials.json and the key in the ralph.jev
keychain entry (or ralph's 0600 key file). The key is never printed and ralph's
own copy is left untouched.

Refuses to run when jevkit already has a stored key, unless --force is given.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.migrate(cmd, force)
		},
	}
	c.Flags().BoolVar(&force, "force", false, "replace an existing jevkit key configuration")
	return c
}

func (a *App) migrate(cmd *cobra.Command, force bool) error {
	dir, err := a.ralphConfigDir()
	if err != nil {
		return failf("%v", err)
	}
	s := a.store()
	switch src := s.Source(cmd.Context()); src {
	case keystore.SourceCommand, keystore.SourceKeychain, keystore.SourceFile:
		if !force {
			return failf("jevkit already has a stored key (source: %s); use --force to replace it", src)
		}
	}

	var creds struct {
		Command string `json:"command"`
		File    bool   `json:"file"`
	}
	if data, err := os.ReadFile(filepath.Join(dir, ralphCredentials)); err == nil {
		if err := json.Unmarshal(data, &creds); err != nil {
			return failf("%s is not valid JSON", filepath.Join(dir, ralphCredentials))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return failf("read ralph credentials: %v", err)
	}

	// A missing or unavailable keychain simply means there is no key there.
	key, _ := a.keyring().Get(ralphKeychainService, keystore.KeychainAccount)
	fromKeychain := key != ""
	if key == "" && creds.File {
		if data, err := os.ReadFile(filepath.Join(dir, ralphKeyFile)); err == nil {
			key = strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
		}
	}
	if key == "" && creds.Command == "" {
		return failf("nothing to migrate: no credential command, keychain entry or key file found for ralph in %s", dir)
	}

	if force {
		if err := s.Clear(); err != nil {
			return failf("clear existing key: %v", err)
		}
	}
	if creds.Command != "" {
		if err := s.SetCommand(creds.Command); err != nil {
			return failf("copy credential command: %v", err)
		}
		a.outf("Copied the credential command.\n")
	}
	if key != "" {
		where := "keychain"
		if err := s.SetKeychain(key); err != nil {
			if err := s.SetFile(key); err != nil {
				return failf("could not store the key: %v", err)
			}
			where = "0600 file"
		}
		origin := "ralph's key file"
		if fromKeychain {
			origin = "the ralph.jev keychain entry"
		}
		a.outf("Copied the key from %s to the jevkit %s.\n", origin, where)
	}
	a.outf("Ralph's own configuration was left untouched. Run `jevkit key status` to confirm.\n")
	return nil
}
