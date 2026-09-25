package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShellWrapperDeniesOutsidePathAndYoloBypasses(t *testing.T) {
	a := newApp(t)
	outside := t.TempDir()
	path := filepath.Join(outside, "output.txt")
	command := "printf safe > " + filepath.ToSlash(path)
	a.Stdout, a.Stderr = &bytes.Buffer{}, &bytes.Buffer{}
	if err := a.runShellWrapper(a.WorkDir, "codex", command); err == nil {
		t.Fatal("outside path was allowed")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("denied command executed: %v", err)
	}
	a.Yolo = true
	if err := a.runShellWrapper(a.WorkDir, "codex", command); err != nil {
		t.Fatalf("yolo should lift path guard: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if err := a.runShellWrapper(a.WorkDir, "codex", "rm -rf /"); err == nil {
		t.Fatal("yolo lifted killswitch")
	}
}

func TestCompactShellResultNPMOutcomes(t *testing.T) {
	noisy := strings.Repeat("npm http fetch GET 200 https://registry.example/package\n", 400)

	success, compacted := compactShellResult("npm install", noisy+"added 42 packages in 3s\n", 0, "1")
	if !compacted || !strings.Contains(success, "added 42 packages") {
		t.Fatalf("success summary = %q, compacted=%t", success, compacted)
	}

	failure, compacted := compactShellResult("npm install", noisy+"npm ERR! code E404\nnpm ERR! 404 Not Found\n", 1, "1")
	if !compacted || !strings.Contains(failure, "E404") {
		t.Fatalf("failure summary = %q, compacted=%t", failure, compacted)
	}
}

func TestShellWrapperStoresOnlyCompactedOutputAndPreservesExitStatus(t *testing.T) {
	a := newApp(t)
	a.Environ = append(a.Environ, "JEVKIT_COMPACT_GENERIC=1")
	var out bytes.Buffer
	a.Stdout = &out
	a.Stderr = &bytes.Buffer{}

	if err := a.runShellWrapper(a.WorkDir, "cursor", "seq 1 5000"); err != nil {
		t.Fatalf("success wrapper: %v", err)
	}
	if !strings.Contains(out.String(), "[jevkit: shell output compacted; original:") {
		t.Fatalf("missing compact footer: %s", out.String())
	}

	out.Reset()
	err := a.runShellWrapper(a.WorkDir, "cursor", "printf 'failed\\n'; exit 17")
	ee, ok := err.(*exitError)
	if !ok || ee.code != 17 {
		t.Fatalf("exit error = %#v", err)
	}
	if out.String() != "failed\n" {
		t.Fatalf("failure output = %q", out.String())
	}
}

func TestShellWrapperFailsOpenWhenRawResultCannotBeStored(t *testing.T) {
	a := newApp(t)
	a.StateDir = ""
	a.Environ = append(a.Environ, "JEVKIT_COMPACT_GENERIC=1")
	var out bytes.Buffer
	a.Stdout, a.Stderr = &out, &bytes.Buffer{}

	if err := a.runShellWrapper(a.WorkDir, "cursor", "seq 1 5000"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "[jevkit: shell output compacted;") || !strings.Contains(out.String(), "5000") {
		t.Fatalf("storage failure must preserve raw output, got %q", out.String())
	}
}
