package worker

import "strings"

// toolArgs uses per-invocation controls, never shared runtime settings files.
// Native definitions are deliberately excluded by ValidateTools.
func toolArgs(req Request) []string {
	p := req.Agent.Tools
	if p.Empty() {
		return nil
	}
	var args []string
	switch req.Agent.Runtime {
	case "codex":
		if p.Shell != nil && !*p.Shell {
			args = append(args, "-c", "features.shell_tool=false")
		}
		if p.Web != nil && !*p.Web {
			args = append(args, "-c", `web_search="disabled"`)
		}
		if p.Delegate != nil && !*p.Delegate {
			args = append(args, "-c", "features.multi_agent=false")
		}
		if len(args) > 0 {
			// Unknown controls must fail instead of becoming ignored config
			// on an older Codex installation.
			args = append(args, "--strict-config")
		}
	case "claude":
		var deny []string
		for _, group := range []struct {
			setting *bool
			names   []string
		}{
			{p.Shell, []string{"Bash", "PowerShell"}},
			{p.Web, []string{"WebSearch", "WebFetch"}},
			{p.Delegate, []string{"Agent", "Task"}},
		} {
			if group.setting == nil || *group.setting {
				continue
			}
			deny = append(deny, group.names...)
		}
		if len(deny) > 0 {
			args = append(args, "--disallowedTools", strings.Join(deny, ","))
		}
	}
	return args
}
