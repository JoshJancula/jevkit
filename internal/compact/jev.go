package compact

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/OWNER/jevkit/internal/jev"
)

// Asker is the slice of the Jev client the compactor uses.
type Asker interface {
	Ask(ctx context.Context, req jev.Request) (*jev.Response, error)
}

// JevOptions controls optional model-assisted compaction. The zero value is
// safe: compaction is disabled by default.
type JevOptions struct {
	Enabled            bool
	Shadow             bool
	ThresholdBytes     int
	MaxLinesPerRequest int
	StateDir           string
	AuthoritativeExit  bool
	// RawPointer must contain the complete, byte-identical original output.
	RawPointer   string
	Runtime      string
	PolicyRuleID string
	Policy       *Policy
}

const (
	DefaultJevThresholdBytes  = 8192
	DefaultMaxLinesPerRequest = 255
)

func (o JevOptions) threshold() int {
	if o.ThresholdBytes <= 0 {
		return DefaultJevThresholdBytes
	}
	return o.ThresholdBytes
}

func (o JevOptions) maxLines() int {
	if o.MaxLinesPerRequest <= 0 {
		return DefaultMaxLinesPerRequest
	}
	return min(o.MaxLinesPerRequest, jev.MaxChoiceOptions)
}

type JevResult struct {
	Body string
	Used bool
	Err  error
}

// JevCompact first applies the deterministic eligibility and compaction
// guards. The model tier runs only with a verified, retrievable raw original;
// an unavailable model falls back to the deterministic result. Shadow mode
// always returns the original bytes.
func JevCompact(command, stdout, stderr string, exitStatus int, asker Asker, opts JevOptions) (JevResult, Result) {
	original := joinStreams(stdout, stderr)
	passthrough := func(err error, family string) (JevResult, Result) {
		return JevResult{Body: original, Err: err}, unchanged(stdout, stderr, exitStatus, family)
	}
	if !opts.Enabled || strings.Contains(original, "[jevkit]") || strings.Contains(original, "[jevkit:") {
		return passthrough(nil, "")
	}
	base := Compact(command, stdout, stderr, exitStatus, Options{ThresholdBytes: opts.threshold(), Policy: opts.Policy})
	if !base.Compacted {
		return passthrough(nil, base.Family)
	}
	// Do not invoke the model or shorten output when the original cannot be
	// recovered. The previous v1 ranking path had no retrievable original and
	// accepted spoofed marker text as a substitute for source verification.
	if opts.RawPointer == "" {
		return passthrough(nil, base.Family)
	}
	stored, err := os.ReadFile(opts.RawPointer)
	if err != nil {
		return passthrough(fmt.Errorf("read raw original: %w", err), base.Family)
	}
	if string(stored) != original {
		return passthrough(fmt.Errorf("raw original differs from tool result"), base.Family)
	}
	if asker == nil {
		if opts.Shadow {
			return passthrough(nil, base.Family)
		}
		return JevResult{Body: joinStreams(base.Stdout, base.Stderr)}, base
	}
	return compactV2(command, stdout, stderr, exitStatus, asker, opts, base)
}
