package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/OWNER/jevkit/internal/jev"
	"github.com/OWNER/jevkit/internal/redact"
	"github.com/OWNER/jevkit/internal/registry"
)

// Asker is the slice of the jev client the server uses.
type Asker interface {
	Ask(ctx context.Context, req jev.Request) (*jev.Response, error)
}

// Config wires a Server. Everything except Log and Version is required.
type Config struct {
	// Decider supplies the registry and applies its thresholds.
	Decider *registry.Decider
	// Client asks Jev. It resolves the API key itself.
	Client Asker
	// Redact scrubs plain state/instructions text before it is sent; an
	// error rejects the call. It reports which rules fired (never the
	// matched text) so callers can record redaction counts.
	Redact func(string) (string, []redact.Hit, error)
	// RedactJSON scrubs structured (object or array) JSON text before it is
	// sent, preserving shape: jev_ask's structured state, instructions and
	// criteria. An error rejects the call.
	RedactJSON func(raw json.RawMessage) (json.RawMessage, []redact.Hit, error)
	// Unavailable returns "" when Jev can be used, else a short reason
	// (no key, breaker open). It must not return or log the key.
	Unavailable func(ctx context.Context) string
	// AuditDir, when set, receives one privacy-safe jev_ask audit line per
	// call (timestamp, caller, question ids/types, byte and redaction-hit
	// counts; never payload text) under <AuditDir>/jevkit/jev-ask-audit.jsonl.
	// Empty disables auditing.
	AuditDir string
	// Log receives all logging (stderr in production); nil discards it.
	Log io.Writer
	// Version is reported as the server version.
	Version string
}

// Server is the Jev MCP server.
type Server struct {
	cfg Config
	log *slog.Logger
	srv *sdk.Server
}

// The curated tools, each bound to one registered question set.
var curated = []struct{ tool, set, blurb string }{
	{"jev_classify_failure", "graph.failure-class", "Classify a graph-stage failure"},
	{"jev_classify_request", "graph.router-confidence", "Route a request"},
	{"jev_rank_relevance", "compaction.line-relevance", "Rank evidence/context lines"},
}

// New builds the server and registers its tools.
func New(cfg Config) (*Server, error) {
	switch {
	case cfg.Decider == nil || cfg.Decider.Registry == nil:
		return nil, errors.New("mcp: a decider with a registry is required")
	case cfg.Client == nil:
		return nil, errors.New("mcp: a client is required")
	case cfg.Redact == nil:
		return nil, errors.New("mcp: a redactor is required")
	case cfg.RedactJSON == nil:
		return nil, errors.New("mcp: a JSON redactor is required")
	case cfg.Unavailable == nil:
		return nil, errors.New("mcp: an availability check is required")
	}
	logw := cfg.Log
	if logw == nil {
		logw = io.Discard
	}
	s := &Server{cfg: cfg, log: slog.New(slog.NewTextHandler(logw, nil))}
	if cfg.Version == "" {
		s.cfg.Version = "dev"
	}
	s.srv = sdk.NewServer(
		&sdk.Implementation{Name: "jevkit", Version: s.cfg.Version},
		&sdk.ServerOptions{
			Logger: s.log,
			// Tools only: no logging capability, no list-changed notices.
			Capabilities: &sdk.ServerCapabilities{Tools: &sdk.ToolCapabilities{}},
		},
	)

	type entry struct {
		tool *sdk.Tool
		h    sdk.ToolHandler
	}
	entries := []entry{{s.askTool(), s.handleAsk}}
	devTool, err := s.developerAssessTool()
	if err != nil {
		return nil, err
	}
	entries = append(entries, entry{devTool, s.handleDeveloperAssess})
	for _, c := range curated {
		t, err := s.curatedTool(c.tool, c.set, c.blurb)
		if err != nil {
			return nil, err
		}
		set, tool := c.set, c.tool
		entries = append(entries, entry{t, func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return s.handleCurated(ctx, tool, set, req)
		}})
	}
	// Register in name order so the catalog is stable across starts.
	sort.Slice(entries, func(i, j int) bool { return entries[i].tool.Name < entries[j].tool.Name })
	for _, e := range entries {
		s.srv.AddTool(e.tool, e.h)
	}
	return s, nil
}

// Run serves one session over t until the peer disconnects or ctx ends.
func (s *Server) Run(ctx context.Context, t sdk.Transport) error {
	return s.srv.Run(ctx, t)
}

// RunStdio serves newline-delimited JSON-RPC over in and out.
func (s *Server) RunStdio(ctx context.Context, in io.ReadCloser, out io.WriteCloser) error {
	return s.Run(ctx, &sdk.IOTransport{Reader: in, Writer: out})
}

func (s *Server) logf(format string, args ...any) {
	s.log.Info(fmt.Sprintf(format, args...))
}
