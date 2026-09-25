# Security check

Jevkit checks supported shell calls before execution. Claude Code, Codex, Cursor, and Antigravity hooks can deny a command. Codex, Cursor, and Antigravity also recheck local rules in the shell wrapper. SDLC worker invocations check their working directory and task text before starting an agent.

This is a path-reference guard, not an operating-system sandbox. It inspects the working directory and visible path text; shell expansion, programs that open paths internally, and paths constructed at runtime are outside its reach. OpenCode shell calls are not policy-checked in this version.

## Policy

The embedded builtin policy blocks destructive root deletion. Named user policies live at <config_dir>/security/<name>.yaml; <config_dir>/security/default selects one. A project may add killswitch patterns in .jevkit/security.yaml. Project files cannot change mode, scoring, or sandbox allowances.

    version: 1
    killswitch:
      - "rm -rf build/*"
    jev_scoring: false
    mode: enforce
    sandbox:
      allow_read:
        - /path/to/reference-material
    tests:
      - command: "rm -rf build/output"
        deny: true

Patterns use * for any run of characters and ? for one character. They match whole commands and shell segments. Killswitch entries from the embedded, user, and project layers accumulate. The builtin read allowances include ~/.claude, ~/.codex, ~/.cursor, ~/.opencode, and Jevkit's config and state directories. SDLC runs add the exact input paths supplied through --task-file and --file to that run's read allowance.

Mode shadow records would-have denials in <state_dir>/jevkit/security-shadow.jsonl without blocking. Invalid local policy files cause hooks to use the embedded policy and record a load error in hook telemetry; jevkit security commands report the error and exit nonzero. A Jev error or timeout never denies a command. Killswitch and path checks still run.

## Jev scoring

`jev_scoring` enables the optional `security.command-risk` question set for named user policies. It sends a redacted command to Jev and asks for a score from low (0) through moderate (1), high (2), severe (3), and critical (4). It is off in builtin defaults because the Jev request adds latency. Enable it with `jev_scoring: true` in a user policy, or set `JEVKIT_SECURITY_SCORING=1` after policy loading; `JEVKIT_SECURITY_SCORING=0|1` overrides the loaded policy.

Scoring has a two-second deadline. In enforce mode it can deny only when the score is severe or critical (3 or higher), the registry confidence decision is `Act`, and the decision is not shadowed. Jev errors, timeouts, missing answers, and other scoring failures fail open and do not deny; the killswitch and path guard still apply. `JEVKIT_SHADOW=1` or `mode: shadow` prevents a registry `Act` from blocking and records the would-have decision. The question set is a placeholder and uncalibrated, so use shadow mode while evaluating it.

Project `.jevkit/security.yaml` files are killswitch-only and cannot set `jev_scoring` (or mode, sandbox, or tests). `--yolo` and `JEVKIT_YOLO=1` lift only the path guard; they do not disable the killswitch or Jev scoring.

## Commands

    jevkit security init
    jevkit security init --project
    jevkit security list
    jevkit security use local
    jevkit security show [name]
    jevkit security add --killswitch "rm -rf build/*" --policy local
    jevkit security remove --killswitch "rm -rf build/*" --policy local
    jevkit security check "rm -rf /"
    jevkit security test

Security init creates local.yaml and keeps builtin as the default. --security-policy <name> or JEVKIT_SECURITY_POLICY selects a named policy for one invocation.

--yolo or JEVKIT_YOLO=1 lifts the workspace path guard for that invocation. It does not disable the killswitch or Jev scoring. The shell wrapper receives the flag as JEVKIT_YOLO=1 because it runs in another process.

## Verify

Run go build ./... and go test ./..., then use security check with a blocked command and a routine command. Set JEVKIT_TRANSPORT=fixture and JEVKIT_SECURITY_SCORING=1 to exercise the offline scoring path with the security.command-risk fixture.
