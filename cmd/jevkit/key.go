package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/OWNER/jevkit/internal/jev"
	"github.com/OWNER/jevkit/internal/keystore"
)

// Asker is the slice of the jev client `key test` uses.
type Asker interface {
	Ask(ctx context.Context, req jev.Request) (*jev.Response, error)
}

// store is the injected keystore, or one built from the app's directories.
func (a *App) store() *keystore.Store {
	if a.Keystore != nil {
		return a.Keystore
	}
	return &keystore.Store{
		Getenv:    a.getenv,
		Workspace: a.WorkDir,
		ConfigDir: a.ConfigDir,
		Keyring:   a.keyring(),
		Warn:      a.Stderr,
	}
}

func (a *App) keyring() keystore.Keyring {
	if a.Keyring != nil {
		return a.Keyring
	}
	return keystore.OSKeyring{}
}

func (a *App) keyCmd() *cobra.Command {
	return a.group("key", "manage the Typesafe API key",
		a.keySetCmd(), a.keyStatusCmd(), a.keyClearCmd(), a.keyTestCmd())
}

const keyArgvHint = "never pass the key as a command-line argument; pipe it via stdin or run `jevkit key set` for a hidden prompt"

func (a *App) keySetCmd() *cobra.Command {
	var command string
	c := &cobra.Command{
		Use:   "set [--command CMD]",
		Short: "store the key (hidden prompt on a terminal, else stdin) or a credential command",
		Long: `Store the API key. On a terminal the key is read with echo off; otherwise it
is read from stdin. The key is never accepted as an argument. It goes to the
OS keychain, or to a 0600 file when no keychain is available.

With --command, store a command whose stdout is the key instead; no secret is
stored.`,
		// The key must never ride in argv, so any positional argument is
		// refused without echoing it.
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) > 0 {
				return usagef("%s", keyArgvHint)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Flags().Changed("command") {
				return a.keySetCommand(command)
			}
			return a.keySetSecret()
		},
	}
	// A bad flag error can echo the rest of a shorthand argument, which may
	// be the key, so report generically.
	c.SetFlagErrorFunc(func(*cobra.Command, error) error {
		return usagef("invalid option; `key set` accepts only --command (%s)", keyArgvHint)
	})
	c.Flags().StringVar(&command, "command", "", "store a credential command whose stdout is the key")
	return c
}

func (a *App) keySetCommand(command string) error {
	if strings.TrimSpace(command) == "" {
		return usagef("--command needs a command string")
	}
	if err := a.store().SetCommand(command); err != nil {
		return failf("%v", err)
	}
	a.outf("Stored credential command. Run `jevkit key status` to confirm the source.\n")
	return nil
}

func (a *App) keySetSecret() error {
	key, err := a.readKey()
	if err != nil {
		return failf("%v", err)
	}
	s := a.store()
	if err := s.SetKeychain(key); err == nil {
		a.outf("Stored the key in the OS keychain (service %s).\n", keystore.KeychainService)
		return nil
	}
	if err := s.SetFile(key); err != nil {
		return failf("could not store the key: %v", err)
	}
	a.outf("No keychain was available; stored the key in a 0600 file instead.\n")
	return nil
}

// readKey reads the key with echo off on a terminal, else from stdin.
func (a *App) readKey() (string, error) {
	read := a.ReadSecret
	if read == nil {
		read = a.readTerminalSecret
	}
	secret, ok, err := read()
	if err != nil {
		return "", err
	}
	if !ok {
		return keystore.ReadKey(a.Stdin)
	}
	key := strings.TrimSpace(string(secret))
	if key == "" {
		return "", errors.New("empty key")
	}
	return key, nil
}

// readTerminalSecret prompts on stderr when stdin is a terminal.
func (a *App) readTerminalSecret() ([]byte, bool, error) {
	f, ok := a.Stdin.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return nil, false, nil
	}
	a.errf("Typesafe API key (input hidden): ")
	b, err := term.ReadPassword(int(f.Fd()))
	a.errf("\n")
	return b, true, err
}

func (a *App) keyStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "show where the key comes from (never the key)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s := a.store()
			a.outf("%s", s.Status(cmd.Context()))
			if s.Source(cmd.Context()) == keystore.SourceNone {
				return codeErr(exitFail)
			}
			return nil
		},
	}
}

func (a *App) keyClearCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clear",
		Short: "remove every stored key and credential command",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if err := a.store().Clear(); err != nil {
				return failf("%v", err)
			}
			a.outf("Cleared the stored key and credential command.\n")
			return nil
		},
	}
}

// keyTestCmd is the only command that makes a live call to the API.
func (a *App) keyTestCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "test",
		Short: "make one live call to check the key is accepted",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			key, _, err := a.store().Resolve(cmd.Context())
			if err != nil {
				return failf("fail (no usable key: %v)", err)
			}
			cfg, err := a.jevConfig()
			if err != nil {
				return failf("%v", err)
			}
			client := a.newJev(cfg, func() (string, error) { return key, nil })
			_, err = client.Ask(cmd.Context(), jev.Request{
				State: "jevkit key test",
				Questions: map[string]jev.Question{
					"ok": jev.NoulQuestion{Instructions: "Is this API key valid?"},
				},
			})
			if err != nil {
				return failf("fail (%s)", failReason(err))
			}
			if cfg.Transport == jev.TransportFixture {
				a.outf("pass (fixture transport: the key was not sent)\n")
				return nil
			}
			a.outf("pass\n")
			return nil
		},
	}
}

func (a *App) newJev(cfg jev.Config, key func() (string, error)) Asker {
	if a.NewJev != nil {
		return a.recordJev(a.NewJev(cfg, key), cfg)
	}
	return a.recordJev(jev.New(cfg, key), cfg)
}

// failReason names a client failure without any wrapped detail, which could
// carry response text.
func failReason(err error) string {
	var je *jev.Error
	if errors.As(err, &je) && je.Reason != "" {
		return fmt.Sprintf("%s, code %d", je.Reason, je.Code)
	}
	return "network error"
}
