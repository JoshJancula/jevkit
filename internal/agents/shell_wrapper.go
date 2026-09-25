package agents

import (
	"strings"

	"github.com/OWNER/jevkit/internal/security/config"
)

// BuildShellWrapperCommand returns a shell-safe invocation of the private
// wrapper. The wrapper is invoked by the agent, rather than the hook, so its
// stdout is the tool result the agent sees.
func BuildShellWrapperCommand(binary, workspace, command, runtime string) string {
	if strings.TrimSpace(binary) == "" {
		binary = "jevkit"
	}
	return shellQuote(binary) + " _runtime shell-wrapper" +
		" --workspace " + shellQuote(workspace) +
		" --runtime " + shellQuote(runtime) +
		" --command " + shellQuote(command)
}

// buildSecurityShellWrapperCommand carries --yolo across the separate wrapper
// process through the environment, while leaving its workspace argument intact.
func buildSecurityShellWrapperCommand(binary, workspace, command, runtime string, yolo bool, policy string) string {
	base := BuildShellWrapperCommand(binary, workspace, command, runtime)
	if policy != "" {
		base = "JEVKIT_SECURITY_POLICY=" + shellQuote(policy) + " " + base
	}
	if yolo {
		base = "JEVKIT_YOLO=1 " + base
	}
	return base
}

func securityPolicyName(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return cfg.Name
}

func IsShellWrapperCommand(command string) bool {
	return strings.Contains(command, "_runtime shell-wrapper")
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
