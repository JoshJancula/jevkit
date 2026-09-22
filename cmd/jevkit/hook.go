package main

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/OWNER/jevkit/internal/agents"
)

func (a *App) hookCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "hook <agent> <event>",
		Short: "run an agent hook handler (stdin JSON → stdout JSON)",
		Long: `Read a hook payload from stdin, dispatch to the named agent adapter for
the given event (pre-tool, post-tool, or stop), and print a JSON response.
Always fails open: garbage input, panics, timeouts, and protocol-version
mismatches exit 0 with a valid empty/allow response.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return codeErr(a.hook(cmd.Context(), args[0], args[1]))
		},
	}
}

func (a *App) hook(ctx context.Context, agentName, eventName string) int {
	event, ok := parseHookEvent(eventName)
	agent := agents.Lookup(agentName)
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
