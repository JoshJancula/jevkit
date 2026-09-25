// Package seed resolves `sdlc start --file` arguments against a workflow's
// declared seedable artifacts: a pre-existing document takes the place of
// whatever node would otherwise have produced it, which is why only an
// artifact the workflow author explicitly marked seedable: true is ever a
// valid target — an artifact a node merely happens to produce is not
// automatically safe to substitute (that node's own execution may do more
// than write the file).
package seed

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/OWNER/jevkit/internal/sdlc/spec"
)

// FileArg is one parsed `--file` value.
type FileArg struct {
	// Path is the local source file to seed.
	Path string
	// Artifact is the explicit "=artifact" target; empty for a bare --file,
	// which resolves only when the workflow declares exactly one seedable
	// artifact.
	Artifact string
}

// ParseFileArg parses "path" or "path=artifact". The split is on the last
// "=", so a path containing "=" before an explicit artifact name still
// parses correctly.
func ParseFileArg(raw string) (FileArg, error) {
	if raw == "" {
		return FileArg{}, fmt.Errorf("seed: --file value must not be empty")
	}
	if i := strings.LastIndex(raw, "="); i >= 0 {
		path, artifact := raw[:i], raw[i+1:]
		if path == "" || artifact == "" {
			return FileArg{}, fmt.Errorf("seed: --file %q: both the path and the artifact name must be non-empty", raw)
		}
		return FileArg{Path: path, Artifact: artifact}, nil
	}
	return FileArg{Path: raw}, nil
}

// ResolveTargets validates args against w and returns artifact path to
// source file path. Target resolution is deterministic and never uses
// Jev — a bare --file resolves only when w declares exactly one seedable
// artifact; more than one (or zero) requires the explicit path=artifact
// form. An artifact name that is declared but not seedable, or not declared
// at all, is an error, as is targeting the same artifact twice.
func ResolveTargets(w *spec.Workflow, args []FileArg) (map[string]string, error) {
	seedable := w.SeedableArtifacts()
	seedableSet := make(map[string]bool, len(seedable))
	for _, a := range seedable {
		seedableSet[a] = true
	}
	declared := map[string]bool{}
	for _, n := range w.Nodes {
		for _, a := range n.Produces {
			declared[a.Path] = true
		}
	}

	out := make(map[string]string, len(args))
	for _, fa := range args {
		artifact := fa.Artifact
		if artifact == "" {
			switch len(seedable) {
			case 0:
				return nil, fmt.Errorf("seed: --file %s: workflow %q declares no seedable artifacts; name one explicitly with --file %s=<artifact>", fa.Path, w.Name, fa.Path)
			case 1:
				artifact = seedable[0]
			default:
				return nil, fmt.Errorf("seed: --file %s is ambiguous: workflow %q declares %d seedable artifacts (%s); use --file %s=<artifact>",
					fa.Path, w.Name, len(seedable), strings.Join(seedable, ", "), fa.Path)
			}
		} else if !seedableSet[artifact] {
			if declared[artifact] {
				return nil, fmt.Errorf("seed: artifact %q is declared by workflow %q but not seedable", artifact, w.Name)
			}
			return nil, fmt.Errorf("seed: artifact %q is not declared by workflow %q", artifact, w.Name)
		}
		if _, dup := out[artifact]; dup {
			return nil, fmt.Errorf("seed: artifact %q is targeted by more than one --file", artifact)
		}
		out[artifact] = fa.Path
	}
	return out, nil
}

// ValidateSchema validates content against the JSON Schema at schemaPath. An
// artifact's schema is a path (relative to the workflow file's own
// directory, or absolute) to a JSON Schema document; content failing to
// parse as JSON is itself a validation failure, not a distinct error, since
// either way the seeded file does not satisfy what a schema-bearing artifact
// promises downstream nodes.
func ValidateSchema(schemaPath string, content []byte) error {
	schemaRaw, err := os.ReadFile(schemaPath)
	if err != nil {
		return fmt.Errorf("seed: read schema %s: %w", schemaPath, err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaRaw))
	if err != nil {
		return fmt.Errorf("seed: schema %s: %w", schemaPath, err)
	}
	c := jsonschema.NewCompiler()
	const resourceID = "seed:schema"
	if err := c.AddResource(resourceID, doc); err != nil {
		return fmt.Errorf("seed: schema %s: %w", schemaPath, err)
	}
	compiled, err := c.Compile(resourceID)
	if err != nil {
		return fmt.Errorf("seed: schema %s: %w", schemaPath, err)
	}
	var inst any
	if err := json.Unmarshal(content, &inst); err != nil {
		return fmt.Errorf("seed: content does not parse as JSON, required by schema %s: %w", schemaPath, err)
	}
	if err := compiled.Validate(inst); err != nil {
		return fmt.Errorf("seed: content does not satisfy schema %s: %w", schemaPath, err)
	}
	return nil
}
