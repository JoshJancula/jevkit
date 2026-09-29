package app

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/JoshJancula/jevkit/internal/jev"
	"github.com/JoshJancula/jevkit/internal/redact"
)

// readInputSource reads path (or stdin, for "-"), bounded by maxTestInput.
func ReadInputSource(a *App, path string) (string, error) {
	in := a.Stdin
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return "", Failf("could not read %s: %v", path, err)
		}
		defer func() { _ = f.Close() }()
		in = f
	}
	data, err := io.ReadAll(io.LimitReader(in, MaxTestInput+1))
	if err != nil {
		return "", Failf("could not read input: %v", err)
	}
	if len(data) > MaxTestInput {
		return "", Failf("input larger than %d bytes", MaxTestInput)
	}
	return string(data), nil
}

// redactText redacts a State or Instructions value: literal JSON text (the
// same shapes wireText treats as literal) is redacted recursively while
// preserving its JSON shape; anything else is redacted as a plain string,
// matching how the value will actually reach the wire.
func RedactText(r *redact.Redactor, s string) (string, error) {
	if jev.IsLiteralJSON(s) {
		out, err := RedactRawJSON(r, json.RawMessage(strings.TrimSpace(s)))
		if err != nil {
			return "", err
		}
		return string(out), nil
	}
	res, err := r.Apply(s)
	if err != nil {
		return "", err
	}
	return res.Text, nil
}

func Truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func OrUnknown(s string) string {
	if s == "" {
		return "no reason recorded"
	}
	return s
}

func UsageCount(known int64, unknown int) string {
	if unknown > 0 {
		return fmt.Sprintf("%s (%d unknown)", FormatInt(known), unknown)
	}
	return FormatInt(known)
}

func FormatInt(n int64) string {
	digits := strconv.FormatInt(n, 10)
	start := 0
	if digits[0] == '-' {
		start = 1
	}
	groups := (len(digits) - start - 1) / 3
	if groups == 0 {
		return digits
	}

	formatted := make([]byte, 0, len(digits)+groups)
	formatted = append(formatted, digits[:start]...)
	for i := start; i < len(digits); i++ {
		if i > start && (len(digits)-i)%3 == 0 {
			formatted = append(formatted, ',')
		}
		formatted = append(formatted, digits[i])
	}
	return string(formatted)
}

// maxTestInput bounds what `redact test` reads.
const MaxTestInput = 8 << 20

func RedactRawJSON(r *redact.Redactor, raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return raw, nil
	}
	out, _, err := r.ApplyJSON(raw)
	if err != nil {
		return nil, err
	}
	return out, nil
}
