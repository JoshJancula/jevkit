package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
)

func (a *App) LookPathFunc() func(string) (string, error) {
	if a.LookPath != nil {
		return a.LookPath
	}
	return exec.LookPath
}

func (a *App) UserHome() string {
	if a.HomeDir != "" {
		return a.HomeDir
	}
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return ""
}

func (a *App) ResolveBinary(flag string) string {
	if flag != "" {
		return flag
	}
	if a.Binary != "" {
		return a.Binary
	}
	if exe, err := os.Executable(); err == nil {
		if abs, err := filepath.Abs(exe); err == nil {
			return abs
		}
		return exe
	}
	return "jevkit"
}

// stateHome is the single resolver for the Jevkit state root, independent of
// how a.StateDir was set: the default (main.go's defaultStateDir, itself
// XDG/Windows/JEVKIT_STATE_DIR-aware) already ends in "jevkit", while an
// explicit JEVKIT_STATE_DIR override may or may not. Every caller below
// joins "jevkit" back on (or delegates to a package like breaker.New,
// usage.Path, or registry.DecisionsPath that does), so stateHome() always
// returns the same directory a "jevkit" segment is about to be appended to.
// Ownership under <stateHome()>/jevkit/:
//   - sdlc/runs/<runID>/run.json, nodes/, artifacts/, events.jsonl,
//     decisions.jsonl, logs/ — internal/sdlc/ledger, one directory per run
//     (see sdlcRunsDir).
//   - usage.jsonl — internal/usage, one shared append-only log for every run
//     and every Jev call (see usage.Path).
//   - decisions.jsonl (top-level, not per-run) — internal/registry, routing
//     decisions outside any run (see registry.DecisionsPath).
//   - breaker.json — internal/breaker, one shared circuit-breaker state.
//   - redaction-audit.jsonl, redaction-review.json — internal/redact/audit,
//     shared audit/review records (see auditPath, reviewPath).
//   - jev-ask-audit.jsonl — internal/mcp, shared MCP audit trail.
//
// External runtime sessions (Claude/Codex/Cursor/OpenCode/Antigravity
// session IDs used to resume a conversation) are not files under this root
// at all: they live in the owning CLI's own native session store and are
// only referenced by ID in ledger.Run.Sessions.
func (a *App) StateHome() string {
	if filepath.Base(a.StateDir) == "jevkit" {
		return filepath.Dir(a.StateDir)
	}
	return a.StateDir
}

// sdlcRunsDir is where run ledgers live under StateDir.
func (a *App) SDLCRunsDir() string { return filepath.Join(a.StateHome(), "jevkit", "sdlc", "runs") }

var RunIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
