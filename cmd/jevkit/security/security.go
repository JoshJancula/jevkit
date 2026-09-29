package security

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/registry"
	"github.com/JoshJancula/jevkit/internal/security"
	securityconfig "github.com/JoshJancula/jevkit/internal/security/config"
	"github.com/JoshJancula/jevkit/internal/security/review"
	"golang.org/x/term"
)

func (a *App) securityCmd() *cobra.Command {
	cmd := a.Group("security", "manage layered shell-command security policies",
		a.securityInitCmd(), a.securityListCmd(), a.securityUseCmd(),
		a.securityShowCmd(), a.securityEditCmd("add"), a.securityEditCmd("remove"),
		a.securityCheckCmd(), a.securityTestCmd(), a.securityCheckOutputCmd(), a.securityReviewsCmd(), a.securityReviewCmd())
	cmd.Long = "Manage layered security policies for shell commands. Optional Jev command-risk scoring can add a confidence-gated check when enabled in a named user policy or by JEVKIT_SECURITY_SCORING=1; deterministic killswitch and path checks still apply."
	return cmd
}

func (a *App) securityCheckOutputCmd() *cobra.Command {
	var tool, runtime string
	c := &cobra.Command{Use: "check-output [file]", Short: "check untrusted output without setting a review latch", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		var source io.Reader = a.Stdin
		if len(args) == 1 {
			f, err := os.Open(args[0])
			if err != nil {
				return err
			}
			defer f.Close()
			source = f
		}
		raw, err := io.ReadAll(io.LimitReader(source, 10<<20))
		if err != nil {
			return err
		}
		cfg, err := securityconfig.Load(a.SecurityLoadOptions())
		if err != nil {
			return err
		}
		cfg.Injection.Mode = "enforce"
		cfg.Asker = a.SecurityAsker(cmd.Context())
		reg, _ := registry.Load()
		d := &registry.Decider{Registry: reg, StateDir: a.StateHome(), Getenv: a.Getenv}
		v := security.CheckInjection(cmd.Context(), cfg, security.InjectionRequest{Body: string(raw), Runtime: runtime, Tool: tool}, d)
		encoded, _ := json.MarshalIndent(v, "", "  ")
		a.Outf("%s\n", encoded)
		return nil
	}}
	c.Flags().StringVar(&tool, "tool", "manual", "tool name for policy matching")
	c.Flags().StringVar(&runtime, "runtime", "manual", "runtime label")
	return c
}

func (a *App) securityReviewsCmd() *cobra.Command {
	return &cobra.Command{Use: "reviews", Short: "list pending prompt-injection reviews", Args: cobra.NoArgs, RunE: func(_ *cobra.Command, _ []string) error {
		all, err := review.List(a.StateHome())
		if err != nil {
			return err
		}
		for _, r := range all {
			if r.Status == "pending" {
				a.Outf("%s  %s  %s  %s\n", r.ID, r.Runtime, r.Tool, r.Created.Format("2006-01-02 15:04:05"))
			}
		}
		return nil
	}}
}

func (a *App) securityReviewCmd() *cobra.Command {
	var showRaw, yes, allow, deny bool
	var note string
	c := &cobra.Command{Use: "review <id>", Short: "inspect and resolve one prompt-injection review", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		if yes {
			f, ok := a.Stdin.(*os.File)
			if !ok || !term.IsTerminal(int(f.Fd())) {
				return app.Failf("--yes requires an interactive TTY")
			}
		}
		r, err := review.Get(a.StateHome(), args[0])
		if err != nil {
			return err
		}
		a.Outf("Review: %s\nStatus: %s\nRuntime: %s\nSession: %s\nWorkspace: %s\nTool: %s\nInput: %s\nScore: %.2f  Confidence: %.2f\nReason: %s\nExcerpt: %s\n", r.ID, r.Status, r.Runtime, r.SessionKey, r.Workspace, r.Tool, r.ToolInput, r.Score, r.Confidence, r.Reason, r.Excerpt)
		if len(r.HeuristicHits) > 0 {
			a.Outf("Heuristic hits: %s\n", strings.Join(r.HeuristicHits, ", "))
		}
		if showRaw {
			raw, err := os.ReadFile(r.RawPointer)
			if err != nil {
				return err
			}
			shown := false
			if f, ok := a.Stdout.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
				pager := exec.Command("less", "-R")
				pager.Stdin = strings.NewReader(string(raw))
				pager.Stdout = a.Stdout
				pager.Stderr = a.Stderr
				shown = pager.Run() == nil
			}
			if !shown {
				_, _ = a.Stdout.Write(raw)
			}
		}
		if r.Status != "pending" {
			return nil
		}
		if allow && deny {
			return app.Failf("choose allow or deny")
		}
		f, tty := a.Stdin.(*os.File)
		if !tty || !term.IsTerminal(int(f.Fd())) {
			return app.Failf("review resolution requires an interactive TTY")
		}
		action := ""
		if allow {
			action = "allow"
		}
		if deny {
			action = "deny"
		}
		reader := bufio.NewReader(a.Stdin)
		if action == "" {
			a.Outf("Allow or deny this output? [allow/deny]: ")
			line, _ := reader.ReadString('\n')
			action = strings.ToLower(strings.TrimSpace(line))
		}
		if action != "allow" && action != "deny" {
			return app.Failf("review was not resolved")
		}
		if !yes {
			a.Outf("Confirm %s review %s? [yes/no]: ", action, r.ID)
			line, _ := reader.ReadString('\n')
			if strings.ToLower(strings.TrimSpace(line)) != "yes" {
				return app.Failf("review was not resolved")
			}
		}
		resolved, err := review.Resolve(a.StateHome(), r.ID, action, note)
		if err != nil {
			return err
		}
		a.Outf("Review %s %s.\n", r.ID, resolved.Status)
		if r.SDLCRunID != "" {
			a.Outf("Resume: jevkit sdlc resume %s\n", r.SDLCRunID)
		} else if r.Runtime == "claude" {
			a.Outf("Resume: claude --resume %s\n", r.SessionKey)
		}
		return nil
	}}
	c.Flags().BoolVar(&showRaw, "show-raw", false, "page through the original output locally")
	c.Flags().BoolVar(&yes, "yes", false, "skip second confirmation; requires a TTY")
	c.Flags().BoolVar(&allow, "allow", false, "allow this exact content hash")
	c.Flags().BoolVar(&deny, "deny", false, "deny this review")
	c.Flags().StringVar(&note, "note", "", "local note saved with the resolution")
	return c
}

func (a *App) securityInitCmd() *cobra.Command {
	var project bool
	cmd := &cobra.Command{Use: "init", Short: "create a security policy template", Long: "Create a named user policy template with jev_scoring: false. With --project, create an additive .jevkit/security.yaml; project policies can add killswitch rules and tighten injection settings.", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			path := securityconfig.PolicyPath(a.ConfigDir, "local")
			if project {
				path = filepath.Join(a.WorkDir, ".jevkit", "security.yaml")
			}
			if path == "" {
				return app.Failf("security config directory unavailable")
			}
			if _, err := os.Stat(path); err == nil {
				return app.Failf("%s already exists", path)
			} else if !os.IsNotExist(err) {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return err
			}
			body := "version: 1\nkillswitch: []\n"
			if !project {
				body += "jev_scoring: false\nmode: enforce\ninjection:\n  mode: off\n  scan: suspicious\n  max_bytes: 16384\n  halt_on: escalate\n  heuristic_halt: false\nsandbox:\n  allow_read: []\ntests: []\n"
			}
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				return err
			}
			a.Outf("%s\n", path)
			return nil
		}}
	cmd.Flags().BoolVar(&project, "project", false, "create additive project policy")
	return cmd
}

func (a *App) securityListCmd() *cobra.Command {
	return &cobra.Command{Use: "list", Short: "list named policies", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			names, err := securityconfig.List(a.ConfigDir)
			if err != nil {
				return app.Failf("%v", err)
			}
			for _, name := range names {
				opts := a.SecurityLoadOptions()
				opts.Name = name
				if _, err := securityconfig.Load(opts); err != nil {
					return app.Failf("%v", err)
				}
			}
			def, err := securityconfig.Default(a.ConfigDir)
			if err != nil {
				return app.Failf("%v", err)
			}
			opts := a.SecurityLoadOptions()
			opts.Name = def
			if _, err := securityconfig.Load(opts); err != nil {
				return app.Failf("%v", err)
			}
			for _, name := range names {
				if name == def {
					a.Outf("* %s (default)\n", name)
				} else {
					a.Outf("  %s\n", name)
				}
			}
			return nil
		}}
}

func (a *App) securityUseCmd() *cobra.Command {
	return &cobra.Command{Use: "use <name>", Short: "select the default policy", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			opts := a.SecurityLoadOptions()
			opts.Name = args[0]
			if _, err := securityconfig.Load(opts); err != nil {
				return app.Failf("%v", err)
			}
			if err := securityconfig.SetDefault(a.ConfigDir, args[0]); err != nil {
				return app.Failf("%v", err)
			}
			a.Outf("default security policy: %s\n", args[0])
			return nil
		}}
}

func (a *App) securityShowCmd() *cobra.Command {
	return &cobra.Command{Use: "show [name]", Short: "show the effective policy", Long: "Show the effective layered policy, including the jev_scoring setting used for optional Jev command-risk scoring.", Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			opts := a.SecurityLoadOptions()
			if len(args) == 1 {
				opts.Name = args[0]
			}
			cfg, err := securityconfig.Load(opts)
			if err != nil {
				return app.Failf("%v", err)
			}
			out, _ := json.MarshalIndent(map[string]any{
				"name": cfg.Name, "killswitch": cfg.Patterns, "jev_scoring": cfg.JevScoring,
				"mode": cfg.Mode, "injection": cfg.Injection, "sandbox": map[string]any{"allow_read": cfg.AllowRead},
			}, "", "  ")
			a.Outf("%s\n", out)
			return nil
		}}
}

func securityCheckOutput(deny bool, reason string) string {
	if deny {
		return fmt.Sprintf("deny: %s", reason)
	}
	return "allow"
}
