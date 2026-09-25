package engine

import "testing"

const seedGraph = `
version: 1
name: seedflow
description: d
nodes:
  - id: write-spec
    kind: work
    agent: self
    produces: [{path: spec.md, required: true, seedable: true}]
    next: gate
  - id: gate
    kind: gate
    questionSet: sdlc.needs-delegation
    routes: {yes: implement, no: aborted}
    trueRoute: yes
    default: implement
  - id: implement
    kind: work
    agent: self
    produces: [{path: patch.diff, required: true}]
    next: done
  - id: done
    kind: terminal
    outcome: succeeded
  - id: aborted
    kind: terminal
    outcome: aborted
`

func TestFirstUnsatisfiedNodeNoSeeding(t *testing.T) {
	g := buildGraph(t, seedGraph)
	id, err := FirstUnsatisfiedNode(g, nil)
	if err != nil {
		t.Fatal(err)
	}
	if id != "write-spec" {
		t.Fatalf("id = %q, want write-spec", id)
	}
}

func TestFirstUnsatisfiedNodeSkipsSatisfiedWork(t *testing.T) {
	g := buildGraph(t, seedGraph)
	id, err := FirstUnsatisfiedNode(g, map[string]bool{"spec.md": true})
	if err != nil {
		t.Fatal(err)
	}
	// write-spec's required artifact is satisfied, so it's skipped; "gate"
	// is a decision node and is never skipped just because artifacts exist.
	if id != "gate" {
		t.Fatalf("id = %q, want gate", id)
	}
}

func TestFirstUnsatisfiedNodeStopsAtUnsatisfiedWork(t *testing.T) {
	g := buildGraph(t, seedGraph)
	// Seeding an unrelated artifact must not skip write-spec.
	id, err := FirstUnsatisfiedNode(g, map[string]bool{"patch.diff": true})
	if err != nil {
		t.Fatal(err)
	}
	if id != "write-spec" {
		t.Fatalf("id = %q, want write-spec", id)
	}
}

func TestFirstUnsatisfiedNodeSkipsThroughJoin(t *testing.T) {
	g := buildGraph(t, joinGraph) // a(work,self) -> j(join) -> done(terminal)
	id, err := FirstUnsatisfiedNode(g, nil)
	if err != nil {
		t.Fatal(err)
	}
	if id != "a" {
		t.Fatalf("id = %q, want a", id)
	}
	// "a" has no declared produces, so producedAllRequired is vacuously true
	// even with nothing seeded — it still can't be skipped since it isn't
	// satisfied by any artifact, it just has none to satisfy. Confirm the
	// walk lands where NewRun itself would.
	st, _, err := NewRun(g)
	if err != nil {
		t.Fatal(err)
	}
	if st.Current != id {
		t.Fatalf("FirstUnsatisfiedNode = %q, but NewRun entered %q", id, st.Current)
	}
}
