package plugins

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	generatedMetaName = ".jevkit-plugin-generated.json"
	templatesRel      = "plugins/templates"
	outputRel         = "plugins/jevkit"
	schemasRel        = "plugins/schemas"
)

var tokenRe = regexp.MustCompile(`\{\{([A-Za-z0-9_]+)\}\}`)

// Options controls Generate.
type Options struct {
	// RepoRoot is the jevkit repository root (contains plugins/templates).
	RepoRoot string
	// OutRoot overrides the default plugins/jevkit output directory.
	OutRoot string
	// Version overrides the Version constant when non-empty.
	Version string
}

// Result describes one generated host package.
type Result struct {
	Host  string
	Dir   string
	Files []string
}

func (o Options) version() string {
	if o.Version != "" {
		return o.Version
	}
	return Version
}

func (o Options) outRoot() string {
	if o.OutRoot != "" {
		return o.OutRoot
	}
	return filepath.Join(o.RepoRoot, outputRel)
}

func tokens(host, version string) map[string]string {
	return map[string]string{
		"PLUGIN_ID":       PluginID,
		"PLUGIN_VERSION":  version,
		"RUNTIME":         host,
		"INSTALL_COMMAND": installCommandFor(version),
		"MODULE_PATH":     ModulePath,
		"GO_INSTALL_REF":  goInstallRef(version),
		"COMMAND_NAME":    CommandName,
	}
}

func render(text string, tok map[string]string) (string, error) {
	var missing []string
	out := tokenRe.ReplaceAllStringFunc(text, func(m string) string {
		key := tokenRe.FindStringSubmatch(m)[1]
		v, ok := tok[key]
		if !ok {
			missing = append(missing, key)
			return m
		}
		return v
	})
	if len(missing) > 0 {
		sort.Strings(missing)
		return "", fmt.Errorf("unknown template tokens: %s", strings.Join(missing, ", "))
	}
	return out, nil
}

func normalizeText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text
}

func writeFile(path, body string, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), mode)
}

func copyRendered(src, dst string, tok map[string]string, mode fs.FileMode) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	rendered, err := render(string(raw), tok)
	if err != nil {
		return fmt.Errorf("%s: %w", src, err)
	}
	body := normalizeText(rendered)
	if strings.HasSuffix(dst, ".json") {
		var v any
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			return fmt.Errorf("%s: rendered JSON invalid: %w", src, err)
		}
		pretty, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err
		}
		body = string(pretty) + "\n"
	}
	return writeFile(dst, body, mode)
}

func copyBinaryish(src, dst string, mode fs.FileMode) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	body := normalizeText(string(raw))
	return writeFile(dst, body, mode)
}

type fileSpec struct {
	SrcRel string
	DstRel string
	Mode   fs.FileMode
	Render bool
}

func hostSpecs(host string) ([]fileSpec, error) {
	shared := []fileSpec{
		{SrcRel: "shared/jevkit-plugin-bootstrap.sh", DstRel: "shared/jevkit-plugin-bootstrap.sh", Mode: 0o755, Render: true},
		{SrcRel: "shared/jevkit-run.sh", DstRel: "shared/jevkit-run.sh", Mode: 0o755, Render: true},
	}
	switch host {
	case "claude":
		return append(shared,
			fileSpec{"claude/plugin.json", ".claude-plugin/plugin.json", 0o644, true},
			fileSpec{"claude/marketplace.json", ".claude-plugin/marketplace.json", 0o644, true},
			fileSpec{"claude/hooks.json", "hooks/hooks.json", 0o644, true},
			fileSpec{"claude/mcp.json", ".mcp.json", 0o644, true},
			fileSpec{"claude/host-manifest.json", "host-manifest.json", 0o644, true},
		), nil
	case "cursor":
		return append(shared,
			fileSpec{"cursor/plugin.json", ".cursor-plugin/plugin.json", 0o644, true},
			fileSpec{"cursor/hooks.json", "hooks.json", 0o644, true},
			fileSpec{"cursor/mcp.json", "mcp.json", 0o644, true},
			fileSpec{"cursor/host-manifest.json", "host-manifest.json", 0o644, true},
		), nil
	case "antigravity":
		return append(shared,
			fileSpec{"antigravity/hooks.json", "hooks.json", 0o644, true},
			fileSpec{"antigravity/mcp_config.json", "mcp_config.json", 0o644, true},
			fileSpec{"antigravity/host-manifest.json", "host-manifest.json", 0o644, true},
		), nil
	case "opencode":
		return append(shared,
			fileSpec{"opencode/host-manifest.json", "host-manifest.json", 0o644, true},
			fileSpec{"opencode/jevkit-runtime-hooks.ts", "plugins/jevkit-runtime-hooks.ts", 0o644, false},
		), nil
	default:
		return nil, fmt.Errorf("unknown host %q", host)
	}
}

// Generate writes host packages under OutRoot (default plugins/jevkit).
func Generate(opts Options) ([]Result, error) {
	if opts.RepoRoot == "" {
		return nil, fmt.Errorf("plugins: RepoRoot is required")
	}
	templatesRoot := filepath.Join(opts.RepoRoot, templatesRel)
	if st, err := os.Stat(templatesRoot); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("plugins: templates missing at %s", templatesRoot)
	}
	outRoot := opts.outRoot()
	version := opts.version()
	if err := os.MkdirAll(outRoot, 0o755); err != nil {
		return nil, err
	}
	if err := writeFile(filepath.Join(outRoot, "VERSION"), version+"\n", 0o644); err != nil {
		return nil, err
	}

	var results []Result
	for _, host := range Hosts {
		res, err := generateHost(opts.RepoRoot, templatesRoot, outRoot, host, version)
		if err != nil {
			return nil, err
		}
		results = append(results, res)
	}
	return results, nil
}

func generateHost(repoRoot, templatesRoot, outRoot, host, version string) (Result, error) {
	specs, err := hostSpecs(host)
	if err != nil {
		return Result{}, err
	}
	tok := tokens(host, version)
	hostDir := filepath.Join(outRoot, host)
	if err := os.RemoveAll(hostDir); err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(hostDir, 0o755); err != nil {
		return Result{}, err
	}

	var files []string
	for _, spec := range specs {
		src := filepath.Join(templatesRoot, spec.SrcRel)
		dst := filepath.Join(hostDir, filepath.FromSlash(spec.DstRel))
		if spec.Render {
			if err := copyRendered(src, dst, tok, spec.Mode); err != nil {
				return Result{}, err
			}
		} else {
			if err := copyBinaryish(src, dst, spec.Mode); err != nil {
				return Result{}, err
			}
		}
		files = append(files, spec.DstRel)
	}

	meta := map[string]any{
		"schemaVersion":    1,
		"pluginVersion":    version,
		"sourceDescriptor": filepath.ToSlash(filepath.Join(templatesRel, host)),
		"generatedPaths":   files,
	}
	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return Result{}, err
	}
	metaRel := generatedMetaName
	if err := writeFile(filepath.Join(hostDir, metaRel), string(metaBytes)+"\n", 0o644); err != nil {
		return Result{}, err
	}
	files = append(files, metaRel)
	sort.Strings(files)

	_ = repoRoot
	return Result{Host: host, Dir: hostDir, Files: files}, nil
}

// FindRepoRoot walks up from start looking for go.mod with the jevkit module.
func FindRepoRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		gomod := filepath.Join(dir, "go.mod")
		if b, err := os.ReadFile(gomod); err == nil {
			if bytes.Contains(b, []byte("module github.com/OWNER/jevkit")) {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("plugins: repo root not found from %s", start)
		}
		dir = parent
	}
}
