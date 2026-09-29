package sdlc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/breaker"
	"github.com/JoshJancula/jevkit/internal/jev"
	"github.com/JoshJancula/jevkit/internal/keystore"
	"github.com/JoshJancula/jevkit/internal/redact/config"
	"github.com/JoshJancula/jevkit/internal/sdlc/ledger"
	"github.com/JoshJancula/jevkit/internal/sdlc/spec"
)

// A question is one drive step. The run lock ensures two drivers cannot ask
// the same question and record conflicting answers.
func (a *App) sdlcDriveQuestion(ctx context.Context, runID string, store *ledger.Store) error {
	return store.WithRunLock(func() error {
		run, err := store.ReadRun()
		if err != nil {
			return app.Failf("read run: %v", err)
		}
		if run.StageFlow == nil || run.Adaptive == nil || run.Adaptive.Stage != "question" {
			return app.Failf("run %s is no longer at a question", runID)
		}
		stage, ok := run.StageFlow.Stage()
		if !ok || stage.Question == nil {
			return app.Failf("run %s has invalid question stage", runID)
		}
		policy, _, err := a.sdlcEnrollment()
		if err != nil {
			return app.Failf("%v", err)
		}
		remaining, err := a.treeRemaining(run, policy)
		if err != nil {
			return app.Failf("%v", err)
		}
		st := *run.Adaptive
		if remaining <= 0 {
			st.Pause("run-time-budget-exhausted")
			run.Adaptive = &st
			run.UpdatedAt = a.Clock().UTC().Format(time.RFC3339)
			if err := store.WriteRun(run); err != nil {
				return app.Failf("store timeout: %v", err)
			}
			return app.Failf("run %s paused: %s", runID, st.Outcome)
		}
		if limit := time.Duration(policy.MaxInvocationSeconds) * time.Second; remaining > limit {
			remaining = limit
		}
		stepCtx, cancel := context.WithTimeout(ctx, remaining)
		defer cancel()
		answer, reason := a.askStageQuestion(stepCtx, run, stage, store)
		if err := run.StageFlow.Advance(answer, &st); err != nil {
			return app.Failf("advance question: %v", err)
		}
		if err := a.chargeTree(&run, policy, "step"); err != nil {
			return app.Failf("charge stage step: %v", err)
		}
		run.Adaptive = &st
		run.UpdatedAt = a.Clock().UTC().Format(time.RFC3339)
		if err := store.WriteRun(run); err != nil {
			return app.Failf("store question: %v", err)
		}
		if err := a.recordDecision(store, ledger.Decision{RunID: runID, Kind: "stage-transition", Stage: stage.ID, Trigger: "workflow question", Choice: answer, Outcome: reason, Next: run.StageFlow.Current}); err != nil {
			return err
		}
		if answer == "fallback" {
			a.Outf("  question %s: fallback (%s) → %s\n", stage.ID, reason, run.StageFlow.Current)
		} else {
			a.Outf("  question %s: %s → %s\n", stage.ID, answer, run.StageFlow.Current)
		}
		return nil
	})
}

func (a *App) askStageQuestion(ctx context.Context, run ledger.Run, stage spec.Stage, store *ledger.Store) (string, string) {
	q := stage.Question
	cfg, err := a.JevConfig()
	if err != nil {
		return "fallback", "invalid Jev model setting"
	}
	keys := a.Store()
	br := a.Breaker
	if br == nil {
		br = breaker.New(a.StateHome())
	}
	if br.IsOpen() {
		return "fallback", "Jev unavailable"
	}
	if cfg.Transport != jev.TransportFixture && keys.Source(ctx) == keystore.SourceNone {
		return "fallback", "no Jev key"
	}
	keyFn := func() (string, error) { key, _, err := keys.Resolve(ctx); return key, err }
	rc, err := config.Load(a.LoadOptions())
	if err != nil {
		return "fallback", "redaction unavailable"
	}
	if key, err := keyFn(); err == nil {
		rc.Options.Key = key
	}
	redactor, err := rc.Redactor()
	if err != nil {
		return "fallback", "redaction unavailable"
	}
	redact := func(value string) (string, error) { return app.RedactText(redactor, value) }
	state := "Task: " + run.Task + "\nCurrent stage: " + stage.ID
	for _, name := range []string{"plan.md", "patch.diff"} {
		if data, err := store.ReadArtifact(name); err == nil {
			if len(data) > 4000 {
				data = data[:4000]
			}
			state += "\n" + name + ":\n" + string(data)
		}
	}
	state, err = redact(state)
	if err != nil {
		return "fallback", "redaction unavailable"
	}
	prompt, err := redact(q.Prompt)
	if err != nil {
		return "fallback", "redaction unavailable"
	}
	criteria := make(map[string]json.RawMessage, len(q.Options))
	for option, description := range q.Options {
		value, err := redact(description)
		if err != nil {
			return "fallback", "redaction unavailable"
		}
		criteria[option], _ = json.Marshal(value)
	}
	req := jev.Request{QuestionSetID: "sdlc.custom." + run.Workflow + "." + stage.ID, State: state, Model: cfg.Model, Questions: map[string]jev.Question{"route": jev.ChoiceQuestion{Instructions: prompt, Criteria: criteria}}}
	client := a.JevClient(cfg, keyFn)
	if direct, ok := client.(*jev.Client); ok {
		direct.Breaker = br
	}
	response, err := client.Ask(ctx, req)
	if err != nil {
		return "fallback", "Jev unavailable"
	}
	if response == nil {
		return "fallback", "empty Jev answer"
	}
	var answer jev.ChoiceAnswer
	switch value := response.Answers["route"].(type) {
	case jev.ChoiceAnswer:
		answer = value
	case *jev.ChoiceAnswer:
		if value != nil {
			answer = *value
		}
	default:
		return "fallback", "invalid Jev answer"
	}
	if _, ok := q.Options[answer.Choice]; !ok {
		return "fallback", "unknown Jev choice"
	}
	if answer.Confidence < q.MinConfidence {
		return "fallback", fmt.Sprintf("confidence %.2f below %.2f", answer.Confidence, q.MinConfidence)
	}
	if strings.TrimSpace(answer.Choice) == "" {
		return "fallback", "empty Jev choice"
	}
	return answer.Choice, ""
}
