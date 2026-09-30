package worker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// IsolatedWorkspace is a proven write boundary at one recorded source revision.
// A Git worktree alone is not a security sandbox; callers must still enforce
// enrolled scopes and runtime isolation claims separately.
type IsolatedWorkspace struct {
	Path       string
	Mode       string
	BaseRev    string
	RepoRoot   string
	Worktree   bool
	SecurityOK bool // true only when a runtime/enforcement layer is also active
}

// WorktreeCreator creates and removes isolated Git worktrees. Tests inject fakes.
type WorktreeCreator interface {
	Create(ctx context.Context, repoRoot, baseRev, dest string) (IsolatedWorkspace, error)
	Remove(ctx context.Context, ws IsolatedWorkspace) error
	Available(ctx context.Context, repoRoot string) bool
}

// GitWorktreeCreator uses `git worktree add --detach` at a recorded revision.
type GitWorktreeCreator struct{}

func (GitWorktreeCreator) Available(ctx context.Context, repoRoot string) bool {
	if strings.TrimSpace(repoRoot) == "" {
		return false
	}
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		return false
	}
	cmd = exec.CommandContext(ctx, "git", "rev-parse", "--verify", "HEAD")
	cmd.Dir = repoRoot
	return cmd.Run() == nil
}

func (GitWorktreeCreator) Create(ctx context.Context, repoRoot, baseRev, dest string) (IsolatedWorkspace, error) {
	if strings.TrimSpace(repoRoot) == "" || strings.TrimSpace(dest) == "" {
		return IsolatedWorkspace{}, fmt.Errorf("worker: worktree requires repo root and destination")
	}
	rev := strings.TrimSpace(baseRev)
	if rev == "" {
		out, err := exec.CommandContext(ctx, "git", "rev-parse", "HEAD").CombinedOutput()
		if err != nil {
			return IsolatedWorkspace{}, fmt.Errorf("worker: resolve HEAD: %w (%s)", err, strings.TrimSpace(string(out)))
		}
		rev = strings.TrimSpace(string(out))
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return IsolatedWorkspace{}, err
	}
	if _, err := os.Stat(dest); err == nil {
		return IsolatedWorkspace{}, fmt.Errorf("worker: worktree destination already exists: %s", dest)
	}
	cmd := exec.CommandContext(ctx, "git", "worktree", "add", "--detach", dest, rev)
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		return IsolatedWorkspace{}, fmt.Errorf("worker: git worktree add: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return IsolatedWorkspace{
		Path:     dest,
		Mode:     "worktree",
		BaseRev:  rev,
		RepoRoot: repoRoot,
		Worktree: true,
		// Git worktree is a write boundary only — not a sandbox.
		SecurityOK: false,
	}, nil
}

func (GitWorktreeCreator) Remove(ctx context.Context, ws IsolatedWorkspace) error {
	if !ws.Worktree || ws.Path == "" || ws.RepoRoot == "" {
		return nil
	}
	cmd := exec.CommandContext(ctx, "git", "worktree", "remove", "--force", ws.Path)
	cmd.Dir = ws.RepoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		// Best-effort cleanup if git remove fails (partial create).
		_ = os.RemoveAll(ws.Path)
		return fmt.Errorf("worker: git worktree remove: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ResolveSourceRevision returns the commit the supervisor records for fan-out.
func ResolveSourceRevision(ctx context.Context, repoRoot string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("worker: resolve source revision: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
