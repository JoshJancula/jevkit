package worker

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// workspaceSnapshot keeps a private Git index and object store. It captures
// the actual worktree state without touching the user's index or Git objects.
// Comparing two captured trees excludes changes that were already present
// before the invocation, including untracked binaries.
type workspaceSnapshot struct {
	dir, temporary, index, objects, alternates string
}

func newWorkspaceSnapshot(ctx context.Context, dir string) (*workspaceSnapshot, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--git-path", "objects")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("worker: locate Git objects: %w", err)
	}
	original := strings.TrimSpace(string(out))
	if !filepath.IsAbs(original) {
		original = filepath.Join(dir, original)
	}
	original, err = filepath.Abs(original)
	if err != nil {
		return nil, err
	}
	temporary, err := os.MkdirTemp("", "jevkit-sdlc-snapshot-")
	if err != nil {
		return nil, err
	}
	objects := filepath.Join(temporary, "objects")
	if err := os.MkdirAll(objects, 0o700); err != nil {
		_ = os.RemoveAll(temporary)
		return nil, err
	}
	alt := original
	if existing := os.Getenv("GIT_ALTERNATE_OBJECT_DIRECTORIES"); existing != "" {
		alt += string(os.PathListSeparator) + existing
	}
	return &workspaceSnapshot{dir: dir, temporary: temporary, index: filepath.Join(temporary, "index"), objects: objects, alternates: alt}, nil
}

func (s *workspaceSnapshot) close() { _ = os.RemoveAll(s.temporary) }

func (s *workspaceSnapshot) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = s.dir
	cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+s.index, "GIT_OBJECT_DIRECTORY="+s.objects, "GIT_ALTERNATE_OBJECT_DIRECTORIES="+s.alternates)
	return cmd
}

func (s *workspaceSnapshot) output(ctx context.Context, args ...string) ([]byte, error) {
	cmd := s.command(ctx, args...)
	out, err := cmd.Output()
	if err != nil {
		var detail string
		if e, ok := err.(*exec.ExitError); ok {
			detail = string(e.Stderr)
		}
		return nil, fmt.Errorf("worker: git %s: %w: %s", strings.Join(args, " "), err, truncate(detail, 300))
	}
	return out, nil
}

func (s *workspaceSnapshot) capture(ctx context.Context) (string, error) {
	// A fresh private index ensures ignore changes and deletions are reflected.
	_ = os.Remove(s.index)
	if _, err := s.output(ctx, "add", "-A", "--"); err != nil {
		return "", err
	}
	out, err := s.output(ctx, "write-tree")
	return strings.TrimSpace(string(out)), err
}

const maxChangeReport = 60 * 1024
const maxPatchPerFile = 16 * 1024

// report contains only this invocation's changes. Git's normal text diff
// omits binary payloads; binary files are represented by object hashes.
func (s *workspaceSnapshot) report(ctx context.Context, before, after string) (string, error) {
	raw, err := s.output(ctx, "diff", "--name-only", "-z", "--no-renames", before, after)
	if err != nil {
		return "", err
	}
	paths := bytes.Split(bytes.TrimSuffix(raw, []byte{0}), []byte{0})
	if len(raw) == 0 {
		paths = nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Invocation change report\nBefore tree: %s\nAfter tree: %s\nChanged paths: %d\n", before, after, len(paths))
	for i, rawPath := range paths {
		path := string(rawPath)
		oldID, err := s.blobID(ctx, before, path)
		if err != nil {
			return "", err
		}
		newID, err := s.blobID(ctx, after, path)
		if err != nil {
			return "", err
		}
		status := "modified"
		if oldID == "" {
			status = "added"
		} else if newID == "" {
			status = "deleted"
		}
		entry := fmt.Sprintf("- %s %q (before %s, after %s)\n", status, path, shortID(oldID), shortID(newID))
		if b.Len()+len(entry) > maxChangeReport-256 {
			fmt.Fprintf(&b, "... %d more paths; inspect the workspace.\n", len(paths)-i)
			break
		}
		b.WriteString(entry)
	}
	b.WriteString("\nText patch excerpts (binary payloads omitted):\n")
	for _, rawPath := range paths {
		if b.Len() > maxChangeReport-maxPatchPerFile-256 {
			b.WriteString("[remaining patch excerpts omitted; inspect the changed files]\n")
			break
		}
		path := string(rawPath)
		var patch boundedOutput
		patch.max = maxPatchPerFile
		cmd := s.command(ctx, "--no-pager", "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--unified=3", before, after, "--", path)
		cmd.Stdout = &patch
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("worker: diff %q: %w: %s", path, err, truncate(stderr.String(), 300))
		}
		b.Write(patch.data)
		if patch.truncated {
			b.WriteString("\n[patch excerpt truncated; inspect file in workspace]\n")
		}
	}
	return b.String(), nil
}

// changedPaths records evidence of drift without assigning it to the worker.
// The list is bounded because a workspace can contain arbitrarily many files.
func (s *workspaceSnapshot) changedPaths(ctx context.Context, before, after string) ([]string, bool, error) {
	raw, err := s.output(ctx, "diff", "--name-only", "-z", "--no-renames", before, after)
	if err != nil || len(raw) == 0 {
		return nil, false, err
	}
	parts := bytes.Split(bytes.TrimSuffix(raw, []byte{0}), []byte{0})
	const limit = 100
	paths := make([]string, 0, min(len(parts), limit))
	for _, part := range parts[:min(len(parts), limit)] {
		paths = append(paths, string(part))
	}
	return paths, len(parts) > limit, nil
}

func (s *workspaceSnapshot) blobID(ctx context.Context, tree, path string) (string, error) {
	out, err := s.output(ctx, "ls-tree", "-z", tree, "--", path)
	if err != nil {
		return "", err
	}
	if len(out) == 0 {
		return "", nil
	}
	meta, _, ok := bytes.Cut(out, []byte{'\t'})
	if !ok {
		return "", fmt.Errorf("worker: invalid ls-tree response for %q", path)
	}
	parts := strings.Fields(string(meta))
	if len(parts) != 3 {
		return "", fmt.Errorf("worker: invalid ls-tree metadata for %q", path)
	}
	return parts[2], nil
}

func shortID(id string) string {
	if id == "" {
		return "none"
	}
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

type boundedOutput struct {
	data      []byte
	max       int
	truncated bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	take := b.max - len(b.data)
	if take < 0 {
		take = 0
	}
	if take > n {
		take = n
	}
	b.data = append(b.data, p[:take]...)
	if take < n {
		b.truncated = true
	}
	return n, nil
}
