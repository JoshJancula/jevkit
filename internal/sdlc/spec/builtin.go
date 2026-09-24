package spec

import (
	"embed"
	"fmt"
	"sort"
)

//go:embed builtin/*.yaml
var builtinFS embed.FS

// builtinNames maps a built-in workflow's name to its embedded file. A
// built-in *is* a workflow, usable as-is by name (`jevkit sdlc start
// feature --task ...`) with no clone-and-edit step required; `sdlc init`
// exists only for a project that wants to customize one.
var builtinNames = map[string]string{
	"feature": "builtin/feature.yaml",
	"bugfix":  "builtin/bugfix.yaml",
	"review":  "builtin/review.yaml",
	"release": "builtin/release.yaml",
}

// BuiltinNames returns every embedded built-in workflow's name, sorted.
func BuiltinNames() []string {
	names := make([]string, 0, len(builtinNames))
	for n := range builtinNames {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// BuiltinSource returns the raw YAML for the built-in workflow named name,
// for `sdlc init` to write out as an editable starting point.
func BuiltinSource(name string) ([]byte, bool) {
	path, ok := builtinNames[name]
	if !ok {
		return nil, false
	}
	raw, err := builtinFS.ReadFile(path)
	if err != nil {
		// Unreachable outside a broken build: the path came from the same
		// map used to embed it.
		return nil, false
	}
	return raw, true
}

// Builtin loads and validates the built-in workflow named name. ok is false
// when name is not a built-in; err is non-nil only if an embedded workflow
// somehow fails to validate, which would be a bug in jevkit itself, not a
// user-fixable error.
func Builtin(name string) (w *Workflow, ok bool, err error) {
	raw, ok := BuiltinSource(name)
	if !ok {
		return nil, false, nil
	}
	w, err = Load(raw)
	if err != nil {
		return nil, true, fmt.Errorf("sdlc: internal error: embedded built-in workflow %q: %w", name, err)
	}
	return w, true, nil
}
