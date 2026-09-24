package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/OWNER/jevkit/internal/compact"
	"gopkg.in/yaml.v3"
)

func (a *App) compactCmd() *cobra.Command {
	return a.group("compact", "inspect and edit safe compaction policy", a.compactInitCmd(), a.compactListCmd(), a.compactAddCmd(), a.compactRemoveCmd(), a.compactValidateCmd(), a.compactExplainCmd())
}

func (a *App) compactInitCmd() *cobra.Command {
	var project bool
	c := &cobra.Command{Use: "init", Short: "create a commented compaction policy", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		path := a.compactionPolicyPath("", project)
		if _, err := os.Stat(path); err == nil {
			return failf("%s already exists", path)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return failf("create policy directory: %v", err)
		}
		body := "# Declarative compaction policy. Hard source/binary protections always win.\nversion: 1\nrules: []\n"
		if err := writeAtomic(path, []byte(body), 0o600); err != nil {
			return failf("write policy: %v", err)
		}
		a.outf("created %s\n", path)
		return nil
	}}
	c.Flags().BoolVar(&project, "project", false, "create additive project policy")
	return c
}

func (a *App) compactListCmd() *cobra.Command {
	var project bool
	c := &cobra.Command{Use: "list", Short: "list configured compaction rules", Example: "  jevkit compact list\n  jevkit compact list --project", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		p, err := loadOrNewPolicy(a.compactionPolicyPath("", project), project)
		if err != nil {
			return failf("%v", err)
		}
		if len(p.Rules) == 0 {
			a.outf("no compaction rules\n")
			return nil
		}
		rows := make([][]string, 0, len(p.Rules))
		for _, r := range p.Rules {
			command, output, threshold := "—", "—", "default"
			if r.Command != "" {
				command = r.Command
			}
			if r.Output != "" {
				output = r.Output
			}
			if r.Threshold > 0 {
				threshold = fmt.Sprintf("%d bytes", r.Threshold)
			}
			rows = append(rows, []string{r.ID, r.Action, command, output, threshold})
		}
		a.heading("Compaction rules")
		a.table([]string{"ID", "ACTION", "COMMAND MATCHER", "OUTPUT MATCHER", "THRESHOLD"}, rows)
		return nil
	}}
	c.Flags().BoolVar(&project, "project", false, "use project policy")
	return c
}

func (a *App) compactAddCmd() *cobra.Command {
	var project bool
	var id, action, command, output string
	var threshold int
	c := &cobra.Command{Use: "add", Short: "add a declarative compaction rule", Example: "  jevkit compact add --id preserve-build --action never-compact --command '^npm run build'", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		if id == "" || action == "" || (command == "" && output == "") {
			return usagef("--id, --action, and --command or --output are required")
		}
		path := a.compactionPolicyPath("", project)
		p, err := loadOrNewPolicy(path, project)
		if err != nil {
			return failf("%v", err)
		}
		p.Rules = append(p.Rules, compact.Rule{ID: id, Action: action, Command: command, Output: output, Threshold: threshold})
		if err := savePolicy(path, p, project); err != nil {
			return failf("%v", err)
		}
		a.outf("added %s\n", id)
		return nil
	}}
	c.Flags().BoolVar(&project, "project", false, "write additive project policy")
	c.Flags().StringVar(&id, "id", "", "stable rule id")
	c.Flags().StringVar(&action, "action", "", "never-compact, deterministic-only, or eligible")
	c.Flags().StringVar(&command, "command", "", "RE2 command matcher")
	c.Flags().StringVar(&output, "output", "", "RE2 output matcher")
	c.Flags().IntVar(&threshold, "threshold-bytes", 0, "optional threshold override")
	return c
}

func (a *App) compactRemoveCmd() *cobra.Command {
	var project bool
	c := &cobra.Command{Use: "remove <id>", Short: "remove a compaction rule", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		path := a.compactionPolicyPath("", project)
		p, err := compact.LoadPolicy(path, project)
		if err != nil {
			return failf("%v", err)
		}
		kept := p.Rules[:0]
		found := false
		for _, r := range p.Rules {
			if r.ID == args[0] {
				found = true
				continue
			}
			kept = append(kept, r)
		}
		if !found {
			return failf("rule %q not found", args[0])
		}
		p.Rules = kept
		if err := savePolicy(path, p, project); err != nil {
			return failf("%v", err)
		}
		a.outf("removed %s\n", args[0])
		return nil
	}}
	c.Flags().BoolVar(&project, "project", false, "use project policy")
	return c
}

func loadOrNewPolicy(path string, project bool) (*compact.Policy, error) {
	p, err := compact.LoadPolicy(path, project)
	if os.IsNotExist(err) {
		return &compact.Policy{Version: 1}, nil
	}
	return p, err
}
func savePolicy(path string, p *compact.Policy, project bool) error {
	b, err := yaml.Marshal(p)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".candidate"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp) }()
	if _, err := compact.LoadPolicy(tmp, project); err != nil {
		return err
	}
	return writeAtomic(path, b, 0o600)
}

func (a *App) compactValidateCmd() *cobra.Command {
	var path string
	var project bool
	c := &cobra.Command{Use: "validate", Short: "validate a declarative compaction policy", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		path = a.compactionPolicyPath(path, project)
		p, err := compact.LoadPolicy(path, project)
		if err != nil {
			return failf("%v", err)
		}
		a.outf("compaction policy ok: %s (%d rule(s))\n", path, len(p.Rules))
		return nil
	}}
	c.Flags().StringVar(&path, "file", "", "policy file (default user policy)")
	c.Flags().BoolVar(&project, "project", false, "validate .jevkit/compaction.yaml as an additive project policy")
	return c
}

func (a *App) compactExplainCmd() *cobra.Command {
	var path, command, output string
	var project bool
	c := &cobra.Command{Use: "explain", Short: "show which policy rule would apply", Example: "  jevkit compact explain --command 'npm run build'\n  git diff | jevkit compact explain --command 'git diff' --output -", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		if command == "" {
			return usagef("--command is required")
		}
		path = a.compactionPolicyPath(path, project)
		p, err := compact.LoadPolicy(path, project)
		if err != nil {
			return failf("%v", err)
		}
		if output == "-" {
			b, err := io.ReadAll(a.Stdin)
			if err != nil {
				return failf("read stdin: %v", err)
			}
			output = string(b)
		}
		r := p.Match(command, output)
		family := compact.Classify(command)
		if compact.IsSourceFamily(family) {
			a.outf("hard protection: %s (policy cannot override)\n", family)
			return nil
		}
		a.outf("policy: %s\n", r.String())
		return nil
	}}
	c.Flags().StringVar(&path, "file", "", "policy file (default user policy)")
	c.Flags().StringVar(&command, "command", "", "tool command to evaluate")
	c.Flags().StringVar(&output, "output", "", "optional output text, or - for stdin")
	c.Flags().BoolVar(&project, "project", false, "use project policy")
	return c
}

func (a *App) compactionPolicyPath(path string, project bool) string {
	if path != "" {
		return path
	}
	if project {
		return filepath.Join(a.WorkDir, ".jevkit", "compaction.yaml")
	}
	return filepath.Join(a.ConfigDir, "compaction.yaml")
}
