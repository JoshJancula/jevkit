# jevkit plan

## Context
Extract ralph's jev features into a standalone Go project, `jevkit`, for strict jev use (TypeSafe AI's closed-set "SystemOne" API at api.typesafe.ai/v1/systemone). Jev is a classifier, not an LLM: "compaction" = jev ranks tagged lines, a deterministic assembler builds the output.

Ralph already has the full hook capability, and jevkit ports it. Jev ranked-line compaction is a tier inside the hook-backed compactor (shell-output-compact.py `_compact_jev_ranked`, gated by RALPH_JEV_COMPACT=1), reached from per-agent hook entry points. Ralph implements it in two parallel bash+python implementations (~5,100 lines; needs jq/curl/python3/timeout; no native Windows). jevkit collapses that into one Go codebase and decouples it from ralph.

Target agents: Claude Code, OpenCode, Cursor, Antigravity (Codex optional; ralph already supports it). Copilot and Grok dropped (user has no access).

## Decision: Go, single static binary (CGO_ENABLED=0)
- Zero runtime deps, ~5ms hook startup (hooks fire every tool call), cross-platform incl. Windows, typesafe, one binary = CLI + MCP server + hook handler + exec wrapper.
- Docker is NOT for users (cold-start latency, keychain unreachable from container). Docker IS for contributors: devcontainer + multi-stage Dockerfile + `make build test lint release-snapshot`, same targets in CI.
- Tradeoff accepted: users lose script hackability; mitigated by signed, checksummed releases and a small readable codebase.

## Hook model (ported from ralph)
Two mechanisms, matching ralph:
1. **PostToolUse output replacement** (Claude Code): `jevkit hook claude post-tool` reads the payload and returns hookSpecificOutput.updatedToolOutput. Ref: ralph bundle/.claude/hooks/compact-bash-output.sh; registration template bundle/.ralph/plugin-inputs/templates/claude/hooks.json.
2. **PreToolUse rewrite into a wrapper** (Cursor, Codex, Antigravity, and OpenCode via plugin lifecycle): `jevkit hook <agent> pre-tool` rewrites the shell command to `jevkit exec -- <cmd>`, which runs it, compacts through the pipeline with the REAL exit code (needed for failure-aware ranking and jev preserve-line), and prints the result. Refs: bundle/.cursor/hooks/pre-tool-shell-policy.sh, bundle/.codex/hooks/pre-tool-bash-policy.sh, bundle/.agents/hooks/pre-tool-shell-policy.sh, bundle/.opencode/plugins/ralph-runtime-hooks.ts; wrapper: bundle/.ralph/bash-lib/native-hook/native-shell-wrapper.sh (`ralph_native_shell_compact_pipeline_json` -> `ralph_compact_shell_output`).
Registration refs: bundle/.cursor/hooks.json, bundle/.agents/hooks.json, bundle/.codex/hooks.json.
Pipeline refs: bash-lib/compactors.sh (dispatch), python/shell-output-compact.py (jev tier ~1014+, dispatch when no family matched ~329-341, registry FAMILY_JEV_RANKED ~3090-3094). Behavior: collapse repeats, tag lines L000.. (255 window cap), ask compaction.line-relevance, assemble head/ranked/tail under byte budget, safety gate + preserve-line round trip, abort => deterministic path. Source-output families (git diff/show/log, grep, rg, find, ls, tree) hard passthrough.
Hooks never break the agent: recover panics, hard timeout, fail-open passthrough, compaction only above size threshold, version handshake with fail-open on mismatch. Default SHADOW mode (question sets are "placeholder - uncalibrated"); report measured savings. Phase 0 verifies the exact payload fields per agent (e.g. whether Claude's PostToolUse payload carries a real exit code) against ralph's fixtures and docs (bundle/.ralph/docs/HOOKS.md, TOOLING.md).

## Distribution
GitHub Releases is source of truth. GoReleaser: darwin/linux/windows x amd64/arm64, checksums, cosign keyless signing, SBOM, build attestations.
Channels: install.sh (curl|sh, sha256 verified, ~/.local/bin, no sudo); Homebrew tap + Scoop (winget later); npm `jevkit` w/ per-platform optional deps (esbuild pattern, for `npx` in plugin configs); `go install`; ghcr.io distroless image (CI only).
`jevkit upgrade` verifies checksum+signature. macOS notarization deferred.

## Release automation (GitHub Actions, on merge to main) + changelog
- Conventional Commits enforced on PR titles (squash merge) via a PR-title check.
- `ci.yml`: PR + push to main; devcontainer `make lint test` plus OS matrix (ubuntu/macos/windows) for keychain and install paths.
- `release.yml`: on push to main after CI passes:
  1. Compute next semver from conventional commits (git-cliff `--bumped-version`): feat -> minor, fix/perf -> patch, `!`/BREAKING -> major, chore/docs/ci/test -> no release.
  2. Regenerate CHANGELOG.md with git-cliff, commit back as bot with `[skip ci]`, tag `vX.Y.Z`. Needs a GitHub App token or bypass rule since main is protected.
  3. GoReleaser publishes the GitHub Release (notes = that version's changelog section), brew/scoop/npm/ghcr, signatures, attestations.
- Idempotent: no releasable commits => exit cleanly. Version embedded via ldflags.

## Layout
cmd/jevkit; internal/{jev,redact,keystore,breaker,registry,usage,compact,mcp,exec,agents/<agent>}.
- jev: typed client, generic question types, strict decoding. Spec refs: ralph python/jev_client.py, bash-lib/jev/jev-client.sh, jev-policy.sh, jev-redact.sh, jev/questions.registry.json (go:embed), schemas/jev-question-set.schema.json, tests/fixtures/jev/.
- keystore: env > .env (parsed, never sourced) > command (context timeout) > keychain (go-keyring) > 0600 file. Clean JEVKIT_* namespace + one-time `jevkit migrate-from-ralph`. Ref: bash-lib/jev/jev-key-store.sh.
- usage: append-only ~/.local/state/jevkit/usage.jsonl with file locking; `jevkit usage`. Ref: python/jev_usage.py.
- mcp: official Go MCP SDK, stdio; tools jev_classify_request, jev_classify_failure, jev_rank_relevance, jev_ask. Ref: jev-mcp-server.sh (never emit null nextCursor; unavailable = successful envelope with available:false).
- install: `jevkit install <agent>` idempotent marker-based edits, --dry-run, uninstall; native plugin packages (ref: bundle/.ralph/plugin-inputs/, bash-lib/plugin/plugin-adapter-*.sh).

## Phases
0 Spike: port hook payload formats + fixtures from ralph, capability matrix. 1 Core (client, redact, keystore, usage, CLI key/usage/doctor). 2 MCP. 3 Compaction pipeline + `jevkit exec` + Claude hook (shadow). 4 Remaining agent hooks + installer + plugins. 5 CI + release automation + changelog. 6 Distribution channels.

## Redaction (user-tunable, fail-closed)
Nothing reaches api.typesafe.ai unredacted. Layers: embedded built-ins (HARD rules cannot be disabled or allowlisted by anyone; SOFT rules are tunable) < user config `<config_dir>/redact.yaml` (trusted; may add rules, literals, env_values, allowlist for soft rules, disable soft rules, tuning, never_send, placeholder style) < project config `.jevkit/redact.yaml` (untrusted; additive-only, can never loosen). Invalid config, failed redaction or a failed post-redaction verification pass rejects the send (code 3) and hooks pass output through. Redaction is line-preserving so ranked line tags stay aligned; Jev only ever sees redacted text and the final output is assembled locally from the original. Tooling: `jevkit redact init|list|explain|test|add|check|audit|last`; audit log stores rule hit counts only; opt-in review mode stores the exact redacted payload for inspection. Stable placeholders use a per-install HMAC salt. All patterns are RE2.

## Testing policy
Unit tests only: Go table tests with fakes and fixtures, in-process hook/MCP/CLI handlers, temp-dir installer tests. No integration, end-to-end or live per-runtime tests; agents are never launched in tests. CI still runs the OS matrix (ubuntu/macos/windows) for the same unit tests.

## Verification
Unit tests per package (`make test`, `go test -race`), lint (`make lint`), release dry-run (`goreleaser --snapshot`, git-cliff preview), and the release workflow logic checked with a scratch repo (feat -> minor release + CHANGELOG commit + tag; docs-only -> no release; re-run -> no duplicate).
