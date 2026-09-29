package worker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func initWorktreeRepo(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "README")
	run("commit", "-m", "init")
}

func TestGitWorktreeCreateAndRemove(t *testing.T) {
	repo := t.TempDir()
	initWorktreeRepo(t, repo)
	ctx := context.Background()
	var c GitWorktreeCreator
	if !c.Available(ctx, repo) {
		t.Fatal("expected git worktree available")
	}
	rev, err := ResolveSourceRevision(ctx, repo)
	if err != nil || rev == "" {
		t.Fatalf("rev: %q %v", rev, err)
	}
	dest := filepath.Join(t.TempDir(), "wt-a")
	ws, err := c.Create(ctx, repo, rev, dest)
	if err != nil {
		t.Fatal(err)
	}
	if ws.Path != dest || ws.BaseRev != rev || ws.SecurityOK {
		t.Fatalf("workspace: %+v", ws)
	}
	if _, err := os.Stat(filepath.Join(dest, "README")); err != nil {
		t.Fatal(err)
	}
	if err := c.Remove(ctx, ws); err != nil {
		t.Fatal(err)
	}
}

func TestGitWorktreeUnavailableOutsideRepo(t *testing.T) {
	var c GitWorktreeCreator
	if c.Available(context.Background(), t.TempDir()) {
		t.Fatal("non-git dir reported available")
	}
}
