package main

import (
	"context"

	"github.com/OWNER/jevkit/internal/jev"
	"github.com/OWNER/jevkit/internal/usage"
)

type runIDContextKey struct{}

func withUsageRun(ctx context.Context, runID string) context.Context {
	return context.WithValue(ctx, runIDContextKey{}, runID)
}

type recordingJev struct {
	inner    Asker
	stateDir string
	config   jev.Config
}

func (r recordingJev) Ask(ctx context.Context, req jev.Request) (*jev.Response, error) {
	resp, err := r.inner.Ask(ctx, req)
	if err != nil || resp == nil {
		return resp, err
	}
	model := resp.Model
	if model == "" {
		model = req.Model
	}
	if model == "" {
		model = r.config.Model
	}
	if model == "" {
		model = jev.DefaultModel
	}
	transport := usage.TransportHTTPS
	if r.config.Transport == jev.TransportFixture {
		transport = usage.TransportFixture
	}
	record := usage.Record{Model: model, QuestionSetID: req.QuestionSetID, Transport: transport,
		UsageSource: usage.SourceUnavailable}
	if resp.UsageReported {
		record.InputTokens, record.OutputTokens = resp.Usage.InputTokens, resp.Usage.OutputTokens
		record.UsageSource = usage.SourceMeasured
	}
	if runID, ok := ctx.Value(runIDContextKey{}).(string); ok {
		record.RunID = runID
	}
	if err := usage.Append(r.stateDir, record); err != nil {
		return nil, err
	}
	return resp, nil
}

func (a *App) recordJev(client Asker, cfg jev.Config) Asker {
	return recordingJev{inner: client, stateDir: a.stateHome(), config: cfg}
}
