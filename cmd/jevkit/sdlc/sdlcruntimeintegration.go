package sdlc

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/sdlc/worker"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// These are personal, project-specific preferences. Agent hook files remain
// project-scoped, while the opt-in record stays out of the repository.
type sdlcRuntimeIntegration struct {
	Version     int  `json:"version"`
	Hooks       bool `json:"hooks"`
	Compaction  bool `json:"compaction"`
	KeepDefault bool `json:"keepDefault"`
}

func (a *App) sdlcRuntimeIntegrationPath() (string, error) {
	project, err := filepath.Abs(a.WorkDir)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(filepath.Clean(project)))
	return filepath.Join(a.StateHome(), "jevkit", "sdlc", "integrations", fmt.Sprintf("%x.json", digest[:16])), nil
}

func (a *App) loadSDLCRuntimeIntegration() (sdlcRuntimeIntegration, bool, error) {
	path, err := a.sdlcRuntimeIntegrationPath()
	if err != nil {
		return sdlcRuntimeIntegration{}, false, err
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return sdlcRuntimeIntegration{}, false, nil
	}
	if err != nil {
		return sdlcRuntimeIntegration{}, false, err
	}
	var choice sdlcRuntimeIntegration
	if err := json.Unmarshal(raw, &choice); err != nil || (choice.Version != 1 && choice.Version != 2) || (choice.Compaction && !choice.Hooks) {
		return sdlcRuntimeIntegration{}, false, fmt.Errorf("invalid SDLC integration preferences at %s", path)
	}
	if choice.Version == 1 {
		// The previous prompt saved every answer without asking whether it was
		// meant to be a default. Ask again before treating it as one.
		choice.KeepDefault = false
	}
	return choice, true, nil
}

func (a *App) saveSDLCRuntimeIntegration(choice sdlcRuntimeIntegration) error {
	if choice.Compaction && !choice.Hooks {
		return fmt.Errorf("tool-output compaction requires Jevkit hooks")
	}
	choice.Version = 2
	path, err := a.sdlcRuntimeIntegrationPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(choice, "", "  ")
	if err != nil {
		return err
	}
	return app.WriteAtomic(path, append(raw, '\n'), 0o600)
}

func (a *App) sdlcIntegrationsCmd() *cobra.Command {
	var hooks, compaction string
	var askEveryRun bool
	c := &cobra.Command{
		Use: "integrations", Short: "configure Jevkit hooks and tool-output compaction for SDLC agents",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if askEveryRun {
				if cmd.Flags().Changed("hooks") || cmd.Flags().Changed("compaction") {
					return app.Usagef("--ask-every-run cannot be combined with --hooks or --compaction")
				}
				path, err := a.sdlcRuntimeIntegrationPath()
				if err != nil {
					return err
				}
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					return err
				}
				a.Outf("SDLC integrations: ask at each new interactive run.\n")
				return nil
			}
			choice, configured, err := a.loadSDLCRuntimeIntegration()
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("hooks") {
				choice.Hooks = hooks == "on"
				if hooks != "on" && hooks != "off" {
					return app.Usagef("--hooks must be on or off")
				}
				if !choice.Hooks {
					choice.Compaction = false
				}
			}
			if cmd.Flags().Changed("compaction") {
				if compaction != "on" && compaction != "off" {
					return app.Usagef("--compaction must be on or off")
				}
				choice.Compaction = compaction == "on"
			}
			if choice.Compaction && !choice.Hooks {
				return app.Usagef("tool-output compaction requires --hooks on")
			}
			if cmd.Flags().Changed("hooks") || cmd.Flags().Changed("compaction") {
				choice.KeepDefault = true
				if err := a.saveSDLCRuntimeIntegration(choice); err != nil {
					return err
				}
				configured = true
			}
			if !configured || !choice.KeepDefault {
				a.Outf("SDLC integrations: ask at each new interactive run.\n")
				return nil
			}
			a.Outf("SDLC default hook auto-install: %t\nSDLC default tool-output compaction: %t\n", choice.Hooks, choice.Compaction)
			return nil
		},
	}
	c.Flags().StringVar(&hooks, "hooks", "", "auto-install project-scoped hooks for SDLC CLI agents: on or off")
	c.Flags().StringVar(&compaction, "compaction", "", "enable Jevkit tool-output compaction in SDLC CLI agents: on or off")
	c.Flags().BoolVar(&askEveryRun, "ask-every-run", false, "clear the saved default and prompt on each new interactive run")
	return c
}

func (a *App) sdlcAskYesNo(ctx context.Context, prompt string) (bool, error) {
	if a.Confirm != nil {
		return a.Confirm(prompt)
	}
	input, ok := a.Stdin.(*os.File)
	if !ok || !term.IsTerminal(int(input.Fd())) {
		return false, nil
	}
	_, _ = fmt.Fprintf(a.Stdout, "%s [y/N]: ", prompt)
	answer := byte(0)
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case b, ok := <-a.sdlcTTYInput():
			if !ok {
				return false, fmt.Errorf("SDLC prompt lost terminal input")
			}
			switch b {
			case 'y', 'Y':
				answer = 'y'
			case 'n', 'N':
				answer = 'n'
			case '\r', '\n':
				return answer == 'y', nil
			case 3:
				return false, context.Canceled
			}
		}
	}
}

func (a *App) chooseSDLCRuntimeIntegration(ctx context.Context, interactive bool) (*ledger.RuntimeIntegration, error) {
	choice, configured, err := a.loadSDLCRuntimeIntegration()
	if err != nil {
		return nil, err
	}
	if configured && choice.KeepDefault {
		return &ledger.RuntimeIntegration{Hooks: choice.Hooks, Compaction: choice.Compaction}, nil
	}
	if !interactive || (a.Confirm == nil && !a.sdlcInteractive()) {
		return &ledger.RuntimeIntegration{}, nil
	}
	choice = sdlcRuntimeIntegration{}
	choice.Hooks, err = a.sdlcAskYesNo(ctx, "Install Jevkit hooks for this SDLC run's CLI agents?")
	if err != nil {
		return nil, err
	}
	if choice.Hooks {
		choice.Compaction, err = a.sdlcAskYesNo(ctx, "Enable Jevkit tool-output compaction for this run?")
		if err != nil {
			return nil, err
		}
	}
	choice.KeepDefault, err = a.sdlcAskYesNo(ctx, "Keep these integration choices as my default for this project?")
	if err != nil {
		return nil, err
	}
	if choice.KeepDefault {
		if err := a.saveSDLCRuntimeIntegration(choice); err != nil {
			return nil, err
		}
	}
	return &ledger.RuntimeIntegration{Hooks: choice.Hooks, Compaction: choice.Compaction}, nil
}

func (a *App) applySDLCRuntimeIntegration(req *worker.Request, run ledger.Run) {
	if run.RuntimeIntegration == nil {
		return
	}
	req.JevkitHooks = run.RuntimeIntegration.Hooks
	req.JevkitBinary = a.ResolveBinary("")
	req.JevkitCompaction = &run.RuntimeIntegration.Compaction
}
