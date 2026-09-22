package audit

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/OWNER/jevkit/internal/redact/config"
)

// ErrDeclined means the user answered no to the confirm prompt; nothing was
// sent or recorded.
var ErrDeclined = errors.New("send declined at the confirm prompt")

// Prompter asks the user whether the redacted payload may be sent.
type Prompter interface {
	Confirm(payload string) (bool, error)
}

// Meta labels a send in the audit log.
type Meta struct {
	Agent       string
	QuestionSet string
}

// Transparency wraps the send path with an audit line, the review store and
// the optional confirm step.
type Transparency struct {
	Log    *Log
	Review *Review
	// Prompter is set only by interactive callers (`jevkit key test`, MCP
	// jev_ask style calls). Hooks leave it nil so they never block, even when
	// redact.confirm is on.
	Prompter Prompter
	Now      func() time.Time
}

// Gate is config.Gate with transparency: never_send, then redaction, then the
// confirm prompt, then the audit line and review entry, then send. A failure
// to record stops the send: what cannot be audited is not sent.
func (t *Transparency) Gate(ctx context.Context, cfg *config.Config, m Meta, subject, text string, send config.Send) (out, pattern string, sent bool, err error) {
	if p, ok := cfg.NeverSend.Match(subject); ok {
		return "", p, false, nil
	}
	r, err := cfg.Redactor()
	if err != nil {
		return "", "", false, err
	}
	res, err := r.Apply(text)
	if err != nil {
		return "", "", false, err
	}
	if cfg.Confirm && t.Prompter != nil {
		ok, err := t.Prompter.Confirm(res.Text)
		if err != nil {
			return "", "", false, err
		}
		if !ok {
			return "", "", false, ErrDeclined
		}
	}
	now := time.Now
	if t.Now != nil {
		now = t.Now
	}
	hits := map[string]int{}
	for _, h := range res.Hits {
		hits[h.RuleID] += h.Count
	}
	if t.Log != nil {
		rec := Record{Time: now(), Agent: m.Agent, QuestionSet: m.QuestionSet, BytesBefore: len(text), BytesAfter: len(res.Text), Hits: hits}
		if err := t.Log.Append(rec); err != nil {
			return "", "", false, fmt.Errorf("audit log: %w", err)
		}
	}
	if t.Review != nil {
		if err := t.Review.Add(Entry{Time: now(), Agent: m.Agent, QuestionSet: m.QuestionSet, Payload: res.Text}); err != nil {
			return "", "", false, fmt.Errorf("review store: %w", err)
		}
	}
	out, err = send(ctx, res.Text)
	return out, "", err == nil, err
}

type terminal struct {
	in  *bufio.Reader
	out io.Writer
}

// NewPrompter shows the payload on out and reads a y/yes answer from in. Any
// other answer, including end of input, is a no.
func NewPrompter(in io.Reader, out io.Writer) Prompter {
	return &terminal{in: bufio.NewReader(in), out: out}
}

func (p *terminal) Confirm(payload string) (bool, error) {
	if _, err := fmt.Fprintf(p.out, "--- payload to be sent (already redacted) ---\n%s\n--- end payload ---\nSend this to TypeSafe AI? [y/N] ", payload); err != nil {
		return false, err
	}
	line, err := p.in.ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}
