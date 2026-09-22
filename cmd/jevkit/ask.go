package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/OWNER/jevkit/internal/jev"
)

// askCmd exposes SystemOne's typed questions for a human at a terminal. It
// deliberately uses the same key resolution and redaction policy as agent
// callers; it is not an escape hatch around either one.
func (a *App) askCmd() *cobra.Command {
	return a.group("ask", "ask Jev a typed question", a.askTypeCmd("noul"), a.askTypeCmd("choice"), a.askTypeCmd("score"))
}

func (a *App) askTypeCmd(kind string) *cobra.Command {
	var state, question, format string
	var options []string
	c := &cobra.Command{
		Use:   kind,
		Short: "ask a " + kind + " question",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(state) == "" || strings.TrimSpace(question) == "" {
				return usagef("--state and --question are required")
			}
			if format != "text" && format != "json" {
				return usagef("--format must be text or json")
			}
			if kind == "choice" && len(options) == 0 {
				return usagef("choice requires at least one --option")
			}
			if len(options) > jev.MaxChoiceOptions {
				return usagef("choice accepts at most %d options", jev.MaxChoiceOptions)
			}
			return a.ask(cmd.Context(), kind, state, question, options, format)
		},
	}
	c.Flags().StringVar(&state, "state", "", "context to evaluate (required; redacted before send)")
	c.Flags().StringVar(&question, "question", "", "question instructions (required; redacted before send)")
	c.Flags().StringVar(&format, "format", "text", "output format: text or json")
	if kind == "choice" {
		c.Flags().StringArrayVar(&options, "option", nil, "allowed answer (repeatable; redacted before send)")
	}
	return c
}

func (a *App) ask(ctx context.Context, kind, state, instructions string, options []string, format string) error {
	cfg, ok := a.load("ask")
	if !ok {
		return codeErr(exitFail)
	}
	if pattern, matched := cfg.NeverSend.Match("jevkit ask " + kind); matched {
		return failf("not sent: matches never_send pattern %q", pattern)
	}
	r, err := cfg.Redactor()
	if err != nil {
		return failf("redaction failed")
	}
	redact := func(s string) (string, error) {
		out, err := r.Apply(s)
		if err != nil {
			return "", err
		}
		return out.Text, nil
	}
	state, err = redact(state)
	if err != nil {
		return failf("redaction failed")
	}
	instructions, err = redact(instructions)
	if err != nil {
		return failf("redaction failed")
	}
	q := map[string]jev.Question{}
	switch kind {
	case "noul":
		q["answer"] = jev.NoulQuestion{Instructions: instructions}
	case "score":
		q["answer"] = jev.ScoreQuestion{Instructions: instructions}
	case "choice":
		choices := make(map[string]*string, len(options))
		for _, option := range options {
			option, err = redact(option)
			if err != nil {
				return failf("redaction failed")
			}
			if strings.TrimSpace(option) == "" {
				return usagef("--option must not be empty")
			}
			choices[option] = nil
		}
		q["answer"] = jev.ChoiceQuestion{Instructions: instructions, Options: choices}
	}
	key, _, err := a.store().Resolve(ctx)
	if err != nil {
		return failf("no usable key: %v", err)
	}
	client := a.newJev(jev.ConfigFromEnv(a.getenv), func() (string, error) { return key, nil })
	resp, err := client.Ask(ctx, jev.Request{QuestionSetID: "manual." + kind, State: state, Questions: q})
	if err != nil || resp == nil {
		return failf("ask failed: %s", failReason(err))
	}
	answer, ok := resp.Answers["answer"]
	if !ok {
		return failf("ask failed: response missing answer")
	}
	return a.printAnswer(kind, answer, format)
}

func (a *App) printAnswer(kind string, answer jev.Answer, format string) error {
	if format == "json" {
		payload := map[string]any{"type": kind, "answer": answerJSON(answer)}
		b, err := json.Marshal(payload)
		if err != nil {
			return failf("could not format answer")
		}
		a.outf("%s\n", b)
		return nil
	}
	switch v := answer.(type) {
	case jev.NoulAnswer:
		a.outf("noul: %.6f\n", v.Noul)
	case jev.ScoreAnswer:
		a.outf("score: %.6f\nconfidence: %.6f\n", v.Score, v.Confidence)
	case jev.ChoiceAnswer:
		a.outf("choice: %s\nconfidence: %.6f\n", v.Choice, v.Confidence)
		if len(v.Probabilities) > 0 {
			keys := make([]string, 0, len(v.Probabilities))
			for k := range v.Probabilities {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				a.outf("%s: %.6f\n", k, v.Probabilities[k])
			}
		}
	default:
		return failf("ask failed: answer type does not match %s", kind)
	}
	return nil
}

func answerJSON(answer jev.Answer) map[string]any {
	switch v := answer.(type) {
	case jev.NoulAnswer:
		return map[string]any{"noul": v.Noul}
	case jev.ScoreAnswer:
		return map[string]any{"score": v.Score, "confidence": v.Confidence}
	case jev.ChoiceAnswer:
		return map[string]any{"choice": v.Choice, "confidence": v.Confidence, "probabilities": v.Probabilities}
	default:
		return map[string]any{"value": fmt.Sprint(answer)}
	}
}
