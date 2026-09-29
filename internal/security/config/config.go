// Package config loads layered security policies. Project policy is additive only.
package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/JoshJancula/jevkit/internal/globmatch"
	"github.com/JoshJancula/jevkit/internal/jev"
	"gopkg.in/yaml.v3"
)

var policyName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

type Config struct {
	Name       string
	StateDir   string
	Killswitch *Killswitch
	Patterns   []string
	AllowRead  []string
	JevScoring bool
	Mode       string
	Yolo       bool
	Injection  Injection
	Tests      []Test
	Asker      interface {
		Ask(context.Context, jev.Request) (*jev.Response, error)
	}
}

type Injection struct {
	Mode          string   `yaml:"mode" json:"mode"`
	Scan          string   `yaml:"scan" json:"scan"`
	MaxBytes      int      `yaml:"max_bytes" json:"max_bytes"`
	Tools         []string `yaml:"tools" json:"tools"`
	HeuristicHalt bool     `yaml:"heuristic_halt" json:"heuristic_halt"`
	HaltOn        string   `yaml:"halt_on" json:"halt_on"`
}

type Test struct {
	Command string `yaml:"command" json:"command"`
	Deny    bool   `yaml:"deny" json:"deny"`
}

type LoadOptions struct {
	ConfigDir, StateDir, WorkDir, Name string
	Environ                            []string
}

type fileSpec struct {
	Version    int        `yaml:"version"`
	Killswitch []string   `yaml:"killswitch"`
	JevScoring *bool      `yaml:"jev_scoring"`
	Mode       string     `yaml:"mode"`
	Injection  *Injection `yaml:"injection"`
	Sandbox    *struct {
		AllowRead []string `yaml:"allow_read"`
	} `yaml:"sandbox"`
	Tests []Test `yaml:"tests"`
}

// Builtin has a small, conservative destructive-command blocklist. Scoring
// starts disabled because a Jev request on each hook adds latency.
func Builtin(opts LoadOptions) (*Config, error) {
	home, _ := os.UserHomeDir()
	allow := []string{}
	for _, name := range []string{".claude", ".codex", ".cursor", ".opencode"} {
		if home != "" {
			allow = append(allow, filepath.Join(home, name))
		}
	}
	for _, path := range []string{opts.ConfigDir, opts.StateDir} {
		if path != "" {
			allow = append(allow, path)
		}
	}
	patterns := []string{"rm -rf /", "rm -rf /*", "rm -fr /", "rm -fr /*", "jevkit security review*"}
	ks, err := NewKillswitch(patterns)
	if err != nil {
		return nil, err
	}
	return &Config{Name: "builtin", StateDir: opts.StateDir, Patterns: patterns, Killswitch: ks, AllowRead: allow, Mode: "enforce", Injection: Injection{Mode: "off", Scan: "suspicious", MaxBytes: 16384, HaltOn: "escalate"}}, nil
}

func Load(opts LoadOptions) (*Config, error) {
	cfg, err := Builtin(opts)
	if err != nil {
		return nil, err
	}
	name := opts.Name
	if name == "" {
		name = envValue(opts.Environ, "JEVKIT_SECURITY_POLICY")
	}
	if name == "" {
		name, err = Default(opts.ConfigDir)
		if err != nil {
			return nil, err
		}
	}
	if !policyName.MatchString(name) {
		return nil, fmt.Errorf("invalid security policy name %q", name)
	}
	cfg.Name = name
	if name != "builtin" {
		path := PolicyPath(opts.ConfigDir, name)
		if path == "" {
			return nil, errors.New("security policy config directory unavailable")
		}
		spec, err := readSpec(path, false)
		if err != nil {
			return nil, err
		}
		applyUser(cfg, spec)
	}
	if opts.WorkDir != "" {
		project := filepath.Join(opts.WorkDir, ".jevkit", "security.yaml")
		spec, err := readSpec(project, true)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if err == nil {
			cfg.Patterns = append(cfg.Patterns, spec.Killswitch...)
			if spec.Injection != nil {
				tightenInjection(&cfg.Injection, *spec.Injection)
			}
		}
	}
	cfg.Killswitch, err = NewKillswitch(cfg.Patterns)
	if err != nil {
		return nil, err
	}
	if v, ok := envBool(opts.Environ, "JEVKIT_SECURITY_SCORING"); ok {
		cfg.JevScoring = v
	}
	if v, ok := envBool(opts.Environ, "JEVKIT_INJECTION_GUARD"); ok {
		if v {
			cfg.Injection.Mode = "enforce"
		} else {
			cfg.Injection.Mode = "off"
		}
	}
	cfg.Yolo, _ = envBool(opts.Environ, "JEVKIT_YOLO")
	return cfg, nil
}

// LoadForHook keeps the embedded killswitch active when a local policy fails
// to load. The error is returned so the caller can record telemetry.
func LoadForHook(opts LoadOptions) (*Config, error) {
	cfg, err := Load(opts)
	if err == nil {
		return cfg, nil
	}
	fallback, builtinErr := Builtin(opts)
	if builtinErr != nil {
		return nil, builtinErr
	}
	fallback.Yolo, _ = envBool(opts.Environ, "JEVKIT_YOLO")
	return fallback, err
}

func readSpec(path string, project bool) (fileSpec, error) {
	var spec fileSpec
	raw, err := os.ReadFile(path)
	if err != nil {
		return spec, err
	}
	var keys map[string]any
	if err := yaml.Unmarshal(raw, &keys); err != nil {
		return spec, fmt.Errorf("%s: %w", path, err)
	}
	allowed := map[string]bool{"version": true, "killswitch": true, "injection": true}
	if !project {
		for _, key := range []string{"jev_scoring", "mode", "sandbox", "tests"} {
			allowed[key] = true
		}
	}
	for key := range keys {
		if !allowed[key] {
			return spec, fmt.Errorf("%s: forbidden policy key %q", path, key)
		}
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&spec); err != nil {
		return spec, fmt.Errorf("%s: %w", path, err)
	}
	if spec.Version != 1 {
		return spec, fmt.Errorf("%s: version must be 1", path)
	}
	if spec.Mode != "" && spec.Mode != "enforce" && spec.Mode != "shadow" {
		return spec, fmt.Errorf("%s: mode must be enforce or shadow", path)
	}
	if spec.Injection != nil {
		i := spec.Injection
		if i.Mode != "" && i.Mode != "off" && i.Mode != "shadow" && i.Mode != "enforce" {
			return spec, fmt.Errorf("%s: invalid injection mode", path)
		}
		if i.HaltOn != "" && i.HaltOn != "act" && i.HaltOn != "escalate" {
			return spec, fmt.Errorf("%s: invalid injection halt_on", path)
		}
		if i.Scan != "" && i.Scan != "all" && i.Scan != "suspicious" {
			return spec, fmt.Errorf("%s: invalid injection scan", path)
		}
		if i.MaxBytes < 0 {
			return spec, fmt.Errorf("%s: invalid injection max_bytes", path)
		}
	}
	if _, err := NewKillswitch(spec.Killswitch); err != nil {
		return spec, fmt.Errorf("%s: %w", path, err)
	}
	if spec.Sandbox != nil {
		for _, allowed := range spec.Sandbox.AllowRead {
			if strings.TrimSpace(allowed) == "" {
				return spec, fmt.Errorf("%s: sandbox allow_read path is empty", path)
			}
			if strings.ContainsAny(allowed, "*?") {
				if _, err := globmatch.Compile(allowed); err != nil {
					return spec, fmt.Errorf("%s: sandbox allow_read: %w", path, err)
				}
			}
		}
	}
	for i, test := range spec.Tests {
		if strings.TrimSpace(test.Command) == "" {
			return spec, fmt.Errorf("%s: tests[%d].command is empty", path, i)
		}
	}
	return spec, nil
}

func applyUser(cfg *Config, spec fileSpec) {
	cfg.Patterns = append(cfg.Patterns, spec.Killswitch...)
	if spec.JevScoring != nil {
		cfg.JevScoring = *spec.JevScoring
	}
	if spec.Mode != "" {
		cfg.Mode = spec.Mode
	}
	if spec.Injection != nil {
		applyInjection(&cfg.Injection, *spec.Injection)
	}
	if spec.Sandbox != nil {
		cfg.AllowRead = append(cfg.AllowRead, spec.Sandbox.AllowRead...)
	}
	cfg.Tests = append(cfg.Tests, spec.Tests...)
}

func applyInjection(dst *Injection, src Injection) {
	if src.Mode != "" {
		dst.Mode = src.Mode
	}
	if src.Scan != "" {
		dst.Scan = src.Scan
	}
	if src.MaxBytes > 0 {
		dst.MaxBytes = src.MaxBytes
	}
	if src.Tools != nil {
		dst.Tools = append([]string(nil), src.Tools...)
	}
	if src.HaltOn != "" {
		dst.HaltOn = src.HaltOn
	}
	dst.HeuristicHalt = src.HeuristicHalt
}
func tightenInjection(dst *Injection, src Injection) {
	rank := map[string]int{"off": 0, "shadow": 1, "enforce": 2}
	if rank[src.Mode] > rank[dst.Mode] {
		dst.Mode = src.Mode
	}
	if src.HeuristicHalt {
		dst.HeuristicHalt = true
	}
	if src.HaltOn == "escalate" {
		dst.HaltOn = "escalate"
	}
	if src.Scan == "all" {
		dst.Scan = "all"
	}
	if src.MaxBytes > dst.MaxBytes {
		dst.MaxBytes = src.MaxBytes
	}
	if len(dst.Tools) > 0 {
		seen := map[string]bool{}
		for _, tool := range dst.Tools {
			seen[tool] = true
		}
		for _, tool := range src.Tools {
			if !seen[tool] {
				dst.Tools = append(dst.Tools, tool)
				seen[tool] = true
			}
		}
	}
}

func PolicyPath(configDir, name string) string {
	if configDir == "" || !policyName.MatchString(name) {
		return ""
	}
	return filepath.Join(configDir, "security", name+".yaml")
}

func Default(configDir string) (string, error) {
	if configDir == "" {
		return "builtin", nil
	}
	raw, err := os.ReadFile(filepath.Join(configDir, "security", "default"))
	if errors.Is(err, os.ErrNotExist) {
		return "builtin", nil
	}
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(string(raw))
	if !policyName.MatchString(name) {
		return "", fmt.Errorf("invalid default security policy %q", name)
	}
	return name, nil
}

func SetDefault(configDir, name string) error {
	if !policyName.MatchString(name) || configDir == "" {
		return fmt.Errorf("invalid security policy name or config directory")
	}
	if name != "builtin" {
		if _, err := os.Stat(PolicyPath(configDir, name)); err != nil {
			return err
		}
	}
	dir := filepath.Join(configDir, "security")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "default"), []byte(name+"\n"), 0o600)
}

func List(configDir string) ([]string, error) {
	names := []string{"builtin"}
	if configDir == "" {
		return names, nil
	}
	entries, err := os.ReadDir(filepath.Join(configDir, "security"))
	if errors.Is(err, os.ErrNotExist) {
		return names, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		name := strings.TrimSuffix(entry.Name(), ".yaml")
		if !entry.IsDir() && name != entry.Name() && name != "builtin" && policyName.MatchString(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

func envValue(environ []string, key string) string {
	for i := len(environ) - 1; i >= 0; i-- {
		item := environ[i]
		if v, ok := strings.CutPrefix(item, key+"="); ok {
			return v
		}
	}
	return ""
}

func envBool(environ []string, key string) (bool, bool) {
	v := strings.ToLower(strings.TrimSpace(envValue(environ, key)))
	switch v {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	}
	return false, false
}
