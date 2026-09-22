// Package agents is the jevkit hook framework and per-agent adapters.
//
// `jevkit hook <agent> <event>` reads a JSON payload on stdin, dispatches to
// an Agent adapter, and prints a JSON response. Hooks must never break the
// calling agent: the runner recovers panics, enforces a hard timeout, fails
// open on garbage input or protocol-version mismatch, and exits 0 with a
// valid empty/allow response unless the adapter deliberately denies.
package agents
