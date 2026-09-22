package config

import "context"

// Send delivers already-redacted text to Jev and returns its answer.
type Send func(ctx context.Context, redacted string) (string, error)

// Gate runs never_send matching, then redaction, then send. subject is the
// command line or path that produced text. When subject matches a never_send
// pattern Gate returns sent=false and the matching pattern without redacting
// text or calling send: the output never leaves the machine. Any redaction
// failure returns its error and send is not called.
func (c *Config) Gate(ctx context.Context, subject, text string, send Send) (out, pattern string, sent bool, err error) {
	if p, ok := c.NeverSend.Match(subject); ok {
		return "", p, false, nil
	}
	r, err := c.Redactor()
	if err != nil {
		return "", "", false, err
	}
	res, err := r.Apply(text)
	if err != nil {
		return "", "", false, err
	}
	out, err = send(ctx, res.Text)
	return out, "", err == nil, err
}
