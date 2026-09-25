package compile

import (
	"testing"

	"github.com/OWNER/jevkit/internal/sdlc/spec"
)

// TestBuiltinsCompile compiles every embedded built-in workflow: each must
// be a valid spec (spec.Builtin already validates on load) and a compilable,
// sha256-pinned DAG. A built-in that only "looks" valid YAML but fails to
// compile would be a real, user-facing break, so this stays in compile's own
// test suite rather than only spec's.
func TestBuiltinsCompile(t *testing.T) {
	for _, name := range spec.BuiltinNames() {
		t.Run(name, func(t *testing.T) {
			w, ok, err := spec.Builtin(name)
			if !ok {
				t.Fatalf("spec.Builtin(%q): not found", name)
			}
			if err != nil {
				t.Fatalf("spec.Builtin(%q): %v", name, err)
			}
			g, err := Compile(w)
			if err != nil {
				t.Fatalf("Compile(%q): %v", name, err)
			}
			if g.SHA256 == "" {
				t.Errorf("%s: expected a sha256 pin", name)
			}
			if w.Description == "" {
				t.Errorf("%s: description must be non-empty (it's the workflow-selection rubric)", name)
			}
		})
	}
}
