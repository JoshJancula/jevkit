package security

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	securityconfig "github.com/JoshJancula/jevkit/internal/security/config"
)

func (a *App) securityEditCmd(verb string) *cobra.Command {
	var pattern, name string
	var project bool
	cmd := &cobra.Command{Use: verb, Short: verb + " a killswitch pattern", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if strings.TrimSpace(pattern) == "" {
				return app.Usagef("--killswitch is required")
			}
			if _, err := securityconfig.NewKillswitch([]string{pattern}); err != nil {
				return app.Usagef("%v", err)
			}
			opts := a.SecurityLoadOptions()
			if name != "" {
				opts.Name = name
			}
			if _, err := securityconfig.Load(opts); err != nil {
				return app.Failf("%v", err)
			}
			path := filepath.Join(a.WorkDir, ".jevkit", "security.yaml")
			if !project {
				selected := opts.Name
				if selected == "" {
					var err error
					selected, err = securityconfig.Default(a.ConfigDir)
					if err != nil {
						return app.Failf("%v", err)
					}
				}
				if selected == "builtin" {
					return app.Usagef("builtin is read-only; use security init and security use local")
				}
				path = securityconfig.PolicyPath(a.ConfigDir, selected)
			}
			raw, err := os.ReadFile(path)
			if os.IsNotExist(err) && project {
				raw = []byte("version: 1\nkillswitch: []\n")
			} else if err != nil {
				return app.Failf("%v", err)
			}
			var doc map[string]any
			if err := yaml.Unmarshal(raw, &doc); err != nil {
				return app.Failf("%v", err)
			}
			entries, _ := doc["killswitch"].([]any)
			next := make([]any, 0, len(entries)+1)
			found := false
			for _, entry := range entries {
				if entry == pattern {
					found = true
					if verb == "remove" {
						continue
					}
				}
				next = append(next, entry)
			}
			if verb == "add" && !found {
				next = append(next, pattern)
			}
			if verb == "remove" && !found {
				return app.Failf("killswitch pattern %q not found", pattern)
			}
			doc["killswitch"] = next
			body, err := yaml.Marshal(doc)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return err
			}
			if err := app.WriteAtomic(path, body, 0o600); err != nil {
				return err
			}
			a.Outf("%s %q in %s\n", verb, pattern, path)
			return nil
		}}
	cmd.Flags().StringVar(&pattern, "killswitch", "", "command glob to edit")
	cmd.Flags().StringVar(&name, "policy", "", "named user policy")
	cmd.Flags().BoolVar(&project, "project", false, "edit additive project policy")
	return cmd
}
