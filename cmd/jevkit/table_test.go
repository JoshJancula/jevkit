package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriteTableUsesBordersAndHandlesUnicode(t *testing.T) {
	var got bytes.Buffer
	writeTable(&got, []string{"NAME", "VALUE"}, [][]string{{"status", "ready"}, {"match", "—"}})
	for _, want := range []string{
		"┌────────┬───────┐",
		"│ NAME   │ VALUE │",
		"├────────┼───────┤",
		"│ match  │ —     │",
		"└────────┴───────┘",
	} {
		if !strings.Contains(got.String(), want) {
			t.Errorf("table missing %q:\n%s", want, got.String())
		}
	}
}
