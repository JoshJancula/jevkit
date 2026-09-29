package security

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRequireCommandAuthorization(t *testing.T) {
	if err := RequireCommandAuthorization("", "abc"); err == nil {
		t.Fatal("empty checks digest must fail")
	}
	if err := RequireCommandAuthorization("abc", ""); err == nil {
		t.Fatal("empty authorized digest must fail")
	}
	if err := RequireCommandAuthorization("abc", "def"); err == nil {
		t.Fatal("mismatched digests must fail")
	}
	if err := RequireCommandAuthorization("abc", "abc"); err != nil {
		t.Fatal(err)
	}
}

func TestRunArgvRequiresAuthorization(t *testing.T) {
	dir := t.TempDir()
	_, err := RunArgv(context.Background(), ArgvRequest{
		Argv:             []string{"echo", "hi"},
		Workspace:        dir,
		ChecksDigest:     "aaa",
		AuthorizedDigest: "bbb",
	})
	if err == nil || !strings.Contains(err.Error(), "not authorized") {
		t.Fatalf("expected authorization failure: %v", err)
	}
}

func TestRunArgvArgvWithoutShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture executes a Unix shell script")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "out.txt")
	digest := "authorized-digest"
	// Use argv form (no UseShell). Absolute paths in argv are allowed for the
	// binary itself when sandbox heuristics do not treat the command string as
	// escaping; prefer a relative script inside the workspace.
	script := filepath.Join(dir, "write.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf hi > out.txt\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := RunArgv(context.Background(), ArgvRequest{
		Argv:             []string{"./write.sh"},
		Workspace:        dir,
		ChecksDigest:     digest,
		AuthorizedDigest: digest,
		Timeout:          5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit=%d stderr=%s", result.ExitCode, result.Stderr)
	}
	raw, err := os.ReadFile(out)
	if err != nil || string(raw) != "hi" {
		t.Fatalf("output: %q %v", raw, err)
	}
}

func TestRunArgvDeniesSandboxEscape(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	digest := "authorized-digest"
	_, err := RunArgv(context.Background(), ArgvRequest{
		Argv:             []string{"cat", filepath.Join(outside, "secret")},
		Workspace:        dir,
		ChecksDigest:     digest,
		AuthorizedDigest: digest,
	})
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("expected sandbox deny: %v", err)
	}
}

func TestRunArgvClosedStdinAndBoundedOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture executes a Unix shell script")
	}
	dir := t.TempDir()
	digest := "authorized-digest"
	script := filepath.Join(dir, "noise.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ndd if=/dev/zero bs=1024 count=8 2>/dev/null\ncat\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := RunArgv(context.Background(), ArgvRequest{
		Argv:             []string{"./noise.sh"},
		Workspace:        dir,
		ChecksDigest:     digest,
		AuthorizedDigest: digest,
		MaxOutputBytes:   64,
		Timeout:          5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || len(result.Stdout) > 64 {
		t.Fatalf("expected truncated stdout: truncated=%v len=%d", result.Truncated, len(result.Stdout))
	}
}

func TestRunArgvRejectsEmptyArgv(t *testing.T) {
	dir := t.TempDir()
	digest := "ok"
	_, err := RunArgv(context.Background(), ArgvRequest{
		Argv:             nil,
		Workspace:        dir,
		ChecksDigest:     digest,
		AuthorizedDigest: digest,
	})
	if err == nil || !strings.Contains(err.Error(), "empty argv") {
		t.Fatalf("expected empty argv error: %v", err)
	}
}
