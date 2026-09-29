package util

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

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/agents"
	"github.com/JoshJancula/jevkit/internal/breaker"
	"github.com/JoshJancula/jevkit/internal/jev"
	"github.com/JoshJancula/jevkit/internal/keystore"
	"github.com/JoshJancula/jevkit/internal/redact"
	"github.com/JoshJancula/jevkit/internal/redact/config"
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
			return app.CodeErr(a.doctor(cmd.Context()))
		},
	}
}

// doctor reports the local setup.
func (a *App) doctor(ctx context.Context) int {
	a.doctorKey(ctx)
	modelValid := a.doctorEndpoint(ctx)
	a.doctorBreaker()
	a.doctorAgents()
	a.Outf("redaction\n")
	a.Outf("  user layer:    %s\n", a.layerState(a.UserPath()))
	a.Outf("  project layer: %s\n", a.layerState(a.ProjectPath()))

	cfg, err := config.Load(a.LoadOptions())
	if err != nil {
		a.Outf("  config:        INVALID\n  error:         %v\n  mode:          unknown (config invalid; nothing will be sent)\n", app.UnwrapReason(err))
		return app.ExitFail
	}
	mode := "standard"
	if cfg.Options.Strict {
		mode = "strict"
	}
	var layers []string
	for _, s := range cfg.Sources {
		layers = append(layers, a.LayerName(s))
	}
	active := "built-in"
	if len(layers) > 0 {
		active += ", " + strings.Join(layers, ", ")
	}
	a.Outf("  config:        ok\n  active layers: %s\n  mode:          %s\n", active, mode)
	a.Outf("  rules:         %d built-in, %d custom, %d disabled\n", len(redact.Rules()), len(cfg.Options.Custom), len(cfg.Options.DisableSoft))
	if !modelValid {
		return app.ExitFail
	}
	return app.ExitOK
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
	a.Outf("key\n")
	src := a.Store().Source(ctx)
	if src == keystore.SourceNone {
		a.Outf("  source:        none (run `jevkit key set`)\n")
		return
	}
	a.Outf("  source:        %s\n", src)
}

func (a *App) doctorEndpoint(ctx context.Context) bool {
	cfg, err := a.JevConfig()
	a.Outf("endpoint\n  url:           %s\n", cfg.Endpoint)
	if err != nil {
		a.Outf("  model:         INVALID (%v)\n", err)
		return false
	}
	a.Outf("  model:         %s\n", cfg.Model)
	switch {
	case cfg.Transport == jev.TransportFixture:
		a.Outf("  reachability:  not checked (fixture transport, offline)\n")
		return true
	case a.Dial == nil:
		a.Outf("  reachability:  not checked\n")
		return true
	}
	addr, err := dialAddr(cfg.Endpoint)
	if err != nil {
		a.Outf("  reachability:  unreachable (%v)\n", err)
		return true
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	conn, err := a.Dial(ctx, "tcp", addr)
	if err != nil {
		a.Outf("  reachability:  unreachable (tcp %s)\n", addr)
		return true
	}
	_ = conn.Close()
	a.Outf("  reachability:  reachable (tcp connect to %s; no request sent)\n", addr)
	return true
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
		b = breaker.New(a.StateHome())
	}
	st := b.Load()
	a.Outf("breaker\n")
	if !st.Open {
		a.Outf("  state:         closed (%d consecutive failures)\n", st.Consecutive)
		return
	}
	a.Outf("  state:         open (%s)\n", app.OrUnknown(st.Reason))
	if !st.OpenedAt.IsZero() {
		a.Outf("  opened:        %s\n", st.OpenedAt.UTC().Format(time.RFC3339))
	}
}

func (a *App) doctorAgents() {
	look := a.LookPath
	if look == nil {
		look = exec.LookPath
	}
	a.Outf("agents\n")
	for _, st := range agents.AllStates(a.WorkDir, a.UserHome(), look) {
		detect := "not found"
		if st.Detected {
			detect = st.Binary
		}
		a.Outf("  %-13s  %s\n", st.Name+":", detect)
		a.Outf("    hooks:       %s\n", st.Hooks)
		a.Outf("    mcp:         %s\n", st.MCP)
	}
}
