package main

import (
	"time"

	"github.com/OWNER/jevkit/internal/redact/config"
	"github.com/OWNER/jevkit/internal/sdlc/ledger"
)

func (a *App) recordDecision(store *ledger.Store, d ledger.Decision) error {
	d.At = a.now().UTC().Format(time.RFC3339Nano)
	if d.RunID == "" {
		return nil
	}
	// Redact before persistence, including offered rubrics. A redactor failure
	// must stop the decision write rather than save unredacted display text.
	cfg, err := config.Load(a.loadOptions())
	if err != nil {
		return err
	}
	r, err := cfg.Redactor()
	if err != nil {
		return err
	}
	clean := func(s string) (string, error) { v, err := r.Apply(s); return v.Text, err }
	for _, field := range []*string{&d.Trigger, &d.Choice, &d.Outcome, &d.Next, &d.Detail} {
		*field, err = clean(*field)
		if err != nil {
			return err
		}
	}
	for i := range d.Candidates {
		d.Candidates[i].Reason, err = clean(d.Candidates[i].Reason)
		if err != nil {
			return err
		}
	}
	for k, value := range d.Rubrics {
		d.Rubrics[k], err = clean(value)
		if err != nil {
			return err
		}
	}
	return store.AppendDecision(d)
}
