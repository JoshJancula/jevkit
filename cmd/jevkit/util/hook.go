package util

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/agents"
	"github.com/JoshJancula/jevkit/internal/registry"
	securityconfig "github.com/JoshJancula/jevkit/internal/security/config"
)

// runtimeCmd is the private, versioned stdin hook protocol used only by
// generated runtime integrations. It is deliberately hidden from normal help
// and completions; people use `jevkit install`, `mcp`, and `ask` instead.
func (a *App) runtimeCmd() *cobra.Command {
	var protocol int
	dispatch := &cobra.Command{
		Use:    "dispatch <agent> <event>",
		Hidden: true,
		Short:  "private runtime integration dispatcher",
		Args:   cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if protocol != agents.ProtocolVersion {
				// Installed integrations must never block their host because a
				// binary and asset version briefly disagree. The adapter runner
				// still supplies a valid host-specific passthrough response.
				return app.CodeErr(a.RuntimePassthrough(args[0], args[1]))
			}
			return app.CodeErr(a.Hook(cmd.Context(), args[0], args[1]))
		},
	}
	dispatch.Flags().IntVar(&protocol, "protocol", 0, "private protocol version")
	runtime := &cobra.Command{
		Use:    "_runtime",
		Hidden: true,
	}
	runtime.AddCommand(dispatch)
	runtime.AddCommand(a.shellWrapperCmd())
	return runtime
}

func (a *App) RuntimePassthrough(agentName, eventName string) int {
	event, ok := parseHookEvent(eventName)
	if !ok {
		event = agents.Event(eventName)
	}
	if agent := agents.Lookup(agentName); agent != nil {
		a.Outf("%s\n", agent.Passthrough(event))
	} else {
		a.Outf("{}\n")
	}
	return app.ExitOK
}

func (a *App) Hook(ctx context.Context, agentName, eventName string) int {
	if a.Getenv("JEVKIT_SDLC_RUN_ID") != "" && a.Getenv("JEVKIT_SDLC_HOOKS") == "0" {
		return a.RuntimePassthrough(agentName, eventName)
	}
	event, ok := parseHookEvent(eventName)
	agent, loadErr := a.runtimeAgent(ctx, agentName)
	if !ok {
		// Unknown event: still fail open with whatever passthrough we have.
		event = agents.Event(eventName)
	}
	opts := agents.Options{
		Timeout:   a.hookTimeout(),
		StateDir:  a.StateHome(),
		Now:       a.Now,
		LoadError: loadErr,
	}
	return agents.Run(ctx, agent, event, a.Stdin, a.Stdout, opts)
}

// runtimeAgent gives replacement-capable adapters a short-lived Jev client.
// A missing key or client setup failure intentionally leaves Asker nil, which
// makes the compaction policy preserve the host's original output.
func (a *App) runtimeAgent(ctx context.Context, name string) (agents.Agent, string) {
	base := agents.Lookup(name)
	if base == nil {
		return nil, ""
	}
	opts := a.SecurityLoadOptions()
	securityCfg, securityErr := securityconfig.LoadForHook(opts)
	loadError := ""
	if securityErr != nil {
		loadError = securityErr.Error()
	}
	client, policy := a.CompactionClient(ctx)
	client = app.UsageOrigin(client, "hook", name)
	if securityCfg != nil && (securityCfg.JevScoring || securityCfg.Injection.Mode != "off") {
		securityCfg.Asker = app.UsageOrigin(a.SecurityAsker(ctx), "hook", name)
	}
	reg, _ := registry.Load()
	decider := &registry.Decider{Registry: reg, StateDir: a.StateHome(), Getenv: a.Getenv}
	switch typed := base.(type) {
	case *agents.Claude:
		clone := *typed
		clone.Asker, clone.StateDir, clone.Policy = client, a.StateHome(), policy
		clone.Security, clone.SecurityDecider = securityCfg, decider
		return &clone, loadError
	case *agents.Cursor:
		clone := *typed
		clone.Asker, clone.StateDir, clone.Policy = client, a.StateHome(), policy
		clone.Security, clone.SecurityDecider = securityCfg, decider
		return &clone, loadError
	case *agents.Codex:
		clone := *typed
		clone.Asker, clone.StateDir, clone.Policy = client, a.StateHome(), policy
		clone.Security, clone.SecurityDecider = securityCfg, decider
		return &clone, loadError
	case *agents.Antigravity:
		clone := *typed
		clone.StateDir = a.StateHome()
		clone.Security, clone.SecurityDecider = securityCfg, decider
		return &clone, loadError
	case *agents.OpenCode:
		clone := *typed
		clone.Asker, clone.StateDir, clone.Policy = client, a.StateHome(), policy
		return &clone, loadError
	default:
		return base, loadError
	}
}

func parseHookEvent(name string) (agents.Event, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "pre-tool", "pretool", "pretooluse", "pre_tool":
		return agents.EventPreTool, true
	case "post-tool", "posttool", "posttooluse", "post_tool":
		return agents.EventPostTool, true
	case "stop":
		return agents.EventStop, true
	default:
		return "", false
	}
}

func (a *App) hookTimeout() time.Duration {
	ms := a.Getenv("JEVKIT_HOOK_TIMEOUT_MS")
	if ms == "" {
		return agents.DefaultTimeout
	}
	n, err := strconv.Atoi(ms)
	if err != nil || n <= 0 {
		return agents.DefaultTimeout
	}
	return time.Duration(n) * time.Millisecond
}
