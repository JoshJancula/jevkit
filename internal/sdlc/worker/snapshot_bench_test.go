package worker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// seedSnapshotFixture builds a git worktree with n tracked-looking files of
// modest size. Files are written but not committed: workspaceSnapshot stages
// the live worktree into a private index, which is the measured cost.
func seedSnapshotFixture(b *testing.B, n int) string {
	b.Helper()
	dir := b.TempDir()
	initGitRepo(b, dir)
	for i := 0; i < n; i++ {
		name := filepath.Join(dir, fmt.Sprintf("f-%04d.txt", i))
		if err := os.WriteFile(name, []byte(fmt.Sprintf("content-%d\n", i)), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	return dir
}

// BenchmarkWorkspaceSnapshotCapture covers the measured Git-snapshot hotspot:
// first capture stages a cold private index (cost scales with worktree size),
// second capture against an unchanged tree reuses hashed entries so cost
// should stay flatter as the fixture grows. Small and large fixtures make a
// regression that reintroduces a full rebuild visible as a large jump on
// second/files=500 specifically.
func BenchmarkWorkspaceSnapshotCapture(b *testing.B) {
	for _, n := range []int{10, 500} {
		b.Run(fmt.Sprintf("files=%d/first", n), func(b *testing.B) {
			dir := seedSnapshotFixture(b, n)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				snapshot, err := newWorkspaceSnapshot(context.Background(), dir)
				if err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				if _, err := snapshot.capture(context.Background()); err != nil {
					snapshot.close()
					b.Fatal(err)
				}
				b.StopTimer()
				snapshot.close()
			}
		})
		b.Run(fmt.Sprintf("files=%d/second-unchanged", n), func(b *testing.B) {
			dir := seedSnapshotFixture(b, n)
			snapshot, err := newWorkspaceSnapshot(context.Background(), dir)
			if err != nil {
				b.Fatal(err)
			}
			defer snapshot.close()
			if _, err := snapshot.capture(context.Background()); err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := snapshot.capture(context.Background()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
