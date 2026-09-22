package main

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/OWNER/jevkit/internal/agents"
	"github.com/OWNER/jevkit/internal/breaker"
	"github.com/OWNER/jevkit/internal/jev"
	"github.com/OWNER/jevkit/internal/keystore"
	"github.com/OWNER/jevkit/internal/redact"
	"github.com/OWNER/jevkit/internal/redact/config"
)

func (a *App) doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "check the key source, endpoint, breaker, agents and redaction config",
		Long: `Report the local setup: where the key comes from, whether the endpoint is
reachable, the circuit breaker state, per-agent install state and the redaction
config. It never prints the key and sends no API request; reachability is a
TCP connect only. Exits 1 when the redaction config is invalid.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return codeErr(a.doctor(cmd.Context()))
		},
	}
}

// doctor reports the local setup.
func (a *App) doctor(ctx context.Context) int {
	a.doctorKey(ctx)
	a.doctorEndpoint(ctx)
	a.doctorBreaker()
	a.doctorAgents()
	a.outf("redaction\n")
	a.outf("  user layer:    %s\n", a.layerState(a.userPath()))
	a.outf("  project layer: %s\n", a.layerState(a.projectPath()))

	cfg, err := config.Load(a.loadOptions())
	if err != nil {
		a.outf("  config:        INVALID\n  error:         %v\n  mode:          unknown (config invalid; nothing will be sent)\n", unwrapReason(err))
		return exitFail
	}
	mode := "standard"
	if cfg.Options.Strict {
		mode = "strict"
	}
	var layers []string
	for _, s := range cfg.Sources {
		layers = append(layers, a.layerName(s))
	}
	active := "built-in"
	if len(layers) > 0 {
		active += ", " + strings.Join(layers, ", ")
	}
	a.outf("  config:        ok\n  active layers: %s\n  mode:          %s\n", active, mode)
	a.outf("  rules:         %d built-in, %d custom, %d disabled\n", len(redact.Rules()), len(cfg.Options.Custom), len(cfg.Options.DisableSoft))
	return exitOK
}

func (a *App) layerState(path string) string {
	if path == "" {
		return "(no config directory)"
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Sprintf("%s (not present)", path)
	}
	return fmt.Sprintf("%s (present)", path)
}

func (a *App) doctorKey(ctx context.Context) {
	a.outf("key\n")
	src := a.store().Source(ctx)
	if src == keystore.SourceNone {
		a.outf("  source:        none (run `jevkit key set`)\n")
		return
	}
	a.outf("  source:        %s\n", src)
}

func (a *App) doctorEndpoint(ctx context.Context) {
	cfg := jev.ConfigFromEnv(a.getenv)
	a.outf("endpoint\n  url:           %s\n", cfg.Endpoint)
	switch {
	case cfg.Transport == jev.TransportFixture:
		a.outf("  reachability:  not checked (fixture transport, offline)\n")
		return
	case a.Dial == nil:
		a.outf("  reachability:  not checked\n")
		return
	}
	addr, err := dialAddr(cfg.Endpoint)
	if err != nil {
		a.outf("  reachability:  unreachable (%v)\n", err)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	conn, err := a.Dial(ctx, "tcp", addr)
	if err != nil {
		a.outf("  reachability:  unreachable (tcp %s)\n", addr)
		return
	}
	_ = conn.Close()
	a.outf("  reachability:  reachable (tcp connect to %s; no request sent)\n", addr)
}

// dialAddr is host:port for an endpoint URL, defaulting the port by scheme.
func dialAddr(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" {
		return "", fmt.Errorf("invalid endpoint URL")
	}
	port := u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "http" {
			port = "80"
		}
	}
	return net.JoinHostPort(u.Hostname(), port), nil
}

func (a *App) doctorBreaker() {
	b := a.Breaker
	if b == nil {
		b = breaker.New(a.stateHome())
	}
	st := b.Load()
	a.outf("breaker\n")
	if !st.Open {
		a.outf("  state:         closed (%d consecutive failures)\n", st.Consecutive)
		return
	}
	a.outf("  state:         open (%s)\n", orUnknown(st.Reason))
	if !st.OpenedAt.IsZero() {
		a.outf("  opened:        %s\n", st.OpenedAt.UTC().Format(time.RFC3339))
	}
}

func orUnknown(s string) string {
	if s == "" {
		return "no reason recorded"
	}
	return s
}

func (a *App) doctorAgents() {
	look := a.LookPath
	if look == nil {
		look = exec.LookPath
	}
	a.outf("agents\n")
	for _, st := range agents.AllStates(a.WorkDir, a.homeDir(), look) {
		detect := "not found"
		if st.Detected {
			detect = st.Binary
		}
		a.outf("  %-13s  %s\n", st.Name+":", detect)
		a.outf("    hooks:       %s\n", st.Hooks)
		a.outf("    mcp:         %s\n", st.MCP)
	}
}
