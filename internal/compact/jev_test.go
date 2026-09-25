package compact

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/OWNER/jevkit/internal/jev"
)

type fakeAsker struct {
	resp *jev.Response
	err  error
	reqs []jev.Request
}

func (f *fakeAsker) Ask(_ context.Context, req jev.Request) (*jev.Response, error) {
	f.reqs = append(f.reqs, req)
	return f.resp, f.err
}

func TestJevCompactRequiresVerifiedRawOriginal(t *testing.T) {
	original := strings.Repeat("compiling module 1234567890\n", 300)
	for _, opts := range []JevOptions{{}, {Enabled: true}, {Enabled: true, RawPointer: rawFixture(t, "different")}} {
		a := &fakeAsker{}
		got, result := JevCompact("custom-build", original, "", 0, a, opts)
		if got.Body != original || got.Used || result.Compacted || len(a.reqs) != 0 {
			t.Fatalf("raw original guard failed: %+v %+v", got, result)
		}
	}
}

func TestJevCompactSourceOutputNeverSent(t *testing.T) {
	fx := loadFixture(t, "git-diff-hunks")
	a := &fakeAsker{}
	got, result := JevCompact(fx.Command, fx.Stdout, fx.Stderr, fx.Exit, a, JevOptions{Enabled: true, ThresholdBytes: 1})
	if got.Body != joinStreams(fx.Stdout, fx.Stderr) || result.Compacted || result.Family != FamilyGitDiff || len(a.reqs) != 0 {
		t.Fatalf("source output reached model: %+v", result)
	}
}

func TestJevCompactUnavailableModelUsesDeterministicResult(t *testing.T) {
	original := strings.Repeat("compiling module 1234567890\n", 300)
	pointer := rawFixture(t, original)
	got, result := JevCompact("custom-build", original, "", 0, nil, JevOptions{Enabled: true, ThresholdBytes: 200, RawPointer: pointer})
	if !result.Compacted || got.Used || got.Body == original {
		t.Fatalf("deterministic fallback failed: %+v %+v", got, result)
	}
	shadow, output := JevCompact("custom-build", original, "", 0, nil, JevOptions{Enabled: true, Shadow: true, ThresholdBytes: 200, RawPointer: pointer})
	if output.Compacted || shadow.Body != original {
		t.Fatalf("shadow changed output: %+v %+v", shadow, output)
	}
}

func TestJevCompactTransportFailureUsesDeterministicResult(t *testing.T) {
	original := strings.Repeat("compiling module 1234567890\n", 300)
	a := &fakeAsker{err: errors.New("jev down")}
	got, result := JevCompact("custom-build", original, "", 0, a, JevOptions{Enabled: true, ThresholdBytes: 200, RawPointer: rawFixture(t, original)})
	if !result.Compacted || got.Used || got.Body == original || !errors.Is(got.Err, a.err) || len(a.reqs) != 1 {
		t.Fatalf("transport fallback failed: %+v %+v", got, result)
	}
}

func TestJevCompactRejectsAlreadyCompactedOutput(t *testing.T) {
	original := strings.Repeat("compiling module 1234567890\n", 300) + "[jevkit] 2 original line(s) omitted\n"
	a := &fakeAsker{}
	got, result := JevCompact("custom-build", original, "", 0, a, JevOptions{Enabled: true, ThresholdBytes: 200, RawPointer: rawFixture(t, original)})
	if result.Compacted || got.Body != original || len(a.reqs) != 0 {
		t.Fatalf("already compacted output was processed: %+v", result)
	}
}
