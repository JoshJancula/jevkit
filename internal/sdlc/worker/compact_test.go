package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCompactCodexWaitsForNativeCompletion(t *testing.T) {
	requireUnixShellFixture(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-codex")
	script := `#!/bin/sh
read first
echo '{"id":1,"result":{}}'
read initialized
read resume
echo '{"id":2,"result":{"thread":{"id":"thread-1"}}}'
read compact
echo '{"id":3,"result":{}}'
echo '{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"status":"completed"}}}'
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := CompactCodex(ctx, path, dir, "thread-1"); err != nil {
		t.Fatal(err)
	}
}

func TestCompactCodexReportsProtocolFailure(t *testing.T) {
	requireUnixShellFixture(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-codex")
	script := `#!/bin/sh
read first
echo '{"id":1,"error":{"message":"authentication failed"}}'
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := CompactCodex(ctx, path, dir, "thread-1"); err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("actual error missing: %v", err)
	}
}
