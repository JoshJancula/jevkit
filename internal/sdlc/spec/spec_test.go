package spec

import (
	"strings"
	"testing"
)

const minimalYAML = `
version: 1
name: ship-feature
description: |
  WHEN: a new user-facing capability needs spec, implementation, tests and review.
nodes:
  - id: write-spec
    kind: work
    agent: self
    next: done
  - id: done
    kind: terminal
    outcome: succeeded
`

func TestLoadValid(t *testing.T) {
	w, err := Load([]byte(minimalYAML))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w.Budgets.MaxNodeAttempts != DefaultMaxNodeAttempts {
		t.Errorf("MaxNodeAttempts default = %d, want %d", w.Budgets.MaxNodeAttempts, DefaultMaxNodeAttempts)
	}
	if w.Budgets.MaxReroutes != DefaultMaxReroutes {
		t.Errorf("MaxReroutes default = %d, want %d", w.Budgets.MaxReroutes, DefaultMaxReroutes)
	}
	if w.Budgets.MissingUsage != "warn" {
		t.Errorf("MissingUsage default = %q, want warn", w.Budgets.MissingUsage)
	}
}

func TestLoadUnknownField(t *testing.T) {
	_, err := Load([]byte(minimalYAML + "\nbogusField: true\n"))
	if err == nil {
		t.Fatal("expected error for unknown top-level field")
	}
}

func TestValidateRequiresDescription(t *testing.T) {
	y := strings.Replace(minimalYAML, "description: |\n  WHEN: a new user-facing capability needs spec, implementation, tests and review.\n", "", 1)
	_, err := Load([]byte(y))
	if err == nil {
		t.Fatal("expected error: missing description")
	}
}

func TestValidateDuplicateID(t *testing.T) {
	y := `
version: 1
name: dup
description: test
nodes:
  - id: a
    kind: work
    agent: self
    next: a
  - id: a
    kind: terminal
    outcome: succeeded
`
	_, err := Load([]byte(y))
	if err == nil || !strings.Contains(err.Error(), "duplicate node id") {
		t.Fatalf("expected duplicate node id error, got %v", err)
	}
}

func TestValidateUnknownTarget(t *testing.T) {
	y := `
version: 1
name: badtarget
description: test
nodes:
  - id: a
    kind: work
    agent: self
    next: nowhere
`
	_, err := Load([]byte(y))
	if err == nil || !strings.Contains(err.Error(), "not a declared node id") {
		t.Fatalf("expected unknown-target error, got %v", err)
	}
}

func TestValidateSelectDefaultMustBeCandidate(t *testing.T) {
	y := `
version: 1
name: badselect
description: test
nodes:
  - id: pick
    kind: select
    questionSet: sdlc.agent-selection
    candidates: [a, b]
    assignTo: implement
    default: c
  - id: implement
    kind: work
    agent: self
    next: done
  - id: done
    kind: terminal
    outcome: succeeded
`
	_, err := Load([]byte(y))
	if err == nil || !strings.Contains(err.Error(), "not among candidates") {
		t.Fatalf("expected default-not-a-candidate error, got %v", err)
	}
}

func TestValidateDuplicateProducedArtifact(t *testing.T) {
	y := `
version: 1
name: dupart
description: test
nodes:
  - id: a
    kind: work
    agent: self
    next: b
    produces: [{path: spec.md, required: true}]
  - id: b
    kind: work
    agent: self
    next: done
    produces: [{path: spec.md, required: true}]
  - id: done
    kind: terminal
    outcome: succeeded
`
	_, err := Load([]byte(y))
	if err == nil || !strings.Contains(err.Error(), "produced by both") {
		t.Fatalf("expected duplicate-artifact error, got %v", err)
	}
}

func TestSeedableArtifacts(t *testing.T) {
	y := `
version: 1
name: seed
description: test
nodes:
  - id: a
    kind: work
    agent: self
    next: done
    produces:
      - path: spec.md
        seedable: true
  - id: done
    kind: terminal
    outcome: succeeded
`
	w, err := Load([]byte(y))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := w.SeedableArtifacts()
	if len(got) != 1 || got[0] != "spec.md" {
		t.Fatalf("SeedableArtifacts() = %v, want [spec.md]", got)
	}
}

func TestGateRoutesMustBeBinaryWithDefaultMatchingOne(t *testing.T) {
	cases := []struct {
		name, yaml, want string
	}{
		{"three routes", `
version: 1
name: t
description: d
nodes:
  - id: g
    kind: gate
    questionSet: sdlc.needs-delegation
    routes: {a: done, b: done, c: done}
    default: done
  - id: done
    kind: terminal
    outcome: succeeded
`, "exactly two entries"},
		{"default matches neither route", `
version: 1
name: t
description: d
nodes:
  - id: g
    kind: gate
    questionSet: sdlc.needs-delegation
    routes: {a: done, b: aborted}
    default: g
  - id: done
    kind: terminal
    outcome: succeeded
  - id: aborted
    kind: terminal
    outcome: aborted
`, "must equal exactly one"},
		{"default matches both routes", `
version: 1
name: t
description: d
nodes:
  - id: g
    kind: gate
    questionSet: sdlc.needs-delegation
    routes: {a: done, b: done}
    default: done
  - id: done
    kind: terminal
    outcome: succeeded
`, "must equal exactly one"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load([]byte(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestGateRequiresTrueRoute(t *testing.T) {
	cases := []struct{ name, trueRoute string }{
		{"missing entirely", ""},
		{"not one of the two keys", "trueRoute: c"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			y := "\nversion: 1\nname: t\ndescription: d\nnodes:\n  - id: g\n    kind: gate\n    questionSet: sdlc.needs-delegation\n    routes: {a: done, b: aborted}\n    default: done\n    " + tc.trueRoute + "\n  - id: done\n    kind: terminal\n    outcome: succeeded\n  - id: aborted\n    kind: terminal\n    outcome: aborted\n"
			_, err := Load([]byte(y))
			if err == nil || !strings.Contains(err.Error(), "requires trueRoute") {
				t.Fatalf("got %v, want a requires-trueRoute error", err)
			}
		})
	}
}

func TestGateAcceptsNonYesNoLabelsWhenDefaultMatchesOne(t *testing.T) {
	y := `
version: 1
name: t
description: d
nodes:
  - id: g
    kind: gate
    questionSet: sdlc.handoff-readiness
    routes: {ready: done, not-ready: retry}
    trueRoute: ready
    default: done
    next: unused
  - id: retry
    kind: terminal
    outcome: aborted
  - id: done
    kind: terminal
    outcome: succeeded
`
	// "next: unused" on a gate is meaningless but harmless; drop it, the
	// point of this fixture is the ready/not-ready labels.
	y = strings.Replace(y, "    next: unused\n", "", 1)
	if _, err := Load([]byte(y)); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestWorkNodeMustBeAssignedOrSelf(t *testing.T) {
	y := `
version: 1
name: t
description: d
nodes:
  - id: orphan
    kind: work
    objective: "do a thing"
    next: done
  - id: done
    kind: terminal
    outcome: succeeded
`
	_, err := Load([]byte(y))
	if err == nil || !strings.Contains(err.Error(), "can never be assigned an agent") {
		t.Fatalf("got %v, want an unassignable-work-node error", err)
	}
}

func TestKindRequirements(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{"work missing objective", `
version: 1
name: t
description: d
nodes:
  - id: a
    kind: work
    next: done
  - id: done
    kind: terminal
    outcome: succeeded
`, "requires an objective"},
		{"gate missing questionSet", `
version: 1
name: t
description: d
nodes:
  - id: a
    kind: gate
    routes: {yes: done, no: done}
  - id: done
    kind: terminal
    outcome: succeeded
`, "requires questionSet"},
		{"check missing command", `
version: 1
name: t
description: d
nodes:
  - id: a
    kind: check
    routes: {pass: done, fail: done}
  - id: done
    kind: terminal
    outcome: succeeded
`, "requires command"},
		{"terminal missing outcome", `
version: 1
name: t
description: d
nodes:
  - id: done
    kind: terminal
`, "requires outcome"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load([]byte(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want error containing %q", err, tc.want)
			}
		})
	}
}
