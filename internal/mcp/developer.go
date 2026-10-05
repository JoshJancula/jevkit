package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/JoshJancula/jevkit/internal/jev"
)

// developerAssessment describes one built-in developer.* question set the
// jev_developer_assess dispatcher can call: its registry id, a short blurb
// for discovery, the state keys it requires, and the optional keys that
// sharpen its answer (named back to the caller on gather when absent).
type developerAssessment struct {
	ID            string
	Blurb         string
	RequiredState []string
	HelpfulState  []string
}

// developerStateKeys are every field a developer.* assessment's structured
// state may carry; unknown keys are rejected. Each assessment additionally
// requires a subset (see developerAssessments) so a caller cannot get a
// confident-looking answer from an under-specified request.
var developerStateKeys = []string{"request", "plannedAction", "finding", "diffSummary", "affectedAreas", "testOutput", "environment", "constraints"}

const developerStateFieldsDoc = "request (string: what the user asked for, quoted or closely paraphrased), plannedAction (string: what you are about to do, naming files, commands or systems), finding (string: a review or lint finding to judge), diffSummary (string), affectedAreas (array of strings), testOutput (string), environment (string), constraints (string)"

// developerAssessments is the opt-in, versioned set of built-in developer
// decision-support assessments. Every one is decision support, not
// autonomous execution: an answer is returned as data only, never turned
// into an automatic code change, command, merge, deploy, or secret exposure.
var developerAssessments = []developerAssessment{
	{"developer.proceed-check", "Before acting, decide proceed / narrow-scope / ask-user / stop for a planned action against what the user asked", []string{"request", "plannedAction"}, []string{"constraints", "environment", "affectedAreas"}},
	{"developer.done-check", "Before reporting a task finished, decide done / needs-verification / incomplete / off-target", []string{"request", "diffSummary"}, []string{"testOutput", "constraints"}},
	{"developer.finding-validity", "Decide whether a review finding is valid, a false positive, or needs verification before you act on it", []string{"finding"}, []string{"diffSummary", "testOutput", "constraints"}},
	{"developer.change-risk", "Score the risk of a code change from low to critical", []string{"diffSummary", "affectedAreas"}, []string{"testOutput", "environment", "constraints"}},
	{"developer.failure-triage", "Classify a test/build failure's most likely root-cause category", []string{"testOutput"}, []string{"diffSummary", "environment"}},
	{"developer.test-priority", "Decide how much testing a change needs before merge", []string{"diffSummary"}, []string{"affectedAreas", "testOutput"}},
	{"developer.review-disposition", "Decide the review disposition for a change", []string{"diffSummary"}, []string{"affectedAreas", "testOutput", "constraints"}},
	{"developer.release-readiness", "Decide whether a change is ready to release", []string{"testOutput", "constraints"}, []string{"diffSummary", "environment"}},
}

func developerAssessmentByID(id string) (developerAssessment, bool) {
	for _, a := range developerAssessments {
		if a.ID == id {
			return a, true
		}
	}
	return developerAssessment{}, false
}

func developerAssessmentIDs() []string {
	ids := make([]string, len(developerAssessments))
	for i, a := range developerAssessments {
		ids[i] = a.ID
	}
	return ids
}

// developerAssessTool builds the jev_developer_assess dispatcher's tool
// definition. It fails if the registry is missing any built-in assessment
// id, so a broken registry is caught at startup rather than at call time.
func (s *Server) developerAssessTool() (*sdk.Tool, error) {
	var lines strings.Builder
	for _, a := range developerAssessments {
		if _, ok := s.cfg.Decider.Registry.Set(a.ID); !ok {
			return nil, fmt.Errorf("mcp: registry has no question set %q", a.ID)
		}
		fmt.Fprintf(&lines, "\n- %s: %s (requires state.%s; optional state.%s)", a.ID, a.Blurb,
			strings.Join(a.RequiredState, ", state."), strings.Join(a.HelpfulState, ", state."))
	}
	stateProps := map[string]any{}
	for _, k := range developerStateKeys {
		if k == "affectedAreas" {
			stateProps[k] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
			continue
		}
		stateProps[k] = map[string]any{"type": "string"}
	}
	return &sdk.Tool{
		Name: "jev_developer_assess",
		Description: fmt.Sprintf(`A fast second opinion for coding agents at decision points: before a broad or risky action, before reporting work done, when triaging a failure, or when judging a review finding. One strongly typed dispatcher over a small, versioned set of built-in developer.* question sets (registry-backed, auditable, shadow-rollout capable — see docs/AGENT-INTEGRATIONS.md for calibration and shadow-mode notes). This is decision support only: no jevkit tool call ever modifies code, runs a command, merges, deploys, or exposes a secret. The caller decides what, if anything, to do with the answer.

Built-in assessments:%s

state is a structured JSON object shared by every assessment: %s. Each assessment requires its own subset of these keys (see the list above) and is sharper with the optional ones it lists; unrecognized keys are rejected. Send summaries and the lines that matter, not whole files. All state is redacted (JSON-aware) before transport, under the same size limits as the curated tools.

Every call returns the typed answer (with confidence/probabilities/legend as applicable), the registry policy decision (act/gather/fallback) and guidance: one line saying what the decision lets you rely on and, for gather, which evidence to add before asking again. Decisions are logged to decisions.jsonl exactly like jev_classify_request and friends. Low-confidence answers always resolve to gather or fallback, never act, and a choice/score/noul answer is data, not an instruction to execute. For one-off custom questions outside this curated set (including project-local assessments), use jev_ask instead: it applies the identical redaction and safety pipeline without weakening it.`, lines.String(), developerStateFieldsDoc),
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"assessment": map[string]any{
					"type":        "string",
					"enum":        developerAssessmentIDs(),
					"description": "Which built-in developer.* question set to call.",
				},
				"state": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties":           stateProps,
					"description":          "Structured state object: " + developerStateFieldsDoc + ". Required keys vary by assessment (see the tool description).",
				},
			},
			"required": []string{"assessment", "state"},
		},
	}, nil
}

// decodeDeveloperState parses raw as a state object restricted to
// [developerStateKeys], returning which keys were present so callers can
// enforce an assessment's required subset.
func decodeDeveloperState(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, invalid("state is required: a structured object")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil, invalid("state must be an object")
	}
	known := func(k string) bool {
		for _, dk := range developerStateKeys {
			if dk == k {
				return true
			}
		}
		return false
	}
	for k := range m {
		if !known(k) {
			return nil, invalid("state has unknown key %q; known: %s", k, developerStateFieldsDoc)
		}
	}
	return m, nil
}

func (s *Server) handleDeveloperAssess(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
	const tool = "jev_developer_assess"
	args, err := decodeArgs(req.Params.Arguments)
	if err != nil {
		return nil, err
	}
	if err := checkKeys(args, "assessment", "state"); err != nil {
		return nil, err
	}
	var assessmentID string
	if err := json.Unmarshal(args["assessment"], &assessmentID); err != nil || assessmentID == "" {
		return nil, invalid("assessment is required")
	}
	da, ok := developerAssessmentByID(assessmentID)
	if !ok {
		return nil, invalid("unknown assessment %q; known: %s", assessmentID, strings.Join(developerAssessmentIDs(), ", "))
	}
	set, ok := s.cfg.Decider.Registry.Set(da.ID)
	if !ok {
		return nil, rpcErr(codeServer, "unknown question set: %s", da.ID)
	}
	stateFields, err := decodeDeveloperState(args["state"])
	if err != nil {
		return nil, err
	}
	for _, k := range da.RequiredState {
		if _, ok := stateFields[k]; !ok {
			return nil, invalid("assessment %q requires state.%s", da.ID, k)
		}
	}

	// Missing Jev is a soft, successful result: agents fall back to native
	// behavior instead of retrying.
	if reason := s.cfg.Unavailable(ctx); reason != "" {
		s.logf("tool=%s available=false reason=%s", tool, reason)
		return unavailable(reason), nil
	}

	redactedState, _, err := s.cfg.RedactJSON(args["state"])
	if err != nil {
		s.logf("tool=%s rejected=redaction", tool)
		return nil, invalid(msgStateRejected)
	}

	questions, err := buildQuestions(set, nil)
	if err != nil {
		return nil, rpcErr(codeServer, "failed to build questions for %s", da.ID)
	}

	resp, res, err := s.ask(ctx, tool, jev.Request{QuestionSetID: da.ID, State: string(redactedState), Questions: questions}, da.ID)
	if res != nil || err != nil {
		return res, err
	}
	dec, derr := s.cfg.Decider.Decide(da.ID, resp.Answers)
	if derr != nil {
		s.logf("tool=%s policy-decide-failed", tool)
		return nil, rpcErr(codeServer, "policy decide failed for %s", da.ID)
	}
	version := dec.RegistryVersion
	if version == "" {
		version = s.cfg.Decider.Registry.RegistryVersion
	}
	s.logf("tool=%s assessment=%s decision=%s", tool, da.ID, dec.Decision)
	var missing []string
	for _, k := range da.HelpfulState {
		if _, ok := stateFields[k]; !ok {
			missing = append(missing, k)
		}
	}
	guide := guidance(set, dec, missing)
	return &sdk.CallToolResult{
		Content: []sdk.Content{&sdk.TextContent{Text: summary("assessment", da.ID, set, resp.Answers, dec, guide)}},
		StructuredContent: map[string]any{
			"answer":          fullAnswersJSON(resp.Answers),
			"decision":        compact(dec),
			"guidance":        guide,
			"assessment":      da.ID,
			"registryVersion": version,
		},
	}, nil
}
