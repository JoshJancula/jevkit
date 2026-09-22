# Redaction

jevkit sends text to TypeSafe AI's Jev service (`https://api.typesafe.ai/v1/systemone`) so it can classify and rank lines. Everything that is sent is redacted on your machine first, and if redaction cannot be trusted, nothing is sent. This document explains what that promises, how to tune it, and how to check it.

## Threat model

**What is sent to api.typesafe.ai**

- The already-redacted text of a question: command output, log lines or a request you asked jevkit to classify.
- Your API key, as the credential for the request. It is never part of the payload.

**What is never sent**

- Anything matching a `never_send` pattern (commands and paths). Their output is not redacted and sent; it is skipped entirely.
- Your API key value, private keys, tokens, credential assignments and the rest of the HARD rules below, in any payload.
- The original text. Redaction is line-preserving, so Jev ranks redacted lines and jevkit assembles the final output locally from the original.
- Matched content in the audit log. It stores rule ids and counts only.

**Fail closed.** An invalid config, a failed redaction, or a failed verification pass over the redacted text rejects the send (exit code 3). Hooks then pass the original output through to the agent unchanged, so a redaction problem never blocks your agent and never leaks.

**What redaction cannot do.** It is pattern-based. Sensitive text that matches no rule (a customer name, a proprietary algorithm in prose) is sent as written. Use `literals`, `rules`, `never_send` and `mode: strict` to close gaps you know about, and read the [security checklist](#security-checklist) if you handle regulated data.

## Layered config

Three layers apply, lowest to highest precedence:

1. **Built-ins**, embedded in the binary.
2. **User file**, `<config_dir>/redact.yaml`. It is trusted and must be mode 0600; jevkit refuses a file that other users can read. Create it with `jevkit redact init`.
3. **Project file**, `.jevkit/redact.yaml` in the repository. It is untrusted. Create it with `jevkit redact init --project`.

**Why project config is additive-only.** A project file arrives with a repository you may have just cloned, and anyone who can commit to it could otherwise switch redaction off for everyone who uses the repo. So a project file may only make redaction stricter: add `rules`, `literals`, `env_values` and `never_send` entries, embed `tests`, and set `mode: strict`. It can never `disable` a rule, add an `allowlist`, change `tuning` or `placeholder`, or set any of the review and confirm keys. Naming one of those is an error ("not permitted in a project config"), and the send is rejected.

## HARD and SOFT rules

Run `jevkit redact list` to see every rule with its class and state.

| Class | Rules | Can be disabled or allowlisted? |
| --- | --- | --- |
| HARD | `builtin.known-secret`, `builtin.private-key-block`, `builtin.authorization-header`, `builtin.bearer-token`, `builtin.openai-key`, `builtin.aws-access-key`, `builtin.github-token`, `builtin.slack-token`, `builtin.credential-assignment`, plus every custom rule, literal and `env_values` entry | No, by anyone, in any layer |
| SOFT | `builtin.home-path`, `builtin.user-path`, `builtin.email`, `builtin.ipv4`, `builtin.env-dump`, `builtin.high-entropy` | Yes, in the user file only |

HARD rules protect things that are secrets by definition. SOFT rules protect things that are often sensitive but often harmless (a public address, a git sha that looks random), so you can tune them when they misfire.

## Config keys

Every key is optional except `version`. Patterns use Go's RE2 syntax. The examples below are complete files: `jevkit redact check` accepts each of them.

### version

Always `1`.

### rules

Extra regex rules. They are HARD. Each needs a unique `id` (it may not reuse a built-in id) and a `pattern`. Optional `flags` (any of `i`, `m`, `s`) and `replacement` (used verbatim instead of the placeholder). Matching is per line.

```yaml
version: 1
rules:
  - id: custom.internal-host
    pattern: 'corp-[a-z0-9-]+\.internal'
    replacement: '[INTERNAL-HOST]'
  - id: custom.ticket-key
    pattern: 'acme-\d{3,6}'
    flags: i
```

### literals

Exact strings (4 or more characters) that must never leave the machine. They are HARD. Prefer `env_values` when the secret lives in an environment variable, so the value is not written into a config file.

```yaml
version: 1
literals:
  - acme-prod-db-password
```

### env_values

Environment variable names, with `*` and `?` globs, whose current values are redacted. Variables matching `*KEY*`, `*TOKEN*`, `*SECRET*`, `*PASSWORD*` and `*CREDENTIAL*` are always redacted; list any others.

```yaml
version: 1
env_values:
  - STRIPE_*
  - DB_URL
```

### allowlist

User file only. Stops a SOFT rule from redacting text you know is safe, for example after `jevkit redact test` shows a false positive. Each entry has a `regex` or a `literal`, and optionally the `rule` it applies to (default: every SOFT rule). HARD rules cannot be allowlisted, and `builtin.home-path` takes none (use `disable` or `tuning.path_handling`).

```yaml
version: 1
allowlist:
  - rule: builtin.ipv4
    literal: "10.0.0.1"
  - rule: builtin.high-entropy
    regex: '^[0-9a-f]{40}$'
```

### disable

User file only. Turns SOFT rules off entirely.

```yaml
version: 1
disable:
  - builtin.email
```

### tuning

User file only. `entropy_threshold` (bits per character, 2 to 6; lower redacts more), `min_token_length` (16 to 256) and `path_handling` (`rewrite` turns your home directory into `~`; `keep` leaves paths alone). These tune the SOFT `builtin.high-entropy` and path rules.

```yaml
version: 1
tuning:
  entropy_threshold: 4.3
  min_token_length: 40
  path_handling: rewrite
```

### never_send

Commands and paths whose output is never sent at all. `*` matches any run of characters and `?` one character. Built in: `cat .env*`, `*/secrets/*` and `kubectl get secret*`. This check runs before redaction. Allowed in project files.

```yaml
version: 1
never_send:
  - terraform output*
  - "*/id_rsa"
```

### mode

`standard` (default) or `strict`. Strict redacts every SOFT rule and lowers the entropy threshold, so git shas and similar tokens are redacted too. It conflicts with `disable`, `allowlist` and `path_handling: keep`. A project file may set `strict` but never `standard`, which is how a repository asks for the stricter behaviour for everyone.

```yaml
version: 1
mode: strict
```

### placeholder

`label` (default) replaces a match with `[REDACTED:rule-id]`. `stable` uses a salted hash, so the same secret gets the same placeholder every time. That keeps repeated values distinguishable in ranked output without revealing them. The salt is random per install and never leaves your machine. User file only.

```yaml
version: 1
placeholder: stable
```

### tests

Regression cases run by `jevkit redact check`. Each has an `input` and any of `must_not_contain` and `must_contain`, checked against the redacted output. Allowed in project files.

```yaml
version: 1
rules:
  - id: custom.internal-host
    pattern: 'corp-[a-z0-9-]+\.internal'
tests:
  - name: internal host is hidden
    input: "curl https://corp-build.internal/x"
    must_not_contain: ["corp-build"]
```

### review, confirm, review_max, review_ttl

User file only, and off by default. See [Transparency](#transparency-audit-review-and-confirm).

```yaml
version: 1
review: true
review_max: 20
review_ttl: 24h
confirm: false
```

### A project file

This is what a project file looks like. It only adds and tightens.

```yaml project
version: 1
rules:
  - id: project.internal-host
    pattern: 'corp-[a-z0-9-]+\.internal'
env_values:
  - STRIPE_*
never_send:
  - terraform output*
mode: strict
```

## Tuning workflow

Redaction is local, so tune it without any network use:

1. `jevkit redact list` shows the rules and their state. `jevkit redact explain <rule-id>` shows a rule's pattern and how to tune it.
2. `jevkit redact test [--diff] [file|-]` redacts a file or stdin and prints the result. `--diff` shows what changed. Feed it real output: `some-command 2>&1 | jevkit redact test --diff -`.
3. If something sensitive got through, add a rule, literal or env var (`jevkit redact add --pattern|--literal|--env|--never-send <value>`, with `--project` for the project file). If a harmless value was redacted, add an `allowlist` entry to the user file.
4. Put the case in `tests:` so it stays fixed.
5. `jevkit redact check` validates both files, lints them and runs the embedded tests. Run it after every edit, and in CI for the project file.
6. Once jevkit is in use, `jevkit redact audit` shows which rules fire and how often, and `jevkit redact last` shows exactly what was sent. Rules that fire constantly on harmless text are candidates for an allowlist entry; a rule that never fires may not match your data.

## Transparency: audit, review and confirm

**Audit log.** Every send appends one line to `<state>/jevkit/redaction-audit.jsonl` (mode 0600): timestamp, agent, question set, bytes before and after, and per-rule hit counts. It stores rule ids and counts only, never matched text or the payload. `jevkit redact audit [--since <when>] [--format json]` summarises it; `--since` takes a duration such as `24h`, days such as `7d`, a date, or an RFC 3339 time. The state directory is `$JEVKIT_STATE_DIR`, else `$XDG_STATE_HOME/jevkit`, else `~/.local/state/jevkit`. If the audit line cannot be written, the send is blocked.

**Review mode.** Off by default, because a stored payload is itself sensitive text. Turn it on with `review: true` in the user file or `JEVKIT_REDACT_REVIEW=1`. It keeps the exact already-redacted payload of the last `review_max` sends (default 20) for `review_ttl` (default 24h, at most 30 days), in a 0600 file, purging expired entries automatically. `jevkit redact last [n]` prints them. Turning review off deletes what was stored.

**Confirm mode.** `confirm: true` (or `JEVKIT_REDACT_CONFIRM=1`) makes interactive sends, such as `jevkit key test` and MCP `jev_ask` calls, show the redacted payload and wait for an explicit yes. Anything else, including end of input, aborts before anything is sent or recorded. Hooks never prompt: they must stay non-interactive, so confirm does not apply to them.

The environment variables override the file in both directions and accept `1/true/yes/on` or `0/false/no/off`.

## Security checklist

For anyone handling regulated data (health, financial, personal, export-controlled):

- [ ] Run `jevkit redact init` and keep the user file at mode 0600.
- [ ] Set `mode: strict` in the user file, and in each project's `.jevkit/redact.yaml`.
- [ ] List every environment variable that holds a secret or identifier under `env_values`.
- [ ] Add `rules` for identifiers the built-ins cannot know: patient, account, case and ticket ids, hostnames, customer names.
- [ ] Add `never_send` entries for anything whose output is regulated in full: database dumps, export commands, secret stores.
- [ ] Write `tests:` for each identifier format, and run `jevkit redact check` in CI.
- [ ] Feed real command output to `jevkit redact test --diff` before you rely on the config.
- [ ] Use `placeholder: stable` only if you accept that a repeated placeholder shows a value repeats.
- [ ] Turn on `confirm: true` for interactive use until you trust the rules.
- [ ] Review `jevkit redact audit` periodically. Enable `review` only while investigating, and disable it afterwards, since stored payloads are sensitive.
- [ ] Treat a project file from an untrusted repository as untrusted: it cannot loosen redaction, but read it before you run `jevkit redact check` on it.
- [ ] Check whether your regulations allow the redacted text to leave your environment at all. Redaction reduces exposure; it does not certify it. If the answer is no, add `never_send` entries or do not enable jevkit for that data.
