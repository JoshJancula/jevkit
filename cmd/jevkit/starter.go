package main

// starterUser is the commented redact.yaml `redact init` writes for the user
// layer. It must stay valid: a test runs it through `redact check`.
const starterUser = `# jevkit redaction config (version 1).
#
# Everything jevkit sends to the jev service is redacted first. Built-in rules
# already cover API keys, bearer tokens, private keys, credential assignments,
# emails, IPv4 addresses, env dumps, home paths and long random-looking tokens.
# Run "jevkit redact list" to see them and "jevkit redact explain <rule-id>"
# for how to tune one. Run "jevkit redact check" after editing this file.
#
# This is the USER file: it is trusted, so it may loosen SOFT rules. It must
# stay private (mode 0600). A project's .jevkit/redact.yaml is untrusted and
# can only ADD rules; it can never disable or loosen anything.
version: 1

# rules: extra regex rules (Go RE2 syntax). They are HARD: they can never be
# disabled or allowlisted. Each needs a unique lowercase id and a pattern.
# Optional: flags (any of i, m, s) and replacement (used verbatim instead of
# the placeholder). Matching is per line. Example:
#
#   rules:
#     - id: custom.internal-host
#       pattern: 'corp-[a-z0-9-]+\.internal'
#       replacement: '[INTERNAL-HOST]'
#
# Or add one from the shell: jevkit redact add --pattern 'corp-[a-z0-9-]+\.internal'
rules: []

# literals: exact strings that must never leave your machine (4+ characters).
# They are HARD. Example: literals: ["acme-prod-db-password"]
literals: []

# env_values: environment variable NAMES (globs allowed) whose current values
# are redacted. Variables matching *KEY*, *TOKEN*, *SECRET*, *PASSWORD* and
# *CREDENTIAL* are always redacted already; list any others here.
# Example: env_values: ["STRIPE_*", "DB_URL"]
env_values: []

# never_send: commands and paths whose output is never sent at all, and is not
# even redacted. "*" matches any run of characters, "?" one character.
# Built in: "cat .env*", "*/secrets/*", "kubectl get secret*".
# Example: never_send: ["terraform output*", "*/id_rsa"]
never_send: []

# allowlist (USER file only): stop a SOFT rule redacting text you know is safe.
# HARD rules cannot be allowlisted. Each entry names a regex or a literal, and
# optionally the rule it applies to (default: every SOFT rule). Example:
#
#   allowlist:
#     - rule: builtin.ipv4
#       literal: "10.0.0.1"
#     - rule: builtin.high-entropy
#       regex: '^[0-9a-f]{40}$'
#
# disable (USER file only): turn SOFT rules off entirely. Example:
#   disable: ["builtin.email"]
#
# tuning (USER file only): the high-entropy detector and path handling.
#   tuning:
#     entropy_threshold: 4.3   # bits per character, 2 to 6; lower redacts more
#     min_token_length: 40     # 16 to 256
#     path_handling: rewrite   # or "keep" to leave home paths alone
#
# mode: "standard" (default) or "strict". Strict redacts every SOFT rule and
# lowers the entropy threshold, so git shas are redacted too. It conflicts
# with disable, allowlist and path_handling: keep, and a project file may set
# "strict" but never "standard".
#   mode: strict
#
# placeholder: "label" (default, [REDACTED:rule-id]) or "stable" (a salted hash
# so the same secret always gets the same placeholder).
#   placeholder: stable

# tests: regression cases run by "jevkit redact check". Each has an input and
# any of must_not_contain / must_contain (checked against the redacted output).
#
#   tests:
#     - name: internal host is hidden
#       input: "curl https://corp-build.internal/x"
#       must_not_contain: ["corp-build"]
tests: []
`

// starterProject is the project-layer starter (.jevkit/redact.yaml). Project
// files are untrusted and additive only.
const starterProject = `# jevkit project redaction config (version 1).
#
# This file lives in the repository, so jevkit treats it as UNTRUSTED: it can
# only add rules, literals, env_values and never_send entries (and set
# "mode: strict"). It can never disable or loosen redaction; put allowlist,
# disable and tuning in your user file (jevkit redact init).
# Run "jevkit redact check" after editing. Do not commit real secrets here:
# prefer env_values (variable names) over literals.
version: 1

# rules: extra HARD regex rules (Go RE2). Example:
#
#   rules:
#     - id: project.internal-host
#       pattern: 'corp-[a-z0-9-]+\.internal'
rules: []

# literals: exact strings (4+ characters) that must never be sent.
literals: []

# env_values: environment variable names (globs allowed) whose values are
# redacted, e.g. ["STRIPE_*"].
env_values: []

# never_send: commands and paths whose output is never sent, e.g.
# ["terraform output*"].
never_send: []

# mode: strict   # redact every SOFT rule for everyone who uses this repo

# tests: regression cases for "jevkit redact check" (input, must_not_contain,
# must_contain).
tests: []
`
