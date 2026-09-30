package app

import (
	"context"
	"fmt"

	"github.com/JoshJancula/jevkit/internal/jev"
	"github.com/JoshJancula/jevkit/internal/usage"
)

type runIDContextKey struct{}
type originContextKey struct{}
type agentContextKey struct{}

func WithUsageRun(ctx context.Context, runID string) context.Context {
	return context.WithValue(ctx, runIDContextKey{}, runID)
}

func WithUsageOrigin(ctx context.Context, origin, agent string) context.Context {
	ctx = context.WithValue(ctx, originContextKey{}, origin)
	return context.WithValue(ctx, agentContextKey{}, agent)
}

type usageOriginAsker struct {
	inner         Asker
	origin, agent string
}

func (w usageOriginAsker) Ask(ctx context.Context, req jev.Request) (*jev.Response, error) {
	return w.inner.Ask(WithUsageOrigin(ctx, w.origin, w.agent), req)
}

func UsageOrigin(client Asker, origin, agent string) Asker {
	if client == nil {
		return nil
	}
	return usageOriginAsker{inner: client, origin: origin, agent: agent}
}

type recordingJev struct {
	inner    Asker
	stateDir string
	config   jev.Config
}

func (r recordingJev) Ask(ctx context.Context, req jev.Request) (*jev.Response, error) {
	makeRecord := func(model string, in, out int, measured bool, status string) usage.Record {
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
		rec := usage.Record{Model: model, QuestionSetID: req.QuestionSetID, Transport: transport,
			UsageSource: usage.SourceUnavailable, InputTokens: in, OutputTokens: out, Status: status}
		if measured {
			rec.UsageSource = usage.SourceMeasured
		}
		if runID, ok := ctx.Value(runIDContextKey{}).(string); ok {
			rec.RunID = runID
		}
		if origin, ok := ctx.Value(originContextKey{}).(string); ok {
			rec.Origin = origin
		}
		if agent, ok := ctx.Value(agentContextKey{}).(string); ok {
			rec.Agent = agent
		}
		return rec
	}
	if client, ok := r.inner.(*jev.Client); ok {
		copyClient := *client
		var recordErr error
		prior := copyClient.ObserveAttempt
		copyClient.ObserveAttempt = func(a jev.Attempt) {
			if prior != nil {
				prior(a)
			}
			status := "transport-error"
			if a.Success {
				status = "ok"
			} else if a.Status >= 200 && a.Status <= 299 {
				status = "protocol-error"
			} else if a.Status != 0 {
				status = fmt.Sprintf("http-%d", a.Status)
			}
			if err := usage.Append(r.stateDir, makeRecord(a.Model, a.Usage.InputTokens, a.Usage.OutputTokens, a.UsageReported, status)); err != nil && recordErr == nil {
				recordErr = err
			}
		}
		resp, err := copyClient.Ask(ctx, req)
		if err == nil && recordErr != nil {
			return nil, recordErr
		}
		return resp, err
	}
	resp, err := r.inner.Ask(ctx, req)
	if err != nil || resp == nil {
		return resp, err
	}
	record := makeRecord(resp.Model, resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.UsageReported, "ok")
	if err := usage.Append(r.stateDir, record); err != nil {
		return nil, err
	}
	return resp, nil
}

func (a *App) RecordJev(client Asker, cfg jev.Config) Asker {
	return recordingJev{inner: client, stateDir: a.StateHome(), config: cfg}
}
