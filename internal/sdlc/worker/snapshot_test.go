package worker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initGitRepo(t testing.TB, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "initial"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

// TestCaptureDropsNewlyIgnoredPathsWithReusedIndex is the correctness guard
// for index reuse across before/after capture(): a mid-invocation .gitignore
// change must exclude the newly ignored path from the after tree exactly as a
// fresh private index would, or change detection silently misses the drop.
func TestCaptureDropsNewlyIgnoredPathsWithReusedIndex(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := newWorkspaceSnapshot(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.close()
	before, err := snapshot.capture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	beforeNames := treeNames(t, snapshot, before)
	if !strings.Contains(beforeNames, "secret.txt") || !strings.Contains(beforeNames, "keep.txt") {
		t.Fatalf("before tree missing expected paths: %q", beforeNames)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("secret.txt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := snapshot.capture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	afterNames := treeNames(t, snapshot, after)
	if strings.Contains(afterNames, "secret.txt") {
		t.Fatalf("reused index left newly ignored path staged: %q", afterNames)
	}
	if !strings.Contains(afterNames, "keep.txt") || !strings.Contains(afterNames, ".gitignore") {
		t.Fatalf("after tree missing expected paths: %q", afterNames)
	}
	paths, truncated, err := snapshot.changedPaths(context.Background(), before, after)
	if err != nil || truncated {
		t.Fatalf("changedPaths: %v truncated=%v", err, truncated)
	}
	joined := strings.Join(paths, "\n")
	if !strings.Contains(joined, "secret.txt") || !strings.Contains(joined, ".gitignore") {
		t.Fatalf("change detection missed ignore transition: %q", joined)
	}
}

func treeNames(t testing.TB, s *workspaceSnapshot, tree string) string {
	t.Helper()
	out, err := s.output(context.Background(), "ls-tree", "-r", "--name-only", tree)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
