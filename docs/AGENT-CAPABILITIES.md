# Agent hook capabilities (Phase 0 spike)

Source: the ralph repo's shipped hook adapters (`bundle/.claude/hooks/*.sh`,
`bundle/.cursor/hooks*`, `bundle/.agents/hooks*`, `bundle/.codex/hooks*`,
`bundle/.opencode/plugins/`, `bundle/.ralph/docs/HOOKS.md`, `TOOLING.md`) and
ralph's own spike notes recorded in those files. Fixtures live in
`testdata/hooks/<agent>/`.

**Provenance caveat.** Claude Code's PostToolUse payload is a sanitized capture
from ralph (`tests/fixtures/native-hook/bash.json`). The other payloads were
reconstructed from the fields those adapters read and emit, with the
version-pinned findings ralph recorded; they are not fresh captures. Re-verify
against a live agent before relying on any field marked *unverified*.

## Summary

| Agent | Config file | Output replacement | PreToolUse rewrite to `jevkit exec` | Real exit code in payload |
|---|---|---|---|---|
| Claude Code | `.claude/settings.json` (hooks) | **Yes**, success only (`updatedToolOutput`) | Yes (`updatedInput.command`) | **No** |
| Cursor | `.cursor/hooks.json` | **No** (`updated_tool_output` not agent-visible) | Yes (`updated_input.command`) | **No** |
| Antigravity (agy) | `.agents/hooks.json` | No PostToolUse content at all | Yes (`overwrite.CommandLine`) | **No** |
| OpenCode | `.opencode/plugins/*.ts` | Unproven (mutation may not reach model) | Yes (`output.args.command`) but headless firing unproven | **No** |
| Codex | `.codex/hooks.json` | Unproven | Yes (`updatedInput.command`) | **No** |

**Conclusion for jevkit:** PreToolUse rewrite to `jevkit exec -- <cmd>` is the
only mechanism that works on every agent. The wrapper runs the command,
sees the real exit code itself, compacts, and prints the result. Output
replacement is a Claude-only optimisation, and it can't see failures.

No agent supplies an exit code in PostToolUse, which is a strong reason for
the wrapper design.

## Claude Code

- Events used: `PreToolUse`, `PostToolUse`, `PostToolUseFailure`, `Stop`.
  Matcher `Bash`.
- Config: `.claude/settings.json` `hooks` block; command hooks receive JSON on stdin.
- PostToolUse fields: `session_id`, `transcript_path`, `cwd`, `permission_mode`,
  `hook_event_name`, `tool_name`, `tool_input.command`,
  `tool_response.{stdout,stderr,interrupted,isImage,noOutputExpected}`,
  `tool_use_id`, `duration_ms`. **No exit code**; stderr is often merged into
  stdout.
- Non-zero exits fire `PostToolUseFailure` with an `error` string
  (`Exit code N ...`) and **no `tool_response`**, so failures can't be
  compacted this way.
- Response: `hookSpecificOutput.updatedToolOutput` with the full Bash shape
  replaces output; missing required fields are ignored. Success only.
- PreToolUse response: `hookSpecificOutput.updatedInput` with
  `permissionDecision: "allow"`.
- Fixtures: `posttooluse-bash.json`, `posttooluse-bash-response.json`,
  `pretooluse-bash.json`, `pretooluse-bash-response.json`,
  `posttoolusefailure-bash.json`.

## Cursor

- Events: `preToolUse`, `postToolUse`, `afterShellExecution`, `stop`.
  Matcher `Shell` (also `MCP:*` and read/grep/glob names).
- Config: `.cursor/hooks.json` (`version: 1`).
- preToolUse fields: `tool_name`, `tool_input.command`, `workspace_roots[]`, `cwd`.
  Response `{permission, updated_input:{command}}`; rewrite is agent-visible
  (proven on Cursor Agent 2026.06.03).
- postToolUse fields: `tool_input.command`, `tool_output.{stdout,stderr}`.
  `updated_tool_output` is **not** agent-visible (headless), so observability only.
- afterShellExecution: `command`, `output`, `duration` (ms). No exit code.
- Fixtures: `pretooluse-shell*.json`, `posttooluse-shell.json`,
  `aftershellexecution.json`.

## Antigravity (agy)

- Events: `PreToolUse` (matcher `run_command`), `Stop`. No useful PostToolUse.
- Config: `.agents/hooks.json` under a `ralph-native` group key
  (`PreToolUse[].{matcher,hooks[].{command,timeout}}`).
- Payload is camelCase: `toolCall.name`, `toolCall.args.CommandLine`,
  `workspacePaths[]`. Cursor-style keys are not accepted.
- Response: `{decision:"allow"|"deny", overwrite:{CommandLine}}`. A hook
  must always print a decision; empty output is not treated as allow by ralph's
  adapter (it uses an EXIT trap).
- PostToolUse carries only `stepIdx` and `error`: no command, output, duration or
  exit code.
- Fixtures: `pretooluse-run-command*.json`, `posttooluse.json`.

## OpenCode

- Not a stdin/stdout hook: a TypeScript plugin in `.opencode/plugins/`
  (`.js`/`.ts` auto-discovered; `.mjs` is not) exporting
  `tool.execute.before(input, output)` and `tool.execute.after(input, output)`.
- before: `input.tool`, `output.args.command` is mutable, which gives the
  rewrite-to-`jevkit exec` path.
- after: `input.{tool,sessionID,callID,args}`; `output.{title,output,metadata}`.
  Bash output is a **single string** (no stdout/stderr split), no duration
  (pair via `callID`), **no exit code**.
- Ralph's spike (OpenCode 1.14.35): hooks not proven to fire on headless
  `opencode run`; mutating `output.output` not proven to reach the model
  (upstream issues 13573/13575). Treat as *unverified*.
- Fixtures: `tool-execute-before.json`, `tool-execute-after.json`
  (JSON encodings of the function arguments).

## Codex

- Events: `PreToolUse`, `PostToolUse`, `Stop`; matchers `Bash` and
  `command_execution` (plus `read_file`, `grep`, `Glob` for results).
- Config: `.codex/hooks.json` (`hooks.<Event>[].{matcher,hooks[].{type:"command",command,timeout}}`).
- Payload keys: `cwd`, `hook_event_name`, `model`, `permission_mode`,
  `session_id`, `tool_input`, `tool_name`, `tool_response`, `tool_use_id`,
  `transcript_path`, `turn_id`. No `duration_ms`, **no exit code** (the exact
  `tool_response` shape is *unverified*; ralph reads `.stdout`/`.stderr`).
- Response: `hookSpecificOutput.{hookEventName, permissionDecision, updatedInput.command}`
  (proven on Codex CLI 0.136.0). PostToolUse output replacement not proven.
- Fixtures: `pretooluse-bash*.json`, `posttooluse-bash.json`.
