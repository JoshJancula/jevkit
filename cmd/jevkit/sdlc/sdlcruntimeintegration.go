package sdlc

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	MCP         bool `json:"mcp,omitempty"`
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
	var hooks, compaction, mcp string
	var askEveryRun bool
	c := &cobra.Command{
		Use: "integrations", Aliases: []string{"i", "int"}, Short: "configure Jevkit hooks, MCP, and tool-output compaction for SDLC agents",
		Long: `Show or save personal integration defaults for this project.
Use "sdlc i" or "sdlc int" as shorter forms.

Hooks and MCP auto-install during SDLC CLI invocations. Tool-output compaction
requires hooks. These defaults are separate from per-agent tool permissions;
use "sdlc agents c" for those configuration examples.`,
		Example: "  jevkit sdlc i\n  jevkit sdlc i --hooks on --compaction on --mcp on\n  jevkit sdlc i --ask-every-run",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if askEveryRun {
				if cmd.Flags().Changed("hooks") || cmd.Flags().Changed("compaction") || cmd.Flags().Changed("mcp") {
					return app.Usagef("--ask-every-run cannot be combined with --hooks, --mcp, or --compaction")
				}
				path, err := a.sdlcRuntimeIntegrationPath()
				if err != nil {
					return err
				}
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					return err
				}
				a.printSDLCIntegrationDefaults(sdlcRuntimeIntegration{}, false)
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
			if cmd.Flags().Changed("mcp") {
				if mcp != "on" && mcp != "off" {
					return app.Usagef("--mcp must be on or off")
				}
				choice.MCP = mcp == "on"
			}
			if choice.Compaction && !choice.Hooks {
				return app.Usagef("tool-output compaction requires --hooks on")
			}
			if cmd.Flags().Changed("hooks") || cmd.Flags().Changed("compaction") || cmd.Flags().Changed("mcp") {
				choice.KeepDefault = true
				if err := a.saveSDLCRuntimeIntegration(choice); err != nil {
					return err
				}
				configured = true
			}
			a.printSDLCIntegrationDefaults(choice, configured && choice.KeepDefault)
			return nil
		},
	}
	c.Flags().StringVar(&hooks, "hooks", "", "auto-install project-scoped hooks for SDLC CLI agents: on or off")
	c.Flags().StringVar(&mcp, "mcp", "", "auto-install project-scoped Jevkit MCP for SDLC CLI agents: on or off")
	c.Flags().StringVar(&compaction, "compaction", "", "enable Jevkit tool-output compaction in SDLC CLI agents: on or off")
	c.Flags().BoolVar(&askEveryRun, "ask-every-run", false, "clear the saved default and prompt on each new interactive run")
	return c
}

func (a *App) printSDLCIntegrationDefaults(choice sdlcRuntimeIntegration, saved bool) {
	a.Heading("SDLC INTEGRATIONS")
	a.Outf("  Project: %s\n", a.WorkDir)
	if saved {
		a.Outf("  Personal defaults saved for this project.\n")
	} else {
		a.Outf("  Defaults: ask at each new interactive run; noninteractive runs use off.\n")
		choice = sdlcRuntimeIntegration{}
	}
	a.Outf("\n")
	state := func(enabled bool) string {
		if enabled {
			return a.Styled(a.Stdout, app.ANSIGreen, "on")
		}
		return "off"
	}
	a.Table([]string{"FEATURE", "DEFAULT"}, [][]string{
		{"Hooks auto-install", state(choice.Hooks)},
		{"MCP auto-install", state(choice.MCP)},
		{"Tool-output compaction", state(choice.Compaction)},
	})
	a.Outf("\n  Configure: jevkit sdlc i --hooks on --mcp on --compaction on\n")
	a.Outf("  Reset:     jevkit sdlc i --ask-every-run\n")
	a.Outf("  Compaction requires hooks. Hooks / MCP install on the next CLI invocation.\n")
	a.Outf("  Per-agent tool permissions: jevkit sdlc agents c\n")
}

// Codex records trust against each exact hook definition for standalone runs.
// SDLC invocations with hooks enabled bypass that trust check.
func (a *App) codexHookReadiness(workDir string) string {
	path := filepath.Join(workDir, ".codex", "hooks.json")
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "not installed yet; SDLC will install hooks and bypass trust when hooks are enabled"
	}
	if err != nil {
		return "could not inspect hook file"
	}
	if !strings.Contains(string(raw), "_runtime dispatch --protocol 1 codex") {
		return "Jevkit hooks not installed"
	}
	home := a.Getenv("CODEX_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "installed; standalone Codex trust status unavailable (review with codex /hooks); SDLC runs with hooks enabled bypass trust"
		}
		home = filepath.Join(userHome, ".codex")
	}
	config, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		return "installed; standalone Codex trust status unavailable (review with codex /hooks); SDLC runs with hooks enabled bypass trust"
	}
	if !strings.Contains(string(config), `[hooks.state."`+path+`:`) {
		return "installed; standalone Codex trust review needed (run codex /hooks); SDLC runs with hooks enabled bypass trust"
	}
	return "installed; standalone Codex trust records present (verify current definitions with codex /hooks); SDLC runs with hooks enabled bypass trust"
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
		return &ledger.RuntimeIntegration{Hooks: choice.Hooks, Compaction: choice.Compaction, MCP: choice.MCP}, nil
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
	choice.MCP, err = a.sdlcAskYesNo(ctx, "Install Jevkit MCP for this run's CLI agents to ask Jev questions?")
	if err != nil {
		return nil, err
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
	return &ledger.RuntimeIntegration{Hooks: choice.Hooks, Compaction: choice.Compaction, MCP: choice.MCP}, nil
}

func (a *App) applySDLCRuntimeIntegration(req *worker.Request, run ledger.Run) {
	if run.RuntimeIntegration == nil {
		return
	}
	req.JevkitHooks = run.RuntimeIntegration.Hooks
	req.JevkitMCP = run.RuntimeIntegration.MCP
	req.JevkitBinary = a.ResolveBinary("")
	req.JevkitCompaction = &run.RuntimeIntegration.Compaction
}
