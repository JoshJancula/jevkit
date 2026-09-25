package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	securityconfig "github.com/OWNER/jevkit/internal/security/config"
)

func (a *App) securityCmd() *cobra.Command {
	cmd := a.group("security", "manage layered shell-command security policies",
		a.securityInitCmd(), a.securityListCmd(), a.securityUseCmd(),
		a.securityShowCmd(), a.securityEditCmd("add"), a.securityEditCmd("remove"),
		a.securityCheckCmd(), a.securityTestCmd())
	cmd.Long = "Manage layered security policies for shell commands. Optional Jev command-risk scoring can add a confidence-gated check when enabled in a named user policy or by JEVKIT_SECURITY_SCORING=1; deterministic killswitch and path checks still apply."
	return cmd
}

func (a *App) securityInitCmd() *cobra.Command {
	var project bool
	cmd := &cobra.Command{Use: "init", Short: "create a security policy template", Long: "Create a named user policy template with jev_scoring: false. With --project, create the additive killswitch-only .jevkit/security.yaml; project policies cannot set mode, sandbox, tests, or Jev scoring.", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			path := securityconfig.PolicyPath(a.ConfigDir, "local")
			if project {
				path = filepath.Join(a.WorkDir, ".jevkit", "security.yaml")
			}
			if path == "" {
				return failf("security config directory unavailable")
			}
			if _, err := os.Stat(path); err == nil {
				return failf("%s already exists", path)
			} else if !os.IsNotExist(err) {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return err
			}
			body := "version: 1\nkillswitch: []\n"
			if !project {
				body += "jev_scoring: false\nmode: enforce\nsandbox:\n  allow_read: []\ntests: []\n"
			}
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				return err
			}
			a.outf("%s\n", path)
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
				return failf("%v", err)
			}
			for _, name := range names {
				opts := a.securityLoadOptions()
				opts.Name = name
				if _, err := securityconfig.Load(opts); err != nil {
					return failf("%v", err)
				}
			}
			def, err := securityconfig.Default(a.ConfigDir)
			if err != nil {
				return failf("%v", err)
			}
			opts := a.securityLoadOptions()
			opts.Name = def
			if _, err := securityconfig.Load(opts); err != nil {
				return failf("%v", err)
			}
			for _, name := range names {
				if name == def {
					a.outf("* %s (default)\n", name)
				} else {
					a.outf("  %s\n", name)
				}
			}
			return nil
		}}
}

func (a *App) securityUseCmd() *cobra.Command {
	return &cobra.Command{Use: "use <name>", Short: "select the default policy", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			opts := a.securityLoadOptions()
			opts.Name = args[0]
			if _, err := securityconfig.Load(opts); err != nil {
				return failf("%v", err)
			}
			if err := securityconfig.SetDefault(a.ConfigDir, args[0]); err != nil {
				return failf("%v", err)
			}
			a.outf("default security policy: %s\n", args[0])
			return nil
		}}
}

func (a *App) securityShowCmd() *cobra.Command {
	return &cobra.Command{Use: "show [name]", Short: "show the effective policy", Long: "Show the effective layered policy, including the jev_scoring setting used for optional Jev command-risk scoring.", Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			opts := a.securityLoadOptions()
			if len(args) == 1 {
				opts.Name = args[0]
			}
			cfg, err := securityconfig.Load(opts)
			if err != nil {
				return failf("%v", err)
			}
			out, _ := json.MarshalIndent(map[string]any{
				"name": cfg.Name, "killswitch": cfg.Patterns, "jev_scoring": cfg.JevScoring,
				"mode": cfg.Mode, "sandbox": map[string]any{"allow_read": cfg.AllowRead},
			}, "", "  ")
			a.outf("%s\n", out)
			return nil
		}}
}

func securityCheckOutput(deny bool, reason string) string {
	if deny {
		return fmt.Sprintf("deny: %s", reason)
	}
	return "allow"
}
