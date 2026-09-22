package main

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/OWNER/jevkit/internal/agents"
	"github.com/OWNER/jevkit/internal/compact"
	"github.com/OWNER/jevkit/internal/jev"
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
				return codeErr(a.runtimePassthrough(args[0], args[1]))
			}
			return codeErr(a.hook(cmd.Context(), args[0], args[1]))
		},
	}
	dispatch.Flags().IntVar(&protocol, "protocol", 0, "private protocol version")
	runtime := &cobra.Command{
		Use:    "_runtime",
		Hidden: true,
	}
	runtime.AddCommand(dispatch)
	return runtime
}

func (a *App) runtimePassthrough(agentName, eventName string) int {
	event, ok := parseHookEvent(eventName)
	if !ok {
		event = agents.Event(eventName)
	}
	if agent := agents.Lookup(agentName); agent != nil {
		a.outf("%s\n", agent.Passthrough(event))
	} else {
		a.outf("{}\n")
	}
	return exitOK
}

func (a *App) hook(ctx context.Context, agentName, eventName string) int {
	event, ok := parseHookEvent(eventName)
	agent := a.runtimeAgent(ctx, agentName)
	if !ok {
		// Unknown event: still fail open with whatever passthrough we have.
		event = agents.Event(eventName)
	}
	opts := agents.Options{
		Timeout:  a.hookTimeout(),
		StateDir: a.stateHome(),
		Now:      a.Now,
	}
	return agents.Run(ctx, agent, event, a.Stdin, a.Stdout, opts)
}

// runtimeAgent gives replacement-capable adapters a short-lived Jev client.
// A missing key or client setup failure intentionally leaves Asker nil, which
// makes the compaction policy preserve the host's original output.
func (a *App) runtimeAgent(ctx context.Context, name string) agents.Agent {
	base := agents.Lookup(name)
	if base == nil {
		return nil
	}
	key, _, err := a.store().Resolve(ctx)
	if err != nil {
		return base
	}
	client := a.newJev(jev.ConfigFromEnv(a.getenv), func() (string, error) { return key, nil })
	policy, _ := compact.LoadPolicy(a.compactionPolicyPath("", false), false)
	if project, err := compact.LoadPolicy(a.compactionPolicyPath("", true), true); err == nil {
		if policy == nil {
			policy = project
		} else {
			policy.Rules = append(policy.Rules, project.Rules...)
		}
	}
	switch typed := base.(type) {
	case *agents.Claude:
		clone := *typed
		clone.Asker, clone.StateDir, clone.Policy = client, a.stateHome(), policy
		return &clone
	case *agents.Cursor:
		clone := *typed
		clone.Asker, clone.StateDir, clone.Policy = client, a.stateHome(), policy
		return &clone
	default:
		return base
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
	ms := a.getenv("JEVKIT_HOOK_TIMEOUT_MS")
	if ms == "" {
		return agents.DefaultTimeout
	}
	n, err := strconv.Atoi(ms)
	if err != nil || n <= 0 {
		return agents.DefaultTimeout
	}
	return time.Duration(n) * time.Millisecond
}
