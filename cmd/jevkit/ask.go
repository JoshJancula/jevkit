package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/OWNER/jevkit/internal/jev"
	"github.com/OWNER/jevkit/internal/redact"
)

// askCmd exposes SystemOne's typed questions for a human at a terminal. It
// deliberately uses the same key resolution and redaction policy as agent
// callers; it is not an escape hatch around either one.
func (a *App) askCmd() *cobra.Command {
	return a.group("ask", "ask Jev a typed question",
		a.askTypeCmd("noul"), a.askTypeCmd("choice"), a.askTypeCmd("score"), a.askRequestCmd())
}

func (a *App) askTypeCmd(kind string) *cobra.Command {
	var state, stateJSON, stateFile string
	var question, instructionsJSON, instructionsFile string
	var format string
	var options, levels, yesCriteria, noCriteria string
	var criteriaJSON, criteriaFile string
	c := &cobra.Command{
		Use:     kind,
		Short:   "ask a " + kind + " question",
		Example: askExample(kind),
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if format != "text" && format != "json" {
				return usagef("--format must be text or json")
			}
			resolvedState, err := resolveRichText(a, "state", "state-json", "state-file", state, stateJSON, stateFile)
			if err != nil {
				return err
			}
			resolvedInstructions, err := resolveRichText(a, "question", "instructions-json", "instructions-file", question, instructionsJSON, instructionsFile)
			if err != nil {
				return err
			}
			criteriaRaw, err := resolveCriteriaRaw(a, criteriaJSON, criteriaFile)
			if err != nil {
				return err
			}
			var criteria askCriteria
			switch kind {
			case "choice":
				if criteriaRaw != "" {
					if options != "" {
						return usagef("--options and --criteria-json/--criteria-file are mutually exclusive")
					}
					m, err := decodeCriteriaObject(criteriaRaw, "choice criteria")
					if err != nil {
						return err
					}
					criteria.raw = m
				} else {
					parsed, err := parseCommaList(options, "--options", 1, jev.MaxChoiceOptions)
					if err != nil {
						return err
					}
					criteria.options = parsed
				}
			case "score":
				if criteriaRaw != "" {
					if levels != "" {
						return usagef("--levels and --criteria-json/--criteria-file are mutually exclusive")
					}
					s, err := decodeCriteriaArray(criteriaRaw, "score criteria")
					if err != nil {
						return err
					}
					criteria.rawLevels = s
				} else {
					parsed, err := parseCommaList(levels, "--levels", 2, 10)
					if err != nil {
						return err
					}
					criteria.levels = parsed
				}
			case "noul":
				if criteriaRaw != "" {
					if yesCriteria != "" || noCriteria != "" {
						return usagef("--true-criteria/--false-criteria and --criteria-json/--criteria-file are mutually exclusive")
					}
					m, err := decodeCriteriaObject(criteriaRaw, "noul criteria")
					if err != nil {
						return err
					}
					criteria.raw = m
				} else {
					criteria.yes = yesCriteria
					criteria.no = noCriteria
				}
			}
			return a.ask(cmd.Context(), kind, resolvedState, resolvedInstructions, criteria, format)
		},
	}
	c.Flags().StringVar(&state, "state", "", "context to evaluate, as a plain string (redacted before send)")
	c.Flags().StringVar(&stateJSON, "state-json", "", "context to evaluate, as literal JSON text: a quoted string, object or array (redacted before send)")
	c.Flags().StringVar(&stateFile, "state-file", "", "path to a file holding the --state value, or - for stdin")
	c.Flags().StringVar(&question, "question", "", "question instructions, as a plain string (redacted before send)")
	c.Flags().StringVar(&instructionsJSON, "instructions-json", "", "question instructions, as literal JSON text (redacted before send)")
	c.Flags().StringVar(&instructionsFile, "instructions-file", "", "path to a file holding the --question value, or - for stdin")
	c.Flags().StringVar(&format, "format", "text", "output format: text or json")
	switch kind {
	case "choice":
		c.Flags().StringVar(&options, "options", "", "comma-separated allowed answers; shorthand for null-valued criteria (redacted before send)")
		c.Flags().StringVar(&criteriaJSON, "criteria-json", "",
			`detailed choice criteria as a JSON object, e.g. {"billing":"...","technical":"...","sales":"..."} (redacted before send)`)
		c.Flags().StringVar(&criteriaFile, "criteria-file", "", "path to a file holding the --criteria-json value, or - for stdin")
	case "score":
		c.Flags().StringVar(&levels, "levels", "", "comma-separated ordered score levels (2-10), low to high; shorthand for string criteria (redacted before send)")
		c.Flags().StringVar(&criteriaJSON, "criteria-json", "", "detailed ordered score criteria as a JSON array of 2-10 string/object/array levels, low to high (redacted before send)")
		c.Flags().StringVar(&criteriaFile, "criteria-file", "", "path to a file holding the --criteria-json value, or - for stdin")
	case "noul":
		c.Flags().StringVar(&yesCriteria, "true-criteria", "", "optional description of a true answer (redacted before send)")
		c.Flags().StringVar(&noCriteria, "false-criteria", "", "optional description of a false answer (redacted before send)")
		c.Flags().StringVar(&criteriaJSON, "criteria-json", "", `detailed noul criteria as a JSON object with optional "true"/"false" keys (redacted before send)`)
		c.Flags().StringVar(&criteriaFile, "criteria-file", "", "path to a file holding the --criteria-json value, or - for stdin")
	}
	return c
}

func askExample(kind string) string {
	switch kind {
	case "choice":
		return `  # Concise shorthand: a closed set of labels with no further rubric.
  jevkit ask choice --state "Tests: 42 passed, 0 failed" --question "What is the result?" --options "pass,fail"

  # Detailed criteria: a JSON object describing each option (labels alone are not always self-explanatory).
  jevkit ask choice --state "Customer says the invoice total looks wrong" --question "Route to which team?" \
    --criteria-json '{"billing":"Payment, invoice or refund issues","technical":"Product defects or errors","sales":"Pricing or new purchase questions"}'

  # Emit a machine-readable answer for scripts.
  jevkit ask choice --state "HTTP status: 503" --question "Classify this outcome" --options "retry,fail" --format json`
	case "score":
		return `  # Concise shorthand: ordered levels, low to high.
  jevkit ask score --state "The change touches authentication and billing" --question "How risky is this change?" --levels "low,medium,high"

  # Detailed criteria: a JSON array of rubric objects, still ordered low to high.
  jevkit ask score --state "PR diff: +812/-40 across 30 files" --question "How risky is this change?" \
    --criteria-json '[{"label":"low","rubric":"Docs or tests only"},{"label":"medium","rubric":"Application code, no auth or billing"},{"label":"high","rubric":"Touches auth, billing or migrations"}]'`
	default:
		return `  # Concise shorthand: describe a true and/or false answer in plain text.
  jevkit ask noul --state "The build completed successfully" --question "Did the build succeed?" --true-criteria "Build completed" --false-criteria "Build did not complete"

  # Detailed criteria: a JSON object, useful when a value needs structure instead of prose.
  jevkit ask noul --state "No tests failed" --question "Is this safe to deploy?" --criteria-json '{"true":{"rubric":"No failures and no flaky retries"}}'

  # Emit a machine-readable answer for scripts.
  jevkit ask noul --state "No tests failed" --question "Is this safe to deploy?" --format json`
	}
}

func (a *App) askRequestCmd() *cobra.Command {
	var file, model, format string
	c := &cobra.Command{
		Use:   "request",
		Short: "send a full API-shaped request with one or more named questions",
		Example: `  # A multi-question request read from a file.
  jevkit ask request --file request.json

  # From stdin, with a model override.
  cat request.json | jevkit ask request --file - --model jev-1.13.0

  # request.json shape:
  # {
  #   "model": "jev-latest",
  #   "state": "42 tests passed; 0 failed",
  #   "questions": {
  #     "outcome": {"type": "choice", "instructions": "What is the result?",
  #                 "criteria": {"pass": null, "fail": null}},
  #     "risk":    {"type": "score", "instructions": "How risky is this?",
  #                 "criteria": ["low", "medium", "high"]}
  #   }
  # }`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(file) == "" {
				return usagef("--file is required (a path, or - for stdin)")
			}
			if format != "text" && format != "json" {
				return usagef("--format must be text or json")
			}
			return a.askRequest(cmd.Context(), file, model, format)
		},
	}
	c.Flags().StringVar(&file, "file", "", "path to a JSON request file, or - for stdin (required)")
	c.Flags().StringVar(&model, "model", "", "override the model named in the request file")
	c.Flags().StringVar(&format, "format", "text", "output format: text or json")
	return c
}

type askCriteria struct {
	options   []string
	levels    []string
	yes, no   string
	raw       map[string]json.RawMessage // detailed choice or noul criteria, unredacted
	rawLevels []json.RawMessage          // detailed score criteria, unredacted
}

func parseCommaList(value, flag string, min, max int) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, usagef("%s is required", flag)
	}
	if strings.ContainsAny(value, "\r\n") {
		return nil, usagef("%s must be a single comma-separated line", flag)
	}
	values, err := csv.NewReader(strings.NewReader(value)).Read()
	if err != nil || len(values) == 0 {
		return nil, usagef("%s must be a comma-separated list", flag)
	}
	if len(values) < min || len(values) > max {
		return nil, usagef("%s accepts %d to %d values", flag, min, max)
	}
	seen := make(map[string]struct{}, len(values))
	for i := range values {
		values[i] = strings.TrimSpace(values[i])
		if values[i] == "" {
			return nil, usagef("%s must not contain an empty value", flag)
		}
		if _, ok := seen[values[i]]; ok {
			return nil, usagef("%s must not contain duplicate values", flag)
		}
		seen[values[i]] = struct{}{}
	}
	return values, nil
}

// resolveRichText picks exactly one of a plain value, a literal-JSON value or
// a file/stdin path, and returns the resolved text. plainFlag, jsonFlag and
// fileFlag name the flags for error messages.
func resolveRichText(a *App, plainFlag, jsonFlag, fileFlag, plain, asJSON, file string) (string, error) {
	type opt struct{ flag, val string }
	var chosen []opt
	if plain != "" {
		chosen = append(chosen, opt{plainFlag, plain})
	}
	if asJSON != "" {
		chosen = append(chosen, opt{jsonFlag, asJSON})
	}
	if file != "" {
		chosen = append(chosen, opt{fileFlag, file})
	}
	if len(chosen) == 0 {
		return "", usagef("one of --%s, --%s, --%s is required", plainFlag, jsonFlag, fileFlag)
	}
	if len(chosen) > 1 {
		return "", usagef("--%s and --%s are mutually exclusive", chosen[0].flag, chosen[1].flag)
	}
	c := chosen[0]
	if c.flag == fileFlag {
		return readInputSource(a, c.val)
	}
	if c.flag == jsonFlag && !json.Valid([]byte(c.val)) {
		return "", usagef("--%s must be valid JSON", jsonFlag)
	}
	return c.val, nil
}

// resolveCriteriaRaw reads --criteria-json/--criteria-file (mutually
// exclusive with each other); the caller enforces exclusivity against the
// kind's shorthand flags. It returns "" when neither was set.
func resolveCriteriaRaw(a *App, asJSON, file string) (string, error) {
	if asJSON != "" && file != "" {
		return "", usagef("--criteria-json and --criteria-file are mutually exclusive")
	}
	if asJSON != "" {
		if !json.Valid([]byte(asJSON)) {
			return "", usagef("--criteria-json must be valid JSON")
		}
		return asJSON, nil
	}
	if file != "" {
		return readInputSource(a, file)
	}
	return "", nil
}

func decodeCriteriaObject(raw, label string) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil || len(m) == 0 {
		return nil, usagef("%s must be a non-empty JSON object", label)
	}
	return m, nil
}

func decodeCriteriaArray(raw, label string) ([]json.RawMessage, error) {
	var s []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &s); err != nil || len(s) == 0 {
		return nil, usagef("%s must be a non-empty JSON array", label)
	}
	return s, nil
}

// readInputSource reads path (or stdin, for "-"), bounded by maxTestInput.
func readInputSource(a *App, path string) (string, error) {
	in := a.Stdin
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return "", failf("could not read %s: %v", path, err)
		}
		defer func() { _ = f.Close() }()
		in = f
	}
	data, err := io.ReadAll(io.LimitReader(in, maxTestInput+1))
	if err != nil {
		return "", failf("could not read input: %v", err)
	}
	if len(data) > maxTestInput {
		return "", failf("input larger than %d bytes", maxTestInput)
	}
	return string(data), nil
}

func (a *App) ask(ctx context.Context, kind, state, instructions string, criteria askCriteria, format string) error {
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
	state, err = redactText(r, state)
	if err != nil {
		return failf("redaction failed")
	}
	instructions, err = redactText(r, instructions)
	if err != nil {
		return failf("redaction failed")
	}
	redact := func(s string) (string, error) { return redactText(r, s) }
	q := map[string]jev.Question{}
	switch kind {
	case "noul":
		criteriaMap, err := noulCriteria(r, redact, criteria)
		if err != nil {
			return err
		}
		q["answer"] = jev.NoulQuestion{Instructions: instructions, Criteria: criteriaMap}
	case "score":
		criteriaLevels, err := scoreCriteria(r, redact, criteria)
		if err != nil {
			return err
		}
		q["answer"] = jev.ScoreQuestion{Instructions: instructions, Criteria: criteriaLevels}
	case "choice":
		choices, err := choiceCriteria(r, redact, criteria)
		if err != nil {
			return err
		}
		q["answer"] = jev.ChoiceQuestion{Instructions: instructions, Criteria: choices}
	}
	req := jev.Request{QuestionSetID: "manual." + kind, State: state, Questions: q}
	if err := req.Validate(); err != nil {
		return usagef("%v", err)
	}
	key, _, err := a.store().Resolve(ctx)
	if err != nil {
		return failf("no usable key: %v", err)
	}
	client := a.newJev(jev.ConfigFromEnv(a.getenv), func() (string, error) { return key, nil })
	resp, err := client.Ask(ctx, req)
	if err != nil || resp == nil {
		return failf("ask failed: %s", failReason(err))
	}
	answer, ok := resp.Answers["answer"]
	if !ok {
		return failf("ask failed: response missing answer")
	}
	return a.printAnswer(kind, answer, format)
}

func (a *App) askRequest(ctx context.Context, path, modelOverride, format string) error {
	cfg, ok := a.load("ask")
	if !ok {
		return codeErr(exitFail)
	}
	if pattern, matched := cfg.NeverSend.Match("jevkit ask request"); matched {
		return failf("not sent: matches never_send pattern %q", pattern)
	}
	r, err := cfg.Redactor()
	if err != nil {
		return failf("redaction failed")
	}
	data, err := readInputSource(a, path)
	if err != nil {
		return err
	}
	in, err := parseRequestFile(data)
	if err != nil {
		return err
	}
	redactedState, err := redactRawJSON(r, in.State)
	if err != nil {
		return failf("redaction failed")
	}
	questions := make(map[string]jev.Question, len(in.Questions))
	for name, q := range in.Questions {
		if strings.TrimSpace(name) == "" {
			return usagef("question id must not be empty")
		}
		if rawJSONIsBlankText(q.Instructions) {
			return usagef("question %q: instructions is required", name)
		}
		redactedInstr, err := redactRawJSON(r, q.Instructions)
		if err != nil {
			return failf("redaction failed")
		}
		var redactedCriteria json.RawMessage
		if len(q.Criteria) > 0 {
			redactedCriteria, err = redactRawJSON(r, q.Criteria)
			if err != nil {
				return failf("redaction failed")
			}
		}
		switch q.Type {
		case "noul":
			criteria := map[string]json.RawMessage{}
			if len(redactedCriteria) > 0 {
				if err := json.Unmarshal(redactedCriteria, &criteria); err != nil {
					return usagef("question %q: noul criteria must be a JSON object", name)
				}
			}
			questions[name] = jev.NoulQuestion{Instructions: string(redactedInstr), Criteria: criteria}
		case "choice":
			if len(redactedCriteria) == 0 {
				return usagef("question %q: choice requires criteria", name)
			}
			var criteria map[string]json.RawMessage
			if err := json.Unmarshal(redactedCriteria, &criteria); err != nil {
				return usagef("question %q: choice criteria must be a JSON object", name)
			}
			questions[name] = jev.ChoiceQuestion{Instructions: string(redactedInstr), Criteria: criteria}
		case "score":
			if len(redactedCriteria) == 0 {
				return usagef("question %q: score requires criteria", name)
			}
			var criteria []json.RawMessage
			if err := json.Unmarshal(redactedCriteria, &criteria); err != nil {
				return usagef("question %q: score criteria must be a JSON array", name)
			}
			questions[name] = jev.ScoreQuestion{Instructions: string(redactedInstr), Criteria: criteria}
		default:
			return usagef("question %q: unknown question type %q", name, q.Type)
		}
	}
	model := in.Model
	if modelOverride != "" {
		model = modelOverride
	}
	req := jev.Request{QuestionSetID: "manual.request", State: string(redactedState), Model: model, Questions: questions}
	if err := req.Validate(); err != nil {
		return usagef("%v", err)
	}
	key, _, err := a.store().Resolve(ctx)
	if err != nil {
		return failf("no usable key: %v", err)
	}
	client := a.newJev(jev.ConfigFromEnv(a.getenv), func() (string, error) { return key, nil })
	resp, err := client.Ask(ctx, req)
	if err != nil || resp == nil {
		return failf("ask failed: %s", failReason(err))
	}
	return a.printResponse(resp, format)
}

type requestQuestionInput struct {
	Type         string          `json:"type"`
	Instructions json.RawMessage `json:"instructions"`
	Criteria     json.RawMessage `json:"criteria"`
}

type requestFileInput struct {
	Model     string                          `json:"model"`
	State     json.RawMessage                 `json:"state"`
	Questions map[string]requestQuestionInput `json:"questions"`
}

// parseRequestFile strictly decodes a full API-shaped request: an unknown
// top-level or per-question field is rejected rather than silently ignored.
func parseRequestFile(data string) (requestFileInput, error) {
	var in requestFileInput
	dec := json.NewDecoder(strings.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return requestFileInput{}, usagef("invalid request file: %v", err)
	}
	if strings.TrimSpace(string(in.State)) == "" {
		return requestFileInput{}, usagef(`request file must include "state"`)
	}
	if len(in.Questions) == 0 {
		return requestFileInput{}, usagef("request file must include at least one question")
	}
	return in, nil
}

// rawJSONIsBlankText reports whether raw is a JSON string whose content is
// empty or all whitespace; an object or array is never blank by this check.
func rawJSONIsBlankText(raw json.RawMessage) bool {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return len(strings.TrimSpace(string(raw))) == 0
	}
	return strings.TrimSpace(s) == ""
}

// redactText redacts a State or Instructions value: literal JSON text (the
// same shapes wireText treats as literal) is redacted recursively while
// preserving its JSON shape; anything else is redacted as a plain string,
// matching how the value will actually reach the wire.
func redactText(r *redact.Redactor, s string) (string, error) {
	if jev.IsLiteralJSON(s) {
		out, err := redactRawJSON(r, json.RawMessage(strings.TrimSpace(s)))
		if err != nil {
			return "", err
		}
		return string(out), nil
	}
	res, err := r.Apply(s)
	if err != nil {
		return "", err
	}
	return res.Text, nil
}

func redactRawJSON(r *redact.Redactor, raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return raw, nil
	}
	out, _, err := r.ApplyJSON(raw)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func redactCriteriaMap(r *redact.Redactor, m map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, failf("redaction failed")
	}
	out, err := redactRawJSON(r, raw)
	if err != nil {
		return nil, failf("redaction failed")
	}
	var redacted map[string]json.RawMessage
	if err := json.Unmarshal(out, &redacted); err != nil {
		return nil, failf("redaction failed")
	}
	return redacted, nil
}

func redactCriteriaSlice(r *redact.Redactor, s []json.RawMessage) ([]json.RawMessage, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, failf("redaction failed")
	}
	out, err := redactRawJSON(r, raw)
	if err != nil {
		return nil, failf("redaction failed")
	}
	var redacted []json.RawMessage
	if err := json.Unmarshal(out, &redacted); err != nil {
		return nil, failf("redaction failed")
	}
	return redacted, nil
}

func redactValues(redact func(string) (string, error), values []string) ([]string, error) {
	redacted := make([]string, len(values))
	seen := make(map[string]struct{}, len(values))
	for i, value := range values {
		var err error
		redacted[i], err = redact(value)
		if err != nil {
			return nil, failf("redaction failed")
		}
		redacted[i] = strings.TrimSpace(redacted[i])
		if redacted[i] == "" {
			return nil, usagef("a redacted criteria value must not be empty")
		}
		if _, ok := seen[redacted[i]]; ok {
			return nil, usagef("criteria values must remain distinct after redaction")
		}
		seen[redacted[i]] = struct{}{}
	}
	return redacted, nil
}

func choiceCriteria(r *redact.Redactor, redact func(string) (string, error), c askCriteria) (map[string]json.RawMessage, error) {
	if c.raw != nil {
		return redactCriteriaMap(r, c.raw)
	}
	options, err := redactValues(redact, c.options)
	if err != nil {
		return nil, err
	}
	choices := make(map[string]json.RawMessage, len(options))
	for _, option := range options {
		choices[option] = jev.Null()
	}
	return choices, nil
}

func scoreCriteria(r *redact.Redactor, redact func(string) (string, error), c askCriteria) ([]json.RawMessage, error) {
	if c.rawLevels != nil {
		return redactCriteriaSlice(r, c.rawLevels)
	}
	levels, err := redactValues(redact, c.levels)
	if err != nil {
		return nil, err
	}
	out := make([]json.RawMessage, len(levels))
	for i, l := range levels {
		out[i] = jev.Str(l)
	}
	return out, nil
}

func noulCriteria(r *redact.Redactor, redact func(string) (string, error), c askCriteria) (map[string]json.RawMessage, error) {
	if c.raw != nil {
		return redactCriteriaMap(r, c.raw)
	}
	criteria := make(map[string]json.RawMessage, 2)
	for field, value := range map[string]string{"true": c.yes, "false": c.no} {
		if strings.TrimSpace(value) == "" {
			continue
		}
		redacted, err := redact(value)
		if err != nil {
			return nil, failf("redaction failed")
		}
		criteria[field] = jev.Str(redacted)
	}
	return criteria, nil
}

func (a *App) printAnswer(kind string, answer jev.Answer, format string) error {
	if answerKind(answer) != kind {
		return failf("ask failed: answer type does not match %s", kind)
	}
	if format == "json" {
		payload := map[string]any{"type": kind, "answer": answerJSON(answer)}
		b, err := json.Marshal(payload)
		if err != nil {
			return failf("could not format answer")
		}
		a.outf("%s\n", b)
		return nil
	}
	a.writeAnswerText("", answer)
	return nil
}

// printResponse prints every named answer from a multi-question request:
// human-readable text per answer, or exact JSON with model, usage, answer
// type, confidence, probabilities and Score legend/distribution.
func (a *App) printResponse(resp *jev.Response, format string) error {
	names := make([]string, 0, len(resp.Answers))
	for name := range resp.Answers {
		names = append(names, name)
	}
	sort.Strings(names)

	if format == "json" {
		answers := make(map[string]any, len(names))
		for _, name := range names {
			answers[name] = namedAnswerJSON(resp.Answers[name])
		}
		payload := map[string]any{
			"model":   resp.Model,
			"usage":   map[string]any{"input_tokens": resp.Usage.InputTokens, "output_tokens": resp.Usage.OutputTokens},
			"answers": answers,
		}
		b, err := json.Marshal(payload)
		if err != nil {
			return failf("could not format answer")
		}
		a.outf("%s\n", b)
		return nil
	}
	a.outf("model: %s\n", resp.Model)
	for _, name := range names {
		a.outf("%s:\n", name)
		a.writeAnswerText("  ", resp.Answers[name])
	}
	return nil
}

func answerKind(answer jev.Answer) string {
	switch answer.(type) {
	case jev.NoulAnswer:
		return "noul"
	case jev.ChoiceAnswer:
		return "choice"
	case jev.ScoreAnswer:
		return "score"
	default:
		return ""
	}
}

func (a *App) writeAnswerText(prefix string, answer jev.Answer) {
	switch v := answer.(type) {
	case jev.NoulAnswer:
		a.outf("%snoul: %.6f\n", prefix, v.Noul)
	case jev.ScoreAnswer:
		a.outf("%sscore: %.6f\n%sconfidence: %.6f\n", prefix, v.Score, prefix, v.Confidence)
		if len(v.Legend) > 0 {
			a.outf("%slegend: %s\n", prefix, strings.Join(v.Legend, ", "))
		}
		if len(v.Distribution) > 0 {
			for _, k := range sortedFloatKeys(v.Distribution) {
				a.outf("%s%s: %.6f\n", prefix, k, v.Distribution[k])
			}
		}
	case jev.ChoiceAnswer:
		a.outf("%schoice: %s\n%sconfidence: %.6f\n", prefix, v.Choice, prefix, v.Confidence)
		if len(v.Probabilities) > 0 {
			for _, k := range sortedFloatKeys(v.Probabilities) {
				a.outf("%s%s: %.6f\n", prefix, k, v.Probabilities[k])
			}
		}
	}
}

func sortedFloatKeys(m map[string]float64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func answerJSON(answer jev.Answer) map[string]any {
	switch v := answer.(type) {
	case jev.NoulAnswer:
		return map[string]any{"noul": v.Noul}
	case jev.ScoreAnswer:
		out := map[string]any{"score": v.Score, "confidence": v.Confidence}
		if len(v.Legend) > 0 {
			out["legend"] = v.Legend
		}
		if len(v.Distribution) > 0 {
			out["distribution"] = v.Distribution
		}
		return out
	case jev.ChoiceAnswer:
		return map[string]any{"choice": v.Choice, "confidence": v.Confidence, "probabilities": v.Probabilities}
	default:
		return map[string]any{"value": fmt.Sprint(answer)}
	}
}

func namedAnswerJSON(answer jev.Answer) map[string]any {
	out := answerJSON(answer)
	out["type"] = answerKind(answer)
	return out
}
