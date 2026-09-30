package enrollment

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// ParseRuntimeArgs splits user arguments without a shell or any expansion.
// Only invocation controls and conflicts with explicit enforcement are reserved;
// the runtime validates other flags, including flags added in future versions.
func (a Agent) ParseRuntimeArgs(readOnly bool) ([]string, error) {
	args, err := splitRuntimeArgs(a.RuntimeArgs)
	if err != nil {
		return nil, fmt.Errorf("agent %q: runtimeArgs: %w", a.ID, err)
	}
	if len(args) == 0 {
		return nil, nil
	}
	if a.Via != Runtime {
		return nil, fmt.Errorf("agent %q: runtimeArgs requires via: runtime", a.ID)
	}
	if !strings.HasPrefix(args[0], "-") {
		return nil, fmt.Errorf("agent %q: runtimeArgs must start with a CLI option", a.ID)
	}
	reserved := map[string]string{
		"codex":       "--model -m --json --output-last-message -o --output-schema --cd -C --last --all --fork",
		"claude":      "--model --agent -p --print --output-format --input-format --resume -r --continue -c --session-id --fork-session --no-session-persistence --json-schema --background --bg --cloud --teleport --remote --worktree -w --cwd",
		"cursor":      "--model -m -p --print --output-format --resume --continue --workspace --cloud",
		"opencode":    "--model -m --agent --format --session -s --continue -c --fork --command --attach --dir",
		"antigravity": "--model --agent -p --print --output-format --input-format --conversation --workspace --json-schema",
	}
	if _, ok := reserved[a.Runtime]; !ok {
		return nil, fmt.Errorf("agent %q: runtimeArgs is not supported by the %s CLI adapter", a.ID, a.Runtime)
	}
	for _, arg := range args {
		flag, _, _ := strings.Cut(arg, "=")
		if flag == "--" || hasArgumentFlag([]string{flag}, "--help -h --version -v -V "+reserved[a.Runtime]) {
			return nil, fmt.Errorf("agent %q: runtimeArgs cannot override Jevkit invocation control %q; use the corresponding roster field where available", a.ID, flag)
		}
	}
	configFlags := map[string]string{
		"codex":  "--config -c --profile -p",
		"claude": "--settings --setting-sources",
	}
	if (readOnly || a.ReadOnly) && (a.HasRuntimePermissionArgs(args) || hasArgumentFlag(args, configFlags[a.Runtime])) {
		return nil, fmt.Errorf("agent %q: runtimeArgs permission options conflict with Jevkit-enforced read-only execution", a.ID)
	}
	if a.Tools != nil && (disabled(a.Tools.Shell) || disabled(a.Tools.Web) || disabled(a.Tools.Delegate)) {
		conflicts := ""
		switch a.Runtime {
		case "codex":
			// Config overrides are followed by Jevkit's tool overrides. Feature
			// switches are processed separately by Codex, so reserve them.
			conflicts = "--enable --disable"
		case "claude":
			conflicts = "--tools --disallowedTools --disallowed-tools --allowedTools --allowed-tools"
		}
		if hasArgumentFlag(args, conflicts) {
			return nil, fmt.Errorf("agent %q: runtimeArgs tool options conflict with the tools restriction mapping", a.ID)
		}
	}
	return args, nil
}

func disabled(value *bool) bool { return value != nil && !*value }

// HasRuntimePermissionArgs reports an explicit user choice that replaces the
// adapter's default permission mode. Explicit read-only assignments reject it.
func (a Agent) HasRuntimePermissionArgs(args []string) bool {
	flags := map[string]string{
		"codex":       "--sandbox -s --full-auto --dangerously-bypass-approvals-and-sandbox --yolo --ask-for-approval -a",
		"claude":      "--permission-mode --dangerously-skip-permissions",
		"cursor":      "--mode --force -f --yolo --sandbox",
		"antigravity": "--mode --dangerously-skip-permissions --sandbox",
	}
	return hasArgumentFlag(args, flags[a.Runtime])
}

func hasArgumentFlag(args []string, flags string) bool {
	for _, arg := range args {
		flag, _, _ := strings.Cut(arg, "=")
		for _, candidate := range strings.Fields(flags) {
			if flag == candidate || len(candidate) == 2 && strings.HasPrefix(flag, candidate) && !strings.HasPrefix(flag, "--") {
				return true
			}
		}
	}
	return false
}

// RuntimeArgsFingerprint keeps changed invocation options out of existing
// sessions and assignments without treating them as independent quorum votes.
func (a Agent) RuntimeArgsFingerprint() string {
	if strings.TrimSpace(a.RuntimeArgs) == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(a.RuntimeArgs))
	return hex.EncodeToString(digest[:])
}

func splitRuntimeArgs(raw string) ([]string, error) {
	if strings.ContainsRune(raw, 0) {
		return nil, fmt.Errorf("NUL bytes are not allowed")
	}
	var args []string
	var word strings.Builder
	var quote byte
	started := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case quote == '\'':
			if c == quote {
				quote = 0
			} else {
				word.WriteByte(c)
			}
		case quote == '"':
			switch {
			case c == quote:
				quote = 0
			case c == '\\' && i+1 < len(raw) && strings.ContainsRune("\"\\$`", rune(raw[i+1])):
				i++
				word.WriteByte(raw[i])
			default:
				word.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote, started = c, true
		case c == '\\':
			if i+1 == len(raw) {
				return nil, fmt.Errorf("unfinished escape")
			}
			i++
			word.WriteByte(raw[i])
			started = true
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if started {
				args = append(args, word.String())
				word.Reset()
				started = false
			}
		default:
			word.WriteByte(c)
			started = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unclosed quote")
	}
	if started {
		args = append(args, word.String())
	}
	return args, nil
}
