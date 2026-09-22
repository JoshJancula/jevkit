package exec

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/OWNER/jevkit/internal/compact"
	"github.com/OWNER/jevkit/internal/jev"
)

func TestRunPassesThroughExitAndStreams(t *testing.T) {
	var stdout, stderr bytes.Buffer
	got := Run(context.Background(), []string{"tool", "two words", ""}, Options{
		Stdout: &stdout, Stderr: &stderr,
		Execute: func(_ context.Context, argv []string) ([]byte, []byte, int, error) {
			want := []string{"tool", "two words", ""}
			if len(argv) != len(want) {
				t.Fatalf("argv = %#v", argv)
			}
			for i := range want {
				if argv[i] != want[i] {
					t.Fatalf("argv = %#v", argv)
				}
			}
			return []byte("out\n"), []byte("err\n"), 23, nil
		},
	})
	if got != 23 || stdout.String() != "out\n" || stderr.String() != "err\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", got, stdout.String(), stderr.String())
	}
}

func TestRunCompactsLargeOutput(t *testing.T) {
	var stdout bytes.Buffer
	large := strings.Repeat("ordinary log line\n", 2000)
	got := Run(context.Background(), []string{"tool"}, Options{
		Stdout:  &stdout,
		Execute: func(context.Context, []string) ([]byte, []byte, int, error) { return []byte(large), nil, 0, nil },
	})
	if got != 0 {
		t.Fatalf("code=%d", got)
	}
	if len(stdout.String()) >= len(large) {
		t.Fatalf("large output was not compacted: %d >= %d", len(stdout.String()), len(large))
	}
}

type failingAsker struct{}

func (failingAsker) Ask(context.Context, jev.Request) (*jev.Response, error) {
	return nil, errors.New("jev unavailable")
}

func TestRunJevFailureFailsOpen(t *testing.T) {
	var stdout bytes.Buffer
	large := strings.Repeat("ordinary log line\n", 2000)
	got := Run(context.Background(), []string{"tool"}, Options{
		Stdout: &stdout, Asker: failingAsker{}, JevOptions: compact.JevOptions{Enabled: true, ThresholdBytes: 1},
		Execute: func(context.Context, []string) ([]byte, []byte, int, error) { return []byte(large), nil, 9, nil },
	})
	if got != 9 || stdout.String() != large {
		t.Fatalf("code=%d output changed=%t", got, stdout.String() != large)
	}
}
