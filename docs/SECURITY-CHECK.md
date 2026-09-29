# Command security checks

Jevkit checks supported shell calls before execution in Claude Code, Codex, Cursor, and Antigravity. It also checks SDLC worker working directories and task text. OpenCode shell calls are not policy-checked in this version.

This is a **path and visible-command guard**, not an operating-system sandbox. It cannot see paths built at runtime, hidden inside a program, or introduced by shell expansion.

## Policy

The built-in policy blocks destructive root deletion and agent calls to `jevkit security review*`. A user policy lives at `<config-dir>/security/<name>.yaml`; a project can add killswitch patterns and tighten injection settings in `.jevkit/security.yaml`. Project files cannot relax command mode, scoring, or path allowances.

```yaml
version: 1
killswitch:
  - "rm -rf build/*"
jev_scoring: false
mode: enforce
```

Patterns match whole commands or shell segments; `*` matches any run of characters and `?` one character. Killswitch rules accumulate across built-in, user, and project layers. `mode: shadow` records possible denials without blocking. A policy load error or Jev failure does not make Jev deny a command; local built-in checks still apply.

`jev_scoring: true` lets Jev score a redacted command before execution. It is off by default and the question set is not calibrated yet. Try it in shadow mode first. In enforce mode, only a severe or critical result with enough confidence can deny; a timeout or missing answer cannot.

## SDLC runtime boundaries

SDLC assignments request read-only or writable execution per role. Jevkit only runs a restriction when the CLI adapter can enforce it; otherwise it **fails closed** instead of pretending. Inspect the tested matrix with:

```bash
jevkit sdlc agents capabilities
```

In short: Codex can enforce a real sandbox mode; Claude and Cursor use prompting modes (`plan` / `ask`); OpenCode has no read-only flag and is refused for read-only roles; Antigravity's writable path passes `--dangerously-skip-permissions` (named explicitly in the matrix). Planner argv checks also require `jevkit sdlc resume RUN_ID --authorize-checks` before the supervisor runs them. Details stay in the [SDLC reference](SDLC-REFERENCE.md) and [agent reference](AGENT-REFERENCE.md).

## Try it locally

```bash
jevkit security init
jevkit security show
jevkit security check "rm -rf /"
jevkit security test
```

Use `jevkit security init --project` to create a project killswitch file. `--security-policy NAME` selects a user policy for one invocation. `--yolo` or `JEVKIT_YOLO=1` lifts the workspace path guard only; killswitch rules and Jev scoring still apply.

## Prompt-injection review

Opt in with `jevkit install claude --injection-guard`, `JEVKIT_INJECTION_GUARD=1`, or an `injection` policy block. The question set is **uncalibrated**. Try `mode: shadow` first and inspect `<state>/jevkit/security-shadow.jsonl` before enabling enforce mode. In enforce mode, Jev scores redacted, bounded suspicious tool output. A score of at least 3 with an Act or Escalate confidence decision creates a review. `halt_on: act` requires Act. `heuristic_halt: true` also halts on Unicode tag characters and chat-template tokens. Jev errors and timeouts do not create a Jev-based halt.

```yaml
version: 1
injection:
  mode: shadow              # off, shadow, enforce
  scan: suspicious          # or all
  max_bytes: 16384
  halt_on: escalate         # or act
  heuristic_halt: false
```

On a halt, Jevkit saves the original tool result locally, withholds it from the model where the runtime supports replacement, stops the turn where supported, and denies later tool calls in that session. `JEVKIT_YOLO` cannot release the latch. Run `jevkit security reviews`, then `jevkit security review ID` in a terminal. The review command shows metadata and a redacted excerpt; `--show-raw` pages through the local original without sending it to Jev. Choose **allow** to release the latch and allowlist the exact content hash, or **deny** to release the latch while keeping that output withheld. The review command requires an interactive TTY; `--yes` only skips its second confirmation on a TTY.

Claude uses `continue:false` and Bash/MCP output replacement where available; its other tools use the stop response and pre-tool latch. Codex and Cursor shell calls use the wrapper to withhold output and retain the exit status. Codex also emits `continue:false` after a pending review. Cursor MCP results are replaced and its pre-tool hook denies later calls. Antigravity applies the latch at its next shell call. OpenCode has no injection guard. Host behavior can change across runtime versions; validate the stop and replacement combination in a scratch session before relying on it.
