package config

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"gopkg.in/yaml.v3"

	"github.com/OWNER/jevkit/internal/jev"
	"github.com/OWNER/jevkit/internal/redact"
)

//go:embed schema.json
var schemaJSON []byte

const (
	schemaID = "https://jevkit.invalid/schema/redact-v1.json"
	// Version is the only supported `version:` value.
	Version = 1
	// maxFileBytes bounds a config file; anything larger fails closed.
	maxFileBytes   = 256 << 10
	minLiteralLen  = 4
	minEnvValueLen = 4
)

// Error describes one problem in one config file. Every Load failure wraps an
// *Error in a *jev.Error with jev.CodeRejected, so callers send nothing and
// hooks pass output through unchanged.
type Error struct {
	File string
	// Key is the offending key path such as "rules[1].pattern"; empty when the
	// whole file is at fault.
	Key string
	Msg string
}

func (e *Error) Error() string {
	if e.Key == "" {
		return fmt.Sprintf("redact config %s: %s", e.File, e.Msg)
	}
	return fmt.Sprintf("redact config %s: key %q: %s", e.File, e.Key, e.Msg)
}

func rejected(err error) error {
	return &jev.Error{Code: jev.CodeRejected, Reason: "invalid redaction config", Err: err}
}

func fail(file, key, format string, args ...any) error {
	return rejected(&Error{File: file, Key: key, Msg: fmt.Sprintf(format, args...)})
}

// The typed form of a config file, decoded only after schema validation.
type fileSpec struct {
	Version     int         `json:"version"`
	Rules       []ruleSpec  `json:"rules"`
	Literals    []string    `json:"literals"`
	EnvValues   []string    `json:"env_values"`
	Allowlist   []allowSpec `json:"allowlist"`
	Disable     []string    `json:"disable"`
	Tuning      *tuningSpec `json:"tuning"`
	NeverSend   []string    `json:"never_send"`
	Placeholder string      `json:"placeholder"`
	Mode        string      `json:"mode"`
	Tests       []TestCase  `json:"tests"`
	Review      *bool       `json:"review"`
	Confirm     *bool       `json:"confirm"`
	ReviewMax   int         `json:"review_max"`
	ReviewTTL   string      `json:"review_ttl"`
}

// TestCase is an embedded regression case from a `tests:` list: the engine is
// run on Input, and the output must contain every MustContain string and none
// of the MustNotContain strings.
type TestCase struct {
	// File is the config file that defined the case; set by Load.
	File           string   `json:"-"`
	Name           string   `json:"name"`
	Input          string   `json:"input"`
	MustNotContain []string `json:"must_not_contain"`
	MustContain    []string `json:"must_contain"`
}

type ruleSpec struct {
	ID          string `json:"id"`
	Pattern     string `json:"pattern"`
	Replacement string `json:"replacement"`
	Flags       string `json:"flags"`
}

type allowSpec struct {
	Rule    string `json:"rule"`
	Regex   string `json:"regex"`
	Literal string `json:"literal"`
}

type tuningSpec struct {
	EntropyThreshold *float64 `json:"entropy_threshold"`
	MinTokenLength   *int     `json:"min_token_length"`
	PathHandling     string   `json:"path_handling"`
}

// Keys any layer may use, and the subset an UNTRUSTED project file may use.
// Everything else can disable or loosen redaction, so a project file naming it
// is an error.
var (
	userKeys    = []string{"version", "rules", "literals", "env_values", "allowlist", "disable", "tuning", "never_send", "placeholder", "mode", "tests", "review", "confirm", "review_max", "review_ttl"}
	projectKeys = []string{"version", "rules", "literals", "env_values", "never_send", "mode", "tests"}
)

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

var (
	schemaOnce sync.Once
	schema     *jsonschema.Schema
	schemaErr  error
)

func compiledSchema() (*jsonschema.Schema, error) {
	schemaOnce.Do(func() {
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
		if err != nil {
			schemaErr = err
			return
		}
		c := jsonschema.NewCompiler()
		if err := c.AddResource(schemaID, doc); err != nil {
			schemaErr = err
			return
		}
		schema, schemaErr = c.Compile(schemaID)
	})
	return schema, schemaErr
}

// SchemaJSON returns the embedded JSON Schema for version 1.
func SchemaJSON() []byte { return append([]byte(nil), schemaJSON...) }

// keyPath renders a JSON-pointer location as rules[1].pattern.
func keyPath(loc []string) string {
	var b strings.Builder
	for _, p := range loc {
		if n := strings.Trim(p, "0123456789"); n == "" && p != "" {
			b.WriteString("[" + p + "]")
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		b.WriteString(p)
	}
	return b.String()
}

// checkMode refuses a file that anyone but its owner can read or write.
func checkMode(file string, fi os.FileInfo) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	if perm := fi.Mode().Perm(); perm&^0o600 != 0 {
		return fail(file, "", "mode %04o is too permissive (must be 0600, not group/world accessible); run chmod 600 %s. Refusing to use it", perm, file)
	}
	return nil
}

// readRegular reads a bounded regular file. missing reports a nonexistent
// file, which is not an error.
func readRegular(file string, requireOwnerOnly bool) (data []byte, missing bool, err error) {
	f, err := os.Open(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, fail(file, "", "%v", err)
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, false, fail(file, "", "%v", err)
	}
	if !fi.Mode().IsRegular() {
		return nil, false, fail(file, "", "not a regular file")
	}
	if requireOwnerOnly {
		if err := checkMode(file, fi); err != nil {
			return nil, false, err
		}
	}
	data, err = io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, false, fail(file, "", "%v", err)
	}
	if len(data) > maxFileBytes {
		return nil, false, fail(file, "", "larger than %d bytes", maxFileBytes)
	}
	return data, false, nil
}

// parse validates data against the schema and the layer's permitted keys and
// returns the typed file.
func parse(file string, data []byte, trusted bool) (*fileSpec, error) {
	var generic any
	if err := yaml.Unmarshal(data, &generic); err != nil {
		return nil, fail(file, "", "invalid YAML: %v", err)
	}
	top, ok := generic.(map[string]any)
	if !ok {
		return nil, fail(file, "", "must be a YAML mapping with `version: %d`", Version)
	}
	allowed := userKeys
	if !trusted {
		allowed = projectKeys
	}
	keys := make([]string, 0, len(top))
	for k := range top {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		switch {
		case contains(allowed, k):
		case contains(userKeys, k):
			return nil, fail(file, k, "not permitted in a project config: project files are untrusted and may only add rules, literals, env_values and never_send entries, never disable or loosen redaction")
		default:
			return nil, fail(file, k, "unknown key")
		}
	}

	raw, err := json.Marshal(generic)
	if err != nil {
		return nil, fail(file, "", "not representable as JSON: %v", err)
	}
	sch, err := compiledSchema()
	if err != nil {
		return nil, fail(file, "", "embedded schema: %v", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fail(file, "", "%v", err)
	}
	if err := sch.Validate(inst); err != nil {
		var ve *jsonschema.ValidationError
		if errors.As(err, &ve) {
			leaf := ve
			for len(leaf.Causes) > 0 {
				leaf = leaf.Causes[0]
			}
			return nil, fail(file, keyPath(leaf.InstanceLocation), "schema: %s", leaf.ErrorKind.LocalizedString(message.NewPrinter(language.English)))
		}
		return nil, fail(file, "", "schema: %v", err)
	}

	var spec fileSpec
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&spec); err != nil {
		return nil, fail(file, "", "%v", err)
	}
	if !trusted && spec.Mode == "standard" {
		return nil, fail(file, "mode", "a project config may only set `mode: strict`; it cannot loosen redaction to standard")
	}
	return &spec, nil
}

var globNameRe = regexp.MustCompile(`^[A-Za-z0-9_*?]+$`)

// validate performs the checks the schema cannot express.
func validate(file string, s *fileSpec) error {
	classes := map[string]redact.Class{}
	for _, r := range redact.Rules() {
		classes[r.ID] = r.Class
	}
	seen := map[string]bool{}
	for i, r := range s.Rules {
		key := fmt.Sprintf("rules[%d]", i)
		if seen[r.ID] {
			return fail(file, key+".id", "duplicate rule id %q", r.ID)
		}
		seen[r.ID] = true
		if _, builtin := classes[r.ID]; builtin {
			return fail(file, key+".id", "rule id %q collides with a built-in rule", r.ID)
		}
		if err := redact.ValidateCustomRule(redact.CustomRule{ID: r.ID, Pattern: r.Pattern, Flags: r.Flags, Replacement: r.Replacement}); err != nil {
			field := "pattern"
			var ce *redact.CustomRuleError
			if errors.As(err, &ce) {
				field, err = ce.Field, errors.New(ce.Msg)
			}
			return fail(file, key+"."+field, "%v", err)
		}
	}
	for i, l := range s.Literals {
		if len(strings.TrimSpace(l)) < minLiteralLen {
			return fail(file, fmt.Sprintf("literals[%d]", i), "literal must be at least %d characters after trimming whitespace", minLiteralLen)
		}
	}
	for i, n := range s.EnvValues {
		if !globNameRe.MatchString(n) {
			return fail(file, fmt.Sprintf("env_values[%d]", i), "not an environment variable name or glob")
		}
	}
	for i, id := range s.Disable {
		key := fmt.Sprintf("disable[%d]", i)
		switch c, ok := classes[id]; {
		case !ok:
			return fail(file, key, "unknown rule %q", id)
		case c == redact.Hard:
			return fail(file, key, "rule %s is HARD and cannot be disabled", id)
		}
	}
	for i, a := range s.Allowlist {
		key := fmt.Sprintf("allowlist[%d]", i)
		if a.Rule != "" {
			switch c, ok := classes[a.Rule]; {
			case !ok:
				return fail(file, key+".rule", "unknown rule %q", a.Rule)
			case c == redact.Hard:
				return fail(file, key+".rule", "rule %s is HARD and cannot be allowlisted", a.Rule)
			case a.Rule == redact.RuleHomePath:
				return fail(file, key+".rule", "rule %s takes no allowlist; use `disable` or tuning.path_handling", a.Rule)
			}
		}
		if a.Regex != "" {
			if _, err := regexp.Compile(a.Regex); err != nil {
				return fail(file, key+".regex", "invalid regexp: %v", err)
			}
		}
	}
	for i, p := range s.NeverSend {
		if _, err := compileNever(p); err != nil {
			return fail(file, fmt.Sprintf("never_send[%d]", i), "%v", err)
		}
	}
	if s.ReviewTTL != "" {
		d, err := time.ParseDuration(s.ReviewTTL)
		if err != nil || d <= 0 || d > maxReviewTTL {
			return fail(file, "review_ttl", "must be a duration such as 24h, above zero and at most %s", maxReviewTTL)
		}
	}
	return nil
}

// LoadOptions says where the layers live.
type LoadOptions struct {
	// UserPath is <config_dir>/redact.yaml (trusted; must be 0600).
	UserPath string
	// ProjectPath is <workspace>/.jevkit/redact.yaml (untrusted, additive only).
	ProjectPath string
	// SaltPath holds the per-install random salt for stable placeholders,
	// created 0600 on first use.
	SaltPath string
	// Environ is the process environment (os.Environ()).
	Environ []string
}

// Config is the resolved result of layering built-ins, user and project.
type Config struct {
	// Options configures redact.New.
	Options redact.Options
	// NeverSend matches commands and paths whose output is never sent.
	NeverSend *NeverSend
	// Sources lists the config files that were loaded, lowest precedence first.
	Sources []string
	// RuleSource maps each custom rule id to the file that defines it.
	RuleSource map[string]string
	// Tests are the embedded `tests:` cases from every loaded file.
	Tests []TestCase
	// Review turns on storing the exact redacted payload of recent sends
	// (redact.review or JEVKIT_REDACT_REVIEW). Off by default: stored
	// payloads are themselves sensitive.
	Review bool
	// ReviewMax and ReviewTTL bound the review store.
	ReviewMax int
	ReviewTTL time.Duration
	// Confirm asks for an interactive yes before an interactive send.
	Confirm bool
}

// Review store defaults and limits.
const (
	DefaultReviewMax = 20
	DefaultReviewTTL = 24 * time.Hour
	maxReviewTTL     = 30 * 24 * time.Hour
)

// ValidateFile checks one config file's bytes without loading any layer.
// trusted selects the user-file (true) or project-file (false) key rules.
func ValidateFile(file string, data []byte, trusted bool) error {
	spec, err := parse(file, data, trusted)
	if err != nil {
		return err
	}
	return validate(file, spec)
}

// Redactor builds the redaction engine for the resolved config.
func (c *Config) Redactor() (*redact.Redactor, error) {
	r, err := redact.New(c.Options)
	if err != nil {
		return nil, rejected(err)
	}
	return r, nil
}

// allowableSoft are the SOFT rules an allowlist entry without `rule:` covers.
var allowableSoft = []string{redact.RuleUserPath, redact.RuleEmail, redact.RuleIPv4, redact.RuleEnvDump, redact.RuleHighEntropy}

// Load resolves the layers, lowest to highest precedence: embedded built-ins,
// the USER file, the PROJECT file. A missing file is skipped. Any problem
// returns a *jev.Error with jev.CodeRejected wrapping a *Error that names the
// file and key; the caller must then send nothing.
func Load(o LoadOptions) (*Config, error) {
	cfg := &Config{Options: redact.OptionsFromEnv(o.Environ), RuleSource: map[string]string{}}
	never := append([]string(nil), builtinNeverSend...)

	type layer struct {
		file    string
		trusted bool
		spec    *fileSpec
	}
	var layers []layer
	for _, l := range []struct {
		file    string
		trusted bool
	}{{o.UserPath, true}, {o.ProjectPath, false}} {
		if l.file == "" {
			continue
		}
		data, missing, err := readRegular(l.file, l.trusted)
		if err != nil {
			return nil, err
		}
		if missing {
			continue
		}
		spec, err := parse(l.file, data, l.trusted)
		if err != nil {
			return nil, err
		}
		if err := validate(l.file, spec); err != nil {
			return nil, err
		}
		layers = append(layers, layer{l.file, l.trusted, spec})
		cfg.Sources = append(cfg.Sources, l.file)
	}

	ruleOwner := map[string]string{}
	var globs []string
	disable := map[string]bool{}
	var strictFile string
	for _, l := range layers {
		s := l.spec
		if s.Mode == "strict" && strictFile == "" {
			strictFile = l.file
		}
		for i, r := range s.Rules {
			if prev, dup := ruleOwner[r.ID]; dup {
				return nil, fail(l.file, fmt.Sprintf("rules[%d].id", i), "rule id %q is already defined in %s; layers cannot override a rule", r.ID, prev)
			}
			ruleOwner[r.ID] = l.file
			cfg.RuleSource[r.ID] = l.file
			cfg.Options.Custom = append(cfg.Options.Custom, redact.CustomRule{ID: r.ID, Pattern: r.Pattern, Flags: r.Flags, Replacement: r.Replacement})
		}
		cfg.Options.Secrets = append(cfg.Options.Secrets, s.Literals...)
		for _, t := range s.Tests {
			t.File = l.file
			cfg.Tests = append(cfg.Tests, t)
		}
		globs = append(globs, s.EnvValues...)
		never = append(never, s.NeverSend...)

		// Everything below is only reachable from the trusted USER file: parse
		// rejected these keys in a project file.
		for _, id := range s.Disable {
			disable[id] = true
		}
		for _, a := range s.Allowlist {
			pat := a.Regex
			if a.Literal != "" {
				pat = regexp.QuoteMeta(a.Literal)
			}
			if cfg.Options.Allow == nil {
				cfg.Options.Allow = map[string][]string{}
			}
			ids := allowableSoft
			if a.Rule != "" {
				ids = []string{a.Rule}
			}
			for _, id := range ids {
				cfg.Options.Allow[id] = append(cfg.Options.Allow[id], pat)
			}
		}
		if t := s.Tuning; t != nil {
			if t.EntropyThreshold != nil {
				cfg.Options.EntropyThreshold = *t.EntropyThreshold
			}
			if t.MinTokenLength != nil {
				cfg.Options.MinTokenLen = *t.MinTokenLength
			}
			if t.PathHandling == "keep" {
				disable[redact.RuleHomePath] = true
				disable[redact.RuleUserPath] = true
			}
		}
		if s.Review != nil {
			cfg.Review = *s.Review
		}
		if s.Confirm != nil {
			cfg.Confirm = *s.Confirm
		}
		if s.ReviewMax > 0 {
			cfg.ReviewMax = s.ReviewMax
		}
		if s.ReviewTTL != "" {
			cfg.ReviewTTL, _ = time.ParseDuration(s.ReviewTTL) // checked by validate
		}
		if s.Placeholder != "" {
			switch s.Placeholder {
			case "label":
				cfg.Options.Placeholder = redact.LabelPlaceholder
			case "stable":
				salt, err := LoadSalt(o.SaltPath)
				if err != nil {
					return nil, err
				}
				cfg.Options.Placeholder = redact.StablePlaceholder(salt)
			}
		}
	}
	if cfg.ReviewMax == 0 {
		cfg.ReviewMax = DefaultReviewMax
	}
	if cfg.ReviewTTL == 0 {
		cfg.ReviewTTL = DefaultReviewTTL
	}
	// The environment overrides the file for both switches.
	if v, ok := envBool(o.Environ, "JEVKIT_REDACT_REVIEW"); ok {
		cfg.Review = v
	}
	if v, ok := envBool(o.Environ, "JEVKIT_REDACT_CONFIRM"); ok {
		cfg.Confirm = v
	}
	if strictFile != "" {
		cfg.Options.Strict = true
		if len(disable) > 0 || len(cfg.Options.Allow) > 0 {
			return nil, fail(strictFile, "mode", "strict mode redacts all SOFT rules and conflicts with disable, allowlist and tuning.path_handling: keep")
		}
	}
	for id := range disable {
		cfg.Options.DisableSoft = append(cfg.Options.DisableSoft, id)
	}
	sort.Strings(cfg.Options.DisableSoft)

	// env_values: the current values of matching variables are always redacted.
	if len(globs) > 0 {
		for _, kv := range o.Environ {
			name, val, ok := strings.Cut(kv, "=")
			if !ok || len(strings.TrimSpace(val)) < minEnvValueLen {
				continue
			}
			for _, g := range globs {
				if m, _ := path.Match(g, name); m {
					cfg.Options.Secrets = append(cfg.Options.Secrets, val)
					break
				}
			}
		}
	}

	ns, err := NewNeverSend(never)
	if err != nil {
		return nil, rejected(err)
	}
	cfg.NeverSend = ns

	// Prove the result builds before anyone relies on it.
	if _, err := cfg.Redactor(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// envBool reads a 1/true or 0/false switch from environ; ok is false when it
// is unset or not a recognised value.
func envBool(environ []string, name string) (v, ok bool) {
	for i := len(environ) - 1; i >= 0; i-- {
		k, val, found := strings.Cut(environ[i], "=")
		if !found || k != name {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(val)) {
		case "1", "true", "yes", "on":
			return true, true
		case "0", "false", "no", "off":
			return false, true
		}
		return false, false
	}
	return false, false
}
