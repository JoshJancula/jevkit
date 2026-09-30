package util

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/jev"
	"github.com/JoshJancula/jevkit/internal/keystore"
)

func (a *App) keyCmd() *cobra.Command {
	return a.Group("key", "manage the Typesafe API key",
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
				return app.Usagef("%s", keyArgvHint)
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
		return app.Usagef("invalid option; `key set` accepts only --command (%s)", keyArgvHint)
	})
	c.Flags().StringVar(&command, "command", "", "store a credential command whose stdout is the key")
	return c
}

func (a *App) keySetCommand(command string) error {
	if strings.TrimSpace(command) == "" {
		return app.Usagef("--command needs a command string")
	}
	if err := a.Store().SetCommand(command); err != nil {
		return app.Failf("%v", err)
	}
	a.Outf("Stored credential command. Run `jevkit key status` to confirm the source.\n")
	return nil
}

func (a *App) keySetSecret() error {
	key, err := a.readKey()
	if err != nil {
		return app.Failf("%v", err)
	}
	s := a.Store()
	if err := s.SetKeychain(key); err == nil {
		a.Outf("Stored the key in the OS keychain (service %s).\n", keystore.KeychainService)
		return nil
	}
	if err := s.SetFile(key); err != nil {
		return app.Failf("could not store the key: %v", err)
	}
	a.Outf("No keychain was available; stored the key in a 0600 file instead.\n")
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
	a.Errf("Typesafe API key (input hidden): ")
	b, err := term.ReadPassword(int(f.Fd()))
	a.Errf("\n")
	return b, true, err
}

func (a *App) keyStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "show where the key comes from (never the key)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s := a.Store()
			a.Outf("%s", s.Status(cmd.Context()))
			if s.Source(cmd.Context()) == keystore.SourceNone {
				return app.CodeErr(app.ExitFail)
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
			if err := a.Store().Clear(); err != nil {
				return app.Failf("%v", err)
			}
			a.Outf("Cleared the stored key and credential command.\n")
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
			key, _, err := a.Store().Resolve(cmd.Context())
			if err != nil {
				return app.Failf("fail (no usable key: %v)", err)
			}
			cfg, err := a.JevConfig()
			if err != nil {
				return app.Failf("%v", err)
			}
			client := app.UsageOrigin(a.JevClient(cfg, func() (string, error) { return key, nil }), "cli", "")
			_, err = client.Ask(cmd.Context(), jev.Request{
				State: "jevkit key test",
				Questions: map[string]jev.Question{
					"ok": jev.NoulQuestion{Instructions: "Is this API key valid?"},
				},
			})
			if err != nil {
				return app.Failf("fail (%s)", failReason(err))
			}
			if cfg.Transport == jev.TransportFixture {
				a.Outf("pass (fixture transport: the key was not sent)\n")
				return nil
			}
			a.Outf("pass\n")
			return nil
		},
	}
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
