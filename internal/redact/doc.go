// Package redact is the built-in, fail-closed redaction engine that runs
// before any text is sent to Jev.
//
// Every rule has a stable id (for example builtin.aws-access-key) and a class.
// HARD rules cannot be disabled or allowlisted by anyone. SOFT rules can be
// disabled or given allowlist patterns through Options.
//
// Redaction is line-preserving: newlines are never added, dropped or merged, so
// L000.. line tags stay aligned. After redaction a verification pass checks
// that no known secret value survives. If redaction errors or verification
// fails, Apply returns a *jev.Error with jev.CodeRejected so the caller rejects
// the input before touching the network.
//
// All patterns use Go RE2, so matching is linear-time (no ReDoS). Results
// report which rules fired and how often, never the matched content.
package redact
