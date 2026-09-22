package plugins

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ManifestSchema maps a path relative to a host package root onto a schema
// file under plugins/schemas/.
type ManifestSchema struct {
	RelPath    string
	SchemaFile string
}

// HostManifests lists JSON files that must exist and validate for each host.
func HostManifests(host string) []ManifestSchema {
	common := []ManifestSchema{
		{RelPath: "host-manifest.json", SchemaFile: "host-manifest.schema.json"},
	}
	switch host {
	case "claude":
		return append(common,
			ManifestSchema{".claude-plugin/plugin.json", "claude-plugin.schema.json"},
			ManifestSchema{".claude-plugin/marketplace.json", "claude-marketplace.schema.json"},
			ManifestSchema{"hooks/hooks.json", "claude-hooks.schema.json"},
			ManifestSchema{".mcp.json", "mcp.schema.json"},
		)
	case "cursor":
		return append(common,
			ManifestSchema{".cursor-plugin/plugin.json", "cursor-plugin.schema.json"},
			ManifestSchema{"hooks.json", "cursor-hooks.schema.json"},
			ManifestSchema{"mcp.json", "mcp.schema.json"},
		)
	case "antigravity":
		return append(common,
			ManifestSchema{"hooks.json", "antigravity-hooks.schema.json"},
			ManifestSchema{"mcp_config.json", "mcp.schema.json"},
		)
	case "opencode":
		return common
	default:
		return nil
	}
}

var secretPattern = regexp.MustCompile(`(?i)(api[_-]?key|\bsecret\b|\bpassword\b|\btoken\b|bearer\s+[a-z0-9._\-]{8,}|sk-[a-z0-9]{10,})`)

// ValidatePackage checks JSON schemas, version sync, jevkit references, and
// absence of secrets for one generated host directory.
func ValidatePackage(repoRoot, hostDir, host, wantVersion string) error {
	schemasDir := filepath.Join(repoRoot, schemasRel)
	for _, m := range HostManifests(host) {
		path := filepath.Join(hostDir, filepath.FromSlash(m.RelPath))
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("%s: %w", m.RelPath, err)
		}
		if err := validateJSONSchema(schemasDir, m.SchemaFile, raw); err != nil {
			return fmt.Errorf("%s: %w", m.RelPath, err)
		}
		if err := checkNoSecrets(m.RelPath, raw); err != nil {
			return err
		}
		if !bytes.Contains(raw, []byte("jevkit")) {
			return fmt.Errorf("%s: must reference jevkit", m.RelPath)
		}
		if err := checkVersionField(m.RelPath, raw, wantVersion); err != nil {
			return err
		}
	}

	metaPath := filepath.Join(hostDir, generatedMetaName)
	metaRaw, err := os.ReadFile(metaPath)
	if err != nil {
		return err
	}
	if !bytes.Contains(metaRaw, []byte(`"pluginVersion": "`+wantVersion+`"`)) &&
		!bytes.Contains(metaRaw, []byte(`"pluginVersion":"`+wantVersion+`"`)) {
		// Pretty-printed indent form from encoding/json.
		if !bytes.Contains(metaRaw, []byte(wantVersion)) {
			return fmt.Errorf("%s: pluginVersion must be %q", generatedMetaName, wantVersion)
		}
	}

	bootstrap := filepath.Join(hostDir, "shared", "jevkit-plugin-bootstrap.sh")
	bootRaw, err := os.ReadFile(bootstrap)
	if err != nil {
		return err
	}
	if !bytes.Contains(bootRaw, []byte("command -v")) || !bytes.Contains(bootRaw, []byte("jevkit")) {
		return fmt.Errorf("bootstrap must check PATH for jevkit")
	}
	if !bytes.Contains(bootRaw, []byte(installCommandFor(wantVersion))) &&
		!bytes.Contains(bootRaw, []byte("go install")) {
		return fmt.Errorf("bootstrap must print an install command")
	}
	if err := checkNoSecrets("shared/jevkit-plugin-bootstrap.sh", bootRaw); err != nil {
		return err
	}

	if host == "opencode" {
		ts := filepath.Join(hostDir, "plugins", "jevkit-runtime-hooks.ts")
		raw, err := os.ReadFile(ts)
		if err != nil {
			return err
		}
		if !bytes.Contains(raw, []byte("jevkit")) {
			return fmt.Errorf("opencode plugin must reference jevkit")
		}
		if err := checkNoSecrets("plugins/jevkit-runtime-hooks.ts", raw); err != nil {
			return err
		}
	}
	return nil
}

func validateJSONSchema(schemasDir, schemaFile string, raw []byte) error {
	schemaPath := filepath.Join(schemasDir, schemaFile)
	schemaBytes, err := os.ReadFile(schemaPath)
	if err != nil {
		return err
	}
	schemaDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaBytes))
	if err != nil {
		return fmt.Errorf("schema %s: %w", schemaFile, err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	c := jsonschema.NewCompiler()
	id := "file://" + filepath.ToSlash(schemaPath)
	if err := c.AddResource(id, schemaDoc); err != nil {
		return err
	}
	sch, err := c.Compile(id)
	if err != nil {
		return fmt.Errorf("compile %s: %w", schemaFile, err)
	}
	if err := sch.Validate(inst); err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	return nil
}

func checkNoSecrets(name string, raw []byte) error {
	for _, line := range strings.Split(string(raw), "\n") {
		if secretPattern.MatchString(line) {
			return fmt.Errorf("%s: possible secret material: %s", name, strings.TrimSpace(line))
		}
	}
	return nil
}

func checkVersionField(rel string, raw []byte, want string) error {
	// Only manifests that carry a version field need matching.
	switch {
	case strings.HasSuffix(rel, "plugin.json"),
		strings.HasSuffix(rel, "marketplace.json"),
		rel == "host-manifest.json":
		if !bytes.Contains(raw, []byte(`"`+want+`"`)) {
			return fmt.Errorf("%s: version must be %q", rel, want)
		}
	}
	return nil
}

// ValidateAll runs ValidatePackage for every host under outRoot.
func ValidateAll(repoRoot, outRoot, wantVersion string) error {
	for _, host := range Hosts {
		dir := filepath.Join(outRoot, host)
		if err := ValidatePackage(repoRoot, dir, host, wantVersion); err != nil {
			return fmt.Errorf("%s: %w", host, err)
		}
	}
	verFile := filepath.Join(outRoot, "VERSION")
	b, err := os.ReadFile(verFile)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(b)) != wantVersion {
		return fmt.Errorf("VERSION file %q != %q", strings.TrimSpace(string(b)), wantVersion)
	}
	return nil
}
