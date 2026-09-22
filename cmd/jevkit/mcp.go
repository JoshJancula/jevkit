package main

import (
	"context"
	"encoding/json"
	"io"
	"sync"

	"github.com/spf13/cobra"

	"github.com/OWNER/jevkit/internal/breaker"
	"github.com/OWNER/jevkit/internal/jev"
	"github.com/OWNER/jevkit/internal/keystore"
	jevmcp "github.com/OWNER/jevkit/internal/mcp"
	"github.com/OWNER/jevkit/internal/redact"
	"github.com/OWNER/jevkit/internal/redact/config"
	"github.com/OWNER/jevkit/internal/registry"
)

func (a *App) mcpCmd() *cobra.Command {
	return a.group("mcp", "run and configure the Jev MCP server", a.mcpStatusCmd(), a.mcpConfigCmd(), &cobra.Command{
		Use:   "start",
		Short: "serve the Jev decision tools over stdio (JSON-RPC)",
		Long: `Serve jev_classify_request, jev_classify_failure, jev_rank_relevance,
jev_developer_assess and jev_ask as an MCP server on stdin/stdout. All
logging goes to stderr.

The server resolves the API key itself (env, credential command, keychain or
file); client configs never carry it. Without a key, tools return a
successful result with available:false so agents fall back to native behavior.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.mcpStart(cmd.Context())
		},
	})
}

func (a *App) mcpStart(ctx context.Context) error {
	srv, err := a.mcpServer()
	if err != nil {
		return failf("%v", err)
	}
	if err := srv.RunStdio(ctx, io.NopCloser(a.Stdin), nopWriteCloser{a.Stdout}); err != nil {
		return failf("mcp server: %v", err)
	}
	return nil
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// mcpServer wires the server to the keystore, breaker, registry and redaction
// config. The key is resolved here, lazily and at most once, and goes only to
// the client and the redactor.
func (a *App) mcpServer() (*jevmcp.Server, error) {
	reg, err := registry.Load()
	if err != nil {
		return nil, err
	}
	cfg := jev.ConfigFromEnv(a.getenv)
	store := a.store()
	br := a.Breaker
	if br == nil {
		br = breaker.New(a.stateHome())
	}

	var (
		mu       sync.Mutex
		key      string
		resolved bool
	)
	resolve := func(ctx context.Context) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if resolved {
			return key, nil
		}
		k, _, err := store.Resolve(ctx)
		if err != nil {
			return "", err
		}
		key, resolved = k, true
		return key, nil
	}

	var client jevmcp.Asker
	keyFn := func() (string, error) { return resolve(context.Background()) }
	if a.NewJev != nil {
		client = a.NewJev(cfg, keyFn)
	} else {
		c := jev.New(cfg, keyFn)
		c.Breaker = br
		client = c
	}

	loadRedactor := func() (*redact.Redactor, error) {
		rc, err := config.Load(a.loadOptions())
		if err != nil {
			return nil, err
		}
		if k, err := resolve(context.Background()); err == nil {
			rc.Options.Key = k
		}
		return rc.Redactor()
	}

	return jevmcp.New(jevmcp.Config{
		Decider: &registry.Decider{Registry: reg, StateDir: a.stateHome(), Getenv: a.getenv},
		Client:  client,
		Redact: func(text string) (string, []redact.Hit, error) {
			r, err := loadRedactor()
			if err != nil {
				return "", nil, err
			}
			res, err := r.Apply(text)
			return res.Text, res.Hits, err
		},
		RedactJSON: func(raw json.RawMessage) (json.RawMessage, []redact.Hit, error) {
			r, err := loadRedactor()
			if err != nil {
				return nil, nil, err
			}
			out, hits, err := r.ApplyJSON(raw)
			return out, hits, err
		},
		AuditDir: a.stateHome(),
		Unavailable: func(ctx context.Context) string {
			switch {
			case br.IsOpen():
				return "breaker-open"
			case cfg.Transport != jev.TransportFixture && store.Source(ctx) == keystore.SourceNone:
				return "no-key"
			}
			return ""
		},
		Log:     a.Stderr,
		Version: a.Version,
	})
}
