package worker

import (
	"strings"
	"testing"

	"github.com/JoshJancula/jevkit/internal/sdlc/adaptive"
	"github.com/JoshJancula/jevkit/internal/sdlc/enrollment"
)

// BenchmarkInvocationLogWriteManySmallChunks is the regression guard for the
// invocationLog.Write fix: before it, every Write call rewrote the entire
// accumulated tail to disk with os.WriteFile, so total cost for N chunks
// summing to well under the tail cap was O(N^2) in bytes written, not O(N).
// It runs at both a size that stays under MaxLogTail (the common case per
// the baseline: "a few KB to a few hundred KB") and one well past it, so a
// regression that reintroduces full-file rewrites on the fast path shows up
// as a large jump in ns/op and B/op specifically for the "under cap" case.
func BenchmarkInvocationLogWriteManySmallChunks(b *testing.B) {
	chunk := []byte(strings.Repeat("x", 200) + "\n")
	cases := []struct {
		name     string
		numWrite int
		tailCap  int
	}{
		{"under-cap", 500, MaxLogTail},          // 500 * 201B ~= 100KB, well under the 1MiB cap
		{"over-cap-small-tail", 5000, 64 << 10}, // forces repeated rollover against a small cap
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				dir := b.TempDir()
				req := Request{Agent: enrollment.Agent{ID: "reviewer", Runtime: "codex"}, Assignment: adaptive.Assignment{InvocationID: "inv"}, LogDir: dir, LogTailBytes: c.tailCap}
				log, err := newInvocationLog(req, "stdout")
				if err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				for n := 0; n < c.numWrite; n++ {
					if _, err := log.Write(chunk); err != nil {
						b.Fatal(err)
					}
				}
				b.StopTimer()
				_ = log.close()
			}
		})
	}
}
