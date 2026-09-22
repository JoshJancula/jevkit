package jev

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const maxResponseBytes = 4 << 20

var questionSetIDSafe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// Client posts questions to SystemOne.
type Client struct {
	Config Config
	// Key returns the API key; it is only called for live transport.
	Key     func() (string, error)
	HTTP    *http.Client
	Breaker Breaker
	// Sleep is the backoff wait; defaults to a context-aware time.Sleep.
	Sleep func(ctx context.Context, d time.Duration)
}

// New returns a Client with a MemoryBreaker and default HTTP client.
func New(cfg Config, key func() (string, error)) *Client {
	return &Client{Config: cfg, Key: key, HTTP: &http.Client{}, Breaker: &MemoryBreaker{}}
}

// Ask validates req, then posts it (or replays a fixture) and returns the
// strictly decoded response. All errors are *Error.
func (c *Client) Ask(ctx context.Context, req Request) (*Response, error) {
	if c.Breaker != nil && c.Breaker.IsOpen() {
		return nil, &Error{Code: CodeDeclined, Reason: "breaker-open"}
	}
	if req.Model == "" {
		req.Model = c.Config.Model
		if req.Model == "" {
			req.Model = DefaultModel
		}
	}
	body, err := prepare(req)
	if err != nil {
		return nil, &Error{Code: CodeRejected, Reason: "input rejected", Err: err}
	}

	fixture := c.Config.Transport == TransportFixture
	key := ""
	if !fixture {
		if c.Key == nil {
			return nil, &Error{Code: CodeTransport, Reason: "no api key"}
		}
		if key, err = c.Key(); err != nil || key == "" {
			return nil, &Error{Code: CodeTransport, Reason: "no api key"}
		}
	}

	for attempt := 0; ; attempt++ {
		var status int
		var raw []byte
		if fixture {
			var ferr *Error
			status, raw, ferr = c.replayFixture(req, body, attempt)
			if ferr != nil {
				return nil, ferr
			}
		} else {
			var terr error
			status, raw, terr = c.post(ctx, key, body)
			if terr != nil {
				if ctx.Err() == nil && attempt < c.Config.MaxRetries {
					c.sleep(ctx, backoff(attempt))
					continue
				}
				return nil, c.fail("connection-failure", scrub(terr, key))
			}
		}

		switch {
		case status == http.StatusUnauthorized || status == http.StatusUnprocessableEntity:
			reason := fmt.Sprintf("http-%d", status)
			if c.Breaker != nil {
				c.Breaker.Open(reason)
			}
			return nil, &Error{Code: CodeTransport, Reason: reason, Config: true}
		case status == 429 || status == 529:
			if attempt < c.Config.MaxRetries {
				c.sleep(ctx, backoff(attempt))
				continue
			}
			return nil, c.fail(fmt.Sprintf("http-%d", status), nil)
		case status < 200 || status > 299:
			return nil, c.fail(fmt.Sprintf("http-%d", status), nil)
		}

		resp, derr := DecodeResponse(raw)
		if derr != nil {
			return nil, c.fail("protocol", scrub(derr, key))
		}
		if c.Breaker != nil {
			c.Breaker.RecordSuccess()
		}
		return resp, nil
	}
}

func (c *Client) fail(reason string, err error) *Error {
	if c.Breaker != nil {
		c.Breaker.RecordFailure(reason)
	}
	return &Error{Code: CodeTransport, Reason: reason, Err: err}
}

// prepare validates the request and returns the wire body (no questionSetId).
func prepare(req Request) ([]byte, error) {
	if req.Questions == nil {
		return nil, errors.New("no questions")
	}
	longest := 0
	for name, q := range req.Questions {
		if q == nil {
			return nil, fmt.Errorf("question %q is nil", name)
		}
		if err := q.validate(); err != nil {
			return nil, fmt.Errorf("question %q: %w", name, err)
		}
		b, err := json.Marshal(q)
		if err != nil {
			return nil, err
		}
		longest = max(longest, len(b))
	}
	if len(req.State)+longest > budgetBytes {
		return nil, fmt.Errorf("request exceeds %d-token budget", MaxTokens)
	}
	return json.Marshal(req)
}

func (c *Client) post(ctx context.Context, key string, body []byte) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.Config.Timeout)
	defer cancel()
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Config.Endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Authorization", "Bearer "+key)
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	res, err := hc.Do(hreq)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes))
	if err != nil {
		return 0, nil, err
	}
	return res.StatusCode, raw, nil
}

// replayFixture serves a recorded response: <dir>/<questionSetId>.json, or
// sha256-<hex of wire body>.json when there is no set id. A fixture may hold
// a "sequence" of steps indexed by attempt (last step repeats).
func (c *Client) replayFixture(req Request, body []byte, attempt int) (int, []byte, *Error) {
	name := "sha256-" + hexSum(body)
	if req.QuestionSetID != "" {
		if !questionSetIDSafe.MatchString(req.QuestionSetID) {
			return 0, nil, &Error{Code: CodeRejected, Reason: "fixture-missing"}
		}
		name = req.QuestionSetID
	}
	data, err := os.ReadFile(filepath.Join(c.Config.FixtureDir, name+".json"))
	if err != nil {
		return 0, nil, &Error{Code: CodeRejected, Reason: "fixture-missing"}
	}
	var fx map[string]json.RawMessage
	if err := json.Unmarshal(data, &fx); err != nil {
		return 0, nil, &Error{Code: CodeTransport, Reason: "fixture-invalid", Err: err}
	}
	if seq, ok := fx["sequence"]; ok {
		var steps []map[string]json.RawMessage
		if err := json.Unmarshal(seq, &steps); err != nil || len(steps) == 0 {
			return 0, nil, &Error{Code: CodeTransport, Reason: "fixture-invalid"}
		}
		fx = steps[min(attempt, len(steps)-1)]
	}
	status := http.StatusOK
	if s, ok := fx["status"]; ok {
		if err := json.Unmarshal(s, &status); err != nil {
			return 0, nil, &Error{Code: CodeTransport, Reason: "fixture-invalid", Err: err}
		}
	}
	if b, ok := fx["body"]; ok {
		return status, b, nil
	}
	delete(fx, "status")
	delete(fx, "sequence")
	out, _ := json.Marshal(fx)
	return status, out, nil
}

func hexSum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func backoff(attempt int) time.Duration { return time.Duration(1<<attempt) * time.Second }

func (c *Client) sleep(ctx context.Context, d time.Duration) {
	if c.Sleep != nil {
		c.Sleep(ctx, d)
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// scrub removes the API key from an error's text so it can never leak into
// logs or returned errors.
func scrub(err error, key string) error {
	if err == nil || key == "" {
		return err
	}
	msg := err.Error()
	if !strings.Contains(msg, key) {
		return err
	}
	return errors.New(strings.ReplaceAll(msg, key, "[REDACTED]"))
}
