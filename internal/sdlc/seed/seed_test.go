package seed

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OWNER/jevkit/internal/sdlc/spec"
)

func mustLoad(t *testing.T, y string) *spec.Workflow {
	t.Helper()
	w, err := spec.Load([]byte(y))
	if err != nil {
		t.Fatalf("spec.Load: %v", err)
	}
	return w
}

func TestParseFileArg(t *testing.T) {
	cases := []struct {
		raw, path, artifact string
	}{
		{"plan.md", "plan.md", ""},
		{"plan.md=spec.md", "plan.md", "spec.md"},
		{"./dir=with=equals/plan.md=spec.md", "./dir=with=equals/plan.md", "spec.md"},
	}
	for _, tc := range cases {
		got, err := ParseFileArg(tc.raw)
		if err != nil {
			t.Fatalf("ParseFileArg(%q): %v", tc.raw, err)
		}
		if got.Path != tc.path || got.Artifact != tc.artifact {
			t.Errorf("ParseFileArg(%q) = %+v, want {%q %q}", tc.raw, got, tc.path, tc.artifact)
		}
	}
}

func TestParseFileArgRejectsEmpty(t *testing.T) {
	for _, raw := range []string{"", "=spec.md", "plan.md="} {
		if _, err := ParseFileArg(raw); err == nil {
			t.Errorf("ParseFileArg(%q) should have failed", raw)
		}
	}
}

const oneSeedableYAML = `
version: 1
name: one-seedable
description: d
nodes:
  - id: write-spec
    kind: work
    agent: self
    produces: [{path: spec.md, required: true, seedable: true}]
    next: done
  - id: done
    kind: terminal
    outcome: succeeded
`

const twoSeedableYAML = `
version: 1
name: two-seedable
description: d
nodes:
  - id: a
    kind: work
    agent: self
    produces: [{path: prd.md, required: true, seedable: true}]
    next: b
  - id: b
    kind: work
    agent: self
    produces: [{path: design.md, required: true, seedable: true}, {path: notes.md}]
    next: done
  - id: done
    kind: terminal
    outcome: succeeded
`

func TestResolveTargetsBareResolvesWhenExactlyOneSeedable(t *testing.T) {
	w := mustLoad(t, oneSeedableYAML)
	got, err := ResolveTargets(w, []FileArg{{Path: "./plan.md"}})
	if err != nil {
		t.Fatalf("ResolveTargets: %v", err)
	}
	if got["spec.md"] != "./plan.md" {
		t.Fatalf("got %v", got)
	}
}

func TestResolveTargetsBareAmbiguousWithMultipleSeedable(t *testing.T) {
	w := mustLoad(t, twoSeedableYAML)
	_, err := ResolveTargets(w, []FileArg{{Path: "./x.md"}})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("got %v, want an ambiguous error", err)
	}
}

func TestResolveTargetsBareErrorsWithNoSeedable(t *testing.T) {
	y := `
version: 1
name: none
description: d
nodes:
  - id: a
    kind: work
    agent: self
    objective: "do a"
    next: done
  - id: done
    kind: terminal
    outcome: succeeded
`
	w := mustLoad(t, y)
	_, err := ResolveTargets(w, []FileArg{{Path: "./x.md"}})
	if err == nil || !strings.Contains(err.Error(), "no seedable artifacts") {
		t.Fatalf("got %v", err)
	}
}

func TestResolveTargetsExplicitWorksWithMultiple(t *testing.T) {
	w := mustLoad(t, twoSeedableYAML)
	got, err := ResolveTargets(w, []FileArg{
		{Path: "./prd.md", Artifact: "prd.md"},
		{Path: "./design.md", Artifact: "design.md"},
	})
	if err != nil {
		t.Fatalf("ResolveTargets: %v", err)
	}
	if got["prd.md"] != "./prd.md" || got["design.md"] != "./design.md" {
		t.Fatalf("got %v", got)
	}
}

func TestResolveTargetsRejectsUndeclaredArtifact(t *testing.T) {
	w := mustLoad(t, oneSeedableYAML)
	_, err := ResolveTargets(w, []FileArg{{Path: "./x.md", Artifact: "nonexistent.md"}})
	if err == nil || !strings.Contains(err.Error(), "is not declared") {
		t.Fatalf("got %v", err)
	}
}

func TestResolveTargetsRejectsDeclaredButNotSeedable(t *testing.T) {
	w := mustLoad(t, twoSeedableYAML)
	_, err := ResolveTargets(w, []FileArg{{Path: "./x.md", Artifact: "notes.md"}})
	if err == nil || !strings.Contains(err.Error(), "not seedable") {
		t.Fatalf("got %v", err)
	}
}

func TestResolveTargetsRejectsDoubleTarget(t *testing.T) {
	w := mustLoad(t, twoSeedableYAML)
	_, err := ResolveTargets(w, []FileArg{
		{Path: "./a.md", Artifact: "prd.md"},
		{Path: "./b.md", Artifact: "prd.md"},
	})
	if err == nil || !strings.Contains(err.Error(), "more than one") {
		t.Fatalf("got %v", err)
	}
}

func TestValidateSchemaAcceptsMatchingJSON(t *testing.T) {
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "spec.schema.json")
	if err := os.WriteFile(schemaPath, []byte(`{"type":"object","required":["scope"],"properties":{"scope":{"type":"string"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSchema(schemaPath, []byte(`{"scope":"add rate limiting"}`)); err != nil {
		t.Fatalf("ValidateSchema: %v", err)
	}
}

func TestValidateSchemaRejectsNonConformingJSON(t *testing.T) {
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "spec.schema.json")
	if err := os.WriteFile(schemaPath, []byte(`{"type":"object","required":["scope"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSchema(schemaPath, []byte(`{"notScope":"x"}`)); err == nil {
		t.Fatal("expected a schema validation error")
	}
}

func TestValidateSchemaRejectsNonJSONContent(t *testing.T) {
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "spec.schema.json")
	if err := os.WriteFile(schemaPath, []byte(`{"type":"object"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSchema(schemaPath, []byte("# just markdown, not json\n")); err == nil {
		t.Fatal("expected an error for non-JSON content")
	}
}

func TestValidateSchemaMissingSchemaFileIsAnError(t *testing.T) {
	if err := ValidateSchema(filepath.Join(t.TempDir(), "nope.json"), []byte("{}")); err == nil {
		t.Fatal("expected an error for a missing schema file")
	}
}
