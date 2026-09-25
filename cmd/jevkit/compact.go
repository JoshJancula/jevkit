package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/OWNER/jevkit/internal/compact"
	"github.com/OWNER/jevkit/internal/jev"
	"github.com/OWNER/jevkit/internal/redact"
	"github.com/OWNER/jevkit/internal/registry"
	"gopkg.in/yaml.v3"
)

func (a *App) compactCmd() *cobra.Command {
	return a.group("compact", "inspect and edit safe compaction policy", a.compactInitCmd(), a.compactListCmd(), a.compactAddCmd(), a.compactRemoveCmd(), a.compactValidateCmd(), a.compactExplainCmd(), a.compactStatsCmd(), a.compactEvalCmd(), a.compactCaptureCmd())
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
		p, err := loadOrNewPolicy(path, project)
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
	var path, command, output, input string
	var project, withJev bool
	c := &cobra.Command{Use: "explain", Short: "show which policy rule would apply", Example: "  jevkit compact explain --command 'npm run build'\n  git diff | jevkit compact explain --command 'git diff' --output -", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
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
		if input != "" {
			b, err := os.ReadFile(input)
			if err != nil {
				return failf("read input: %v", err)
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
		if output != "" {
			body := output
			if withJev {
				if input == "" {
					return usagef("--jev requires --input so the original is retrievable")
				}
				client, _ := a.compactionClient(cmd.Context())
				if client == nil {
					return failf("jev unavailable")
				}
				pointer, err := filepath.Abs(input)
				if err != nil {
					return failf("resolve input: %v", err)
				}
				jr, _ := compact.JevCompact(command, output, "", 0, client, compact.JevOptions{Enabled: true, Policy: p, RawPointer: pointer})
				body = jr.Body
			} else {
				result := compact.Compact(command, output, "", 0, compact.Options{Policy: p})
				if result.Compacted {
					body = result.Stdout
				}
			}
			a.outf("before: %d bytes, %d lines\nafter: %d bytes, %d lines\nsaved: %d bytes\n", len(output), len(strings.Split(strings.TrimSuffix(output, "\n"), "\n")), len(body), len(strings.Split(strings.TrimSuffix(body, "\n"), "\n")), len(output)-len(body))
			a.outf("result:\n%s\n", body)
		}
		return nil
	}}
	c.Flags().StringVar(&path, "file", "", "policy file (default user policy)")
	c.Flags().StringVar(&command, "command", "", "tool command to evaluate")
	c.Flags().StringVar(&output, "output", "", "optional output text, or - for stdin")
	c.Flags().StringVar(&input, "input", "", "original output file to dry-run")
	c.Flags().BoolVar(&withJev, "jev", false, "include the Jev classifier")
	c.Flags().BoolVar(&project, "project", false, "use project policy")
	return c
}

func (a *App) compactStatsCmd() *cobra.Command {
	return &cobra.Command{Use: "stats", Short: "summarize recorded compaction decisions", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		path := registry.DecisionsPath(a.stateHome())
		f, err := os.Open(path)
		if os.IsNotExist(err) {
			a.outf("no compaction decisions recorded\n")
			return nil
		}
		if err != nil {
			return failf("read decisions: %v", err)
		}
		defer f.Close()
		counts := map[string]int{}
		families := map[string]int{}
		histogram := [5]int{}
		calls, fallback, saved := 0, 0, 0
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			var d registry.Decision
			if json.Unmarshal(scanner.Bytes(), &d) != nil || d.Surface != "compaction" ||
				(d.QuestionSetID != "compaction.triage.v2" && d.QuestionSetID != "compaction.disposition.v1") {
				continue
			}
			calls++
			if d.Chosen != nil {
				counts[*d.Chosen]++
			}
			if d.CommandFamily != "" {
				families[d.CommandFamily]++
			}
			if d.FallbackUsed {
				fallback++
			}
			if !d.Shadow && d.BytesBefore > d.BytesAfter {
				saved += d.BytesBefore - d.BytesAfter
			}
			bucket := int(d.Confidence * 5)
			if bucket < 0 {
				bucket = 0
			}
			if bucket > 4 {
				bucket = 4
			}
			histogram[bucket]++
		}
		if err := scanner.Err(); err != nil {
			return failf("read decisions: %v", err)
		}
		a.outf("decisions: %d\nfallback: %d\nbytes saved: %d\n", calls, fallback, saved)
		keys := make([]string, 0, len(counts))
		for k := range counts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			a.outf("  %s: %d\n", k, counts[k])
		}
		a.outf("confidence [0-.2, .2-.4, .4-.6, .6-.8, .8-1]: %v\n", histogram)
		keys = keys[:0]
		for k := range families {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			a.outf("family %s: %d\n", k, families[k])
		}
		return nil
	}}
}

func (a *App) compactEvalCmd() *cobra.Command {
	var corpusDir string
	var withJev bool
	c := &cobra.Command{Use: "eval", Short: "measure diagnosis recall and bytes saved on a labeled corpus", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cases, err := compact.ReadCorpus(corpusDir)
		if err != nil {
			return failf("read corpus: %v", err)
		}
		if len(cases) == 0 {
			return failf("corpus is empty: %s", corpusDir)
		}
		var asker compact.Asker
		var policy *compact.Policy
		var pointer string
		if withJev {
			asker, policy = a.compactionClient(cmd.Context())
			if asker == nil {
				return failf("jev unavailable")
			}
			file, err := os.CreateTemp("", "jevkit-compact-eval-*.log")
			if err != nil {
				return failf("create evaluation pointer: %v", err)
			}
			pointer = file.Name()
			defer func() { _ = os.Remove(pointer) }()
			if err := file.Close(); err != nil {
				return failf("close evaluation pointer: %v", err)
			}
		}
		modelUsed, modelFallback := 0, 0
		modelErrors := make(map[string]int)
		firstModelError := ""
		var evalErr error
		m := compact.EvaluateCorpus(cases, func(c compact.CorpusCase) string {
			original := c.Original()
			if withJev {
				if evalErr != nil {
					return original
				}
				if err := os.WriteFile(pointer, []byte(original), 0o600); err != nil {
					evalErr = err
					return original
				}
				jr, _ := compact.JevCompact(c.Command, original, "", c.Exit, asker, compact.JevOptions{Enabled: true, AuthoritativeExit: true, RawPointer: pointer, Policy: policy, Runtime: "eval"})
				if jr.Err != nil {
					if firstModelError == "" {
						firstModelError = jr.Err.Error()
					}
					var je *jev.Error
					if errors.As(jr.Err, &je) {
						modelErrors[fmt.Sprintf("%d:%s", je.Code, je.Reason)]++
					} else {
						modelErrors["local-decision"]++
					}
				}
				if jr.Used {
					modelUsed++
				} else {
					modelFallback++
				}
				return jr.Body
			}
			r := compact.Compact(c.Command, original, "", c.Exit, compact.Options{})
			if r.Compacted {
				return r.Stdout
			}
			return original
		})
		if evalErr != nil {
			return failf("evaluate corpus: %v", evalErr)
		}
		a.outf("cases: %d (failures: %d)\nrecall: %.3f (%d/%d)\nfailure recall: %.3f (%d/%d)\nbytes saved: %d/%d\npromotable: %t\n", m.Cases, m.Failures, m.Recall(), m.RetainedGold, m.GoldLines, m.FailureRecall(), m.RetainedFailureGold, m.FailureGoldLines, m.Saved(), m.BytesBefore, m.Promotable())
		if withJev {
			a.outf("jev used: %d\nmodel not used: %d\n", modelUsed, modelFallback)
			keys := make([]string, 0, len(modelErrors))
			for key := range modelErrors {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				a.outf("classifier error %s: %d\n", key, modelErrors[key])
			}
			if firstModelError != "" {
				a.outf("first classifier error: %s\n", firstModelError)
			}
		}
		return nil
	}}
	c.Flags().StringVar(&corpusDir, "corpus", filepath.Join("testdata", "compact-corpus"), "directory of labeled JSON corpus cases")
	c.Flags().BoolVar(&withJev, "jev", false, "include the live Jev tier (sends redacted corpus evidence)")
	return c
}

func (a *App) compactCaptureCmd() *cobra.Command {
	var command, input, kind string
	var exit int
	var gold []string
	c := &cobra.Command{Use: "capture", Short: "redact and save a local labeled output fixture", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		if command == "" || input == "" || kind == "" || len(gold) == 0 {
			return usagef("--command, --input, --kind, and at least one --gold are required")
		}
		b, err := os.ReadFile(input)
		if err != nil {
			return failf("read original: %v", err)
		}
		r, err := redact.New(redact.Options{})
		if err != nil {
			return failf("redactor: %v", err)
		}
		clean, err := r.Apply(string(b))
		if err != nil {
			return failf("redact original: %v", err)
		}
		cleanCommand, err := r.Apply(command)
		if err != nil {
			return failf("redact command: %v", err)
		}
		cleanGold := make([]string, len(gold))
		for i, line := range gold {
			redacted, err := r.Apply(line)
			if err != nil {
				return failf("redact gold: %v", err)
			}
			cleanGold[i] = redacted.Text
			found := false
			for _, originalLine := range strings.Split(clean.Text, "\n") {
				if originalLine == cleanGold[i] {
					found = true
					break
				}
			}
			if !found {
				return failf("gold line %d is absent from the redacted original", i+1)
			}
		}
		caseName := fmt.Sprintf("local-%d", time.Now().UnixNano())
		caseFile := compact.CorpusCase{Name: caseName, Kind: kind, Command: cleanCommand.Text, Exit: exit, Prefix: clean.Text, Gold: cleanGold}
		encoded, err := json.MarshalIndent(caseFile, "", "  ")
		if err != nil {
			return failf("encode capture: %v", err)
		}
		dir := filepath.Join(a.WorkDir, ".jevkit", "compact-corpus", "local")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return failf("create corpus directory: %v", err)
		}
		path := filepath.Join(dir, caseName+".json")
		if err := os.WriteFile(path, append(encoded, '\n'), 0o600); err != nil {
			return failf("write capture: %v", err)
		}
		a.outf("captured %s\n", path)
		return nil
	}}
	c.Flags().StringVar(&command, "command", "", "original command")
	c.Flags().StringVar(&input, "input", "", "raw original output file")
	c.Flags().StringVar(&kind, "kind", "", "content kind label")
	c.Flags().IntVar(&exit, "exit", 0, "observed exit status")
	c.Flags().StringArrayVar(&gold, "gold", nil, "diagnostic source line; repeat for multiple lines")
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
