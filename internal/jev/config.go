package jev

import (
	"strconv"
	"time"
)

const (
	DefaultEndpoint   = "https://api.typesafe.ai/v1/systemone"
	DefaultModel      = "jev-latest"
	DefaultTimeout    = 4000 * time.Millisecond
	DefaultMaxRetries = 2

	// MaxTokens is the request budget; BytesPerToken is the deliberately
	// conservative estimate (jev is unreliable at counting itself).
	MaxTokens     = 32000
	BytesPerToken = 4
	budgetBytes   = MaxTokens * BytesPerToken

	TransportFixture = "fixture"
)

// Config holds client settings, normally read from JEVKIT_* variables.
type Config struct {
	Endpoint   string
	Model      string
	Timeout    time.Duration
	MaxRetries int
	// Transport is "" (live HTTPS) or TransportFixture.
	Transport  string
	FixtureDir string
}

// ConfigFromEnv builds a Config from getenv (os.Getenv in production):
// JEVKIT_ENDPOINT, JEVKIT_MODEL, JEVKIT_TIMEOUT_MS, JEVKIT_MAX_RETRIES,
// JEVKIT_TRANSPORT and JEVKIT_FIXTURE_DIR.
func ConfigFromEnv(getenv func(string) string) Config {
	str := func(name, def string) string {
		if v := getenv(name); v != "" {
			return v
		}
		return def
	}
	num := func(name string, def int) int {
		if n, err := strconv.Atoi(getenv(name)); err == nil && n >= 0 {
			return n
		}
		return def
	}
	timeout := time.Duration(num("JEVKIT_TIMEOUT_MS", int(DefaultTimeout/time.Millisecond))) * time.Millisecond
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return Config{
		Endpoint:   str("JEVKIT_ENDPOINT", DefaultEndpoint),
		Model:      str("JEVKIT_MODEL", DefaultModel),
		Timeout:    timeout,
		MaxRetries: num("JEVKIT_MAX_RETRIES", DefaultMaxRetries),
		Transport:  getenv("JEVKIT_TRANSPORT"),
		FixtureDir: str("JEVKIT_FIXTURE_DIR", "testdata/jev"),
	}
}
