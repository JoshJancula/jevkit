# Redaction

Jevkit applies local redaction before sending text to TypeSafe AI's Jev service. The API key is sent as an HTTP credential, outside the text being classified. Jevkit keeps the original tool output locally when it compacts an agent result.

Redaction is **pattern based**. A secret or private detail that matches no rule may still be sent. An invalid configuration or failed verification blocks the API request; agent hooks then pass the original output back to the agent. Check real examples before enabling Jevkit for sensitive work.

## Check your output

```bash
jevkit redact list                    # built-in and custom rules
jevkit redact test --diff output.txt  # preview what changes; no API call
jevkit redact check                   # validate config and its tests
jevkit redact audit                   # see rule hit counts, not payloads
```

You can also pipe output to `jevkit redact test --diff -`. Add a rule when sensitive text survives, then use `jevkit redact check` to validate it.

## Add local rules

`jevkit redact init` creates a private user config. `jevkit redact init --project` creates `.jevkit/redact.yaml` in the current repository. A project file can only make redaction stricter; it cannot turn off the built-in protections.

```yaml
version: 1
mode: strict
rules:
  - id: project.internal-host
    pattern: 'corp-[a-z0-9-]+\.internal'
never_send:
  - terraform output*
```

`never_send` skips matching command or path output entirely. Use it for data that should not be sent even after redaction. For a one-off question, `jevkit redact last` shows the last redacted payload only if you have enabled review mode; review mode is off by default because it stores that payload locally.

The [redaction reference](REDACTION-REFERENCE.md) documents all rule types, configuration layers, review mode, and a [checklist for regulated data](REDACTION-REFERENCE.md#security-checklist).
