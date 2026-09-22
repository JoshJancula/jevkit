package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ReviewFileName is the review store's name inside the state directory.
const ReviewFileName = "redaction-review.json"

// Entry is one stored send: the exact already-redacted payload.
type Entry struct {
	Time        time.Time `json:"time"`
	Agent       string    `json:"agent"`
	QuestionSet string    `json:"question_set"`
	Payload     string    `json:"payload"`
}

// Review keeps the last Max sends for TTL. It is off unless Enabled, because
// stored payloads are themselves sensitive; while off it holds nothing and
// deletes anything a previous run left behind.
type Review struct {
	Path    string
	Enabled bool
	// Max and TTL default to 20 and 24h when zero.
	Max int
	TTL time.Duration
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	mu  sync.Mutex
}

func (r *Review) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Review) limits() (int, time.Duration) {
	max, ttl := r.Max, r.TTL
	if max <= 0 {
		max = 20
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return max, ttl
}

// load reads the store and drops expired entries; changed says some went.
func (r *Review) load() (entries []Entry, changed bool, err error) {
	data, err := os.ReadFile(r.Path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		// A corrupt store is dropped: it must not linger past its TTL.
		return nil, true, nil
	}
	_, ttl := r.limits()
	now := r.now()
	kept := entries[:0]
	for _, e := range entries {
		if now.Sub(e.Time) < ttl {
			kept = append(kept, e)
		}
	}
	return kept, len(kept) != len(entries), nil
}

func (r *Review) save(entries []Entry) error {
	if len(entries) == 0 {
		if err := os.Remove(r.Path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	dir := filepath.Dir(r.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".review-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	fail := func(err error) error {
		_ = f.Close()
		_ = os.Remove(name)
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		return fail(err)
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, r.Path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

// Add stores e (stamped now when e.Time is zero), purges expired entries and
// keeps only the newest Max. It is a no-op that also purges when disabled.
func (r *Review) Add(e Entry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.Enabled {
		return r.save(nil)
	}
	entries, _, err := r.load()
	if err != nil {
		return err
	}
	if e.Time.IsZero() {
		e.Time = r.now()
	}
	e.Agent, e.QuestionSet = clean(e.Agent), clean(e.QuestionSet)
	entries = append(entries, e)
	if max, _ := r.limits(); len(entries) > max {
		entries = entries[len(entries)-max:]
	}
	return r.save(entries)
}

// Last returns up to n of the newest live entries, oldest first. Expired
// entries are purged as a side effect. It is empty when disabled.
func (r *Review) Last(n int) ([]Entry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.Enabled {
		return nil, r.save(nil)
	}
	entries, changed, err := r.load()
	if err != nil {
		return nil, err
	}
	if changed {
		if err := r.save(entries); err != nil {
			return nil, err
		}
	}
	if n > 0 && len(entries) > n {
		entries = entries[len(entries)-n:]
	}
	return entries, nil
}
