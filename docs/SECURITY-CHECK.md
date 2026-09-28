# Command security checks

Jevkit checks supported shell calls before execution in Claude Code, Codex, Cursor, and Antigravity. It also checks SDLC worker working directories and task text. OpenCode shell calls are not policy-checked in this version.

This is a **path and visible-command guard**, not an operating-system sandbox. It cannot see paths built at runtime, hidden inside a program, or introduced by shell expansion.

## Policy

The built-in policy blocks destructive root deletion. A user policy lives at `<config-dir>/security/<name>.yaml`; a project can add killswitch patterns in `.jevkit/security.yaml`. Project files cannot relax mode, scoring, or path allowances.

```yaml
version: 1
killswitch:
  - "rm -rf build/*"
jev_scoring: false
mode: enforce
```

Patterns match whole commands or shell segments; `*` matches any run of characters and `?` one character. Killswitch rules accumulate across built-in, user, and project layers. `mode: shadow` records possible denials without blocking. A policy load error or Jev failure does not make Jev deny a command; local built-in checks still apply.

`jev_scoring: true` lets Jev score a redacted command before execution. It is off by default and the question set is not calibrated yet. Try it in shadow mode first. In enforce mode, only a severe or critical result with enough confidence can deny; a timeout or missing answer cannot.

## Try it locally

```bash
jevkit security init
jevkit security show
jevkit security check "rm -rf /"
jevkit security test
```

Use `jevkit security init --project` to create a project killswitch file. `--security-policy NAME` selects a user policy for one invocation. `--yolo` or `JEVKIT_YOLO=1` lifts the workspace path guard only; killswitch rules and Jev scoring still apply.
