package jev

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestQuestionWireExamples is a table-driven port of the documented Choice,
// Noul and Score criteria and state/instructions shapes: a plain string (the
// ergonomic default) and literal JSON text (object/array/null) must both
// reach the wire unchanged in kind.
func TestQuestionWireExamples(t *testing.T) {
	tests := []struct {
		name string
		q    Question
		want string
	}{
		{
			"choice with plain string criteria",
			ChoiceQuestion{Instructions: "which?", Criteria: map[string]json.RawMessage{"a": Str("option a")}},
			`{"type":"choice","instructions":"which?","criteria":{"a":"option a"}}`,
		},
		{
			"choice with null criteria",
			ChoiceQuestion{Instructions: "which?", Criteria: map[string]json.RawMessage{"a": Null()}},
			`{"type":"choice","instructions":"which?","criteria":{"a":null}}`,
		},
		{
			"choice with object criteria",
			ChoiceQuestion{Instructions: "which?", Criteria: map[string]json.RawMessage{"a": json.RawMessage(`{"weight":1}`)}},
			`{"type":"choice","instructions":"which?","criteria":{"a":{"weight":1}}}`,
		},
		{
			"choice with array criteria",
			ChoiceQuestion{Instructions: "which?", Criteria: map[string]json.RawMessage{"a": json.RawMessage(`["x","y"]`)}},
			`{"type":"choice","instructions":"which?","criteria":{"a":["x","y"]}}`,
		},
		{
			"choice with JSON-object instructions",
			ChoiceQuestion{Instructions: `{"goal":"pick"}`, Criteria: map[string]json.RawMessage{"a": Null()}},
			`{"type":"choice","instructions":{"goal":"pick"},"criteria":{"a":null}}`,
		},
		{
			"choice with JSON-array instructions",
			ChoiceQuestion{Instructions: `["pick","one"]`, Criteria: map[string]json.RawMessage{"a": Null()}},
			`{"type":"choice","instructions":["pick","one"],"criteria":{"a":null}}`,
		},
		{
			"noul with no criteria",
			NoulQuestion{Instructions: "is it?"},
			`{"type":"noul","instructions":"is it?"}`,
		},
		{
			"noul with true/false criteria",
			NoulQuestion{Instructions: "is it?", Criteria: map[string]json.RawMessage{"true": Str("yes"), "false": Str("no")}},
			`{"type":"noul","instructions":"is it?","criteria":{"false":"no","true":"yes"}}`,
		},
		{
			"noul with object criteria value",
			NoulQuestion{Instructions: "is it?", Criteria: map[string]json.RawMessage{"true": json.RawMessage(`{"rubric":"strict"}`)}},
			`{"type":"noul","instructions":"is it?","criteria":{"true":{"rubric":"strict"}}}`,
		},
		{
			"score with ordered levels",
			ScoreQuestion{Instructions: "how much?", Criteria: []json.RawMessage{Str("low"), Str("medium"), Str("high")}},
			`{"type":"score","instructions":"how much?","criteria":["low","medium","high"]}`,
		},
		{
			"score with object levels",
			ScoreQuestion{Instructions: "how much?", Criteria: []json.RawMessage{json.RawMessage(`{"label":"low"}`), json.RawMessage(`{"label":"high"}`)}},
			`{"type":"score","instructions":"how much?","criteria":[{"label":"low"},{"label":"high"}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := json.Marshal(tt.q)
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != tt.want {
				t.Errorf("got  %s\nwant %s", b, tt.want)
			}
		})
	}
}

func TestRequestStateWireShapes(t *testing.T) {
	tests := []struct {
		name  string
		state string
		want  string
	}{
		{"plain string state is the ergonomic default", "hello world", `"hello world"`},
		{"literal JSON object state", `{"k":"v"}`, `{"k":"v"}`},
		{"literal JSON array state", `["a","b"]`, `["a","b"]`},
		{"literal JSON string state", `"already quoted"`, `"already quoted"`},
		{"text that merely looks numeric stays a plain string", "42", `"42"`},
		{"empty state", "", `""`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := Request{State: tt.state, Questions: map[string]Question{"q": NoulQuestion{Instructions: "?"}}}
			b, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]json.RawMessage
			if err := json.Unmarshal(b, &wire); err != nil {
				t.Fatal(err)
			}
			if string(wire["state"]) != tt.want {
				t.Errorf("state = %s, want %s", wire["state"], tt.want)
			}
		})
	}
}

func TestQuestionSchemaValidation(t *testing.T) {
	number := json.RawMessage(`5`)
	boolean := json.RawMessage(`true`)
	tests := []struct {
		name    string
		q       Question
		wantErr bool
	}{
		{"choice with zero criteria is rejected", ChoiceQuestion{Instructions: "?"}, true},
		{"choice with one criteria entry is accepted", ChoiceQuestion{Instructions: "?", Criteria: map[string]json.RawMessage{"a": Null()}}, false},
		{"choice criteria key must not be empty", ChoiceQuestion{Instructions: "?", Criteria: map[string]json.RawMessage{"": Null()}}, true},
		{"choice criteria value must be string/object/array/null, not number", ChoiceQuestion{Instructions: "?", Criteria: map[string]json.RawMessage{"a": number}}, true},
		{"choice criteria value must be string/object/array/null, not bool", ChoiceQuestion{Instructions: "?", Criteria: map[string]json.RawMessage{"a": boolean}}, true},
		{"noul with no criteria is accepted", NoulQuestion{Instructions: "?"}, false},
		{"noul criteria key must be true or false", NoulQuestion{Instructions: "?", Criteria: map[string]json.RawMessage{"maybe": Str("x")}}, true},
		{"noul criteria true key is accepted", NoulQuestion{Instructions: "?", Criteria: map[string]json.RawMessage{"true": Str("x")}}, false},
		{"noul criteria value must not be a number", NoulQuestion{Instructions: "?", Criteria: map[string]json.RawMessage{"true": number}}, true},
		{"score with one level is rejected (min 2)", ScoreQuestion{Instructions: "?", Criteria: []json.RawMessage{Str("low")}}, true},
		{"score with two levels is accepted", ScoreQuestion{Instructions: "?", Criteria: []json.RawMessage{Str("low"), Str("high")}}, false},
		{"score with eleven levels is rejected (max 10)", ScoreQuestion{Instructions: "?", Criteria: manyStr(11)}, true},
		{"score with ten levels is accepted", ScoreQuestion{Instructions: "?", Criteria: manyStr(10)}, false},
		{"score level must not be null", ScoreQuestion{Instructions: "?", Criteria: []json.RawMessage{Str("low"), Null()}}, true},
		{"score level must not be a number", ScoreQuestion{Instructions: "?", Criteria: []json.RawMessage{Str("low"), number}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.q.validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("validate() = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func manyStr(n int) []json.RawMessage {
	out := make([]json.RawMessage, n)
	for i := range out {
		out[i] = Str(string(rune('a' + i)))
	}
	return out
}

func TestChoiceMaxCriteriaCap(t *testing.T) {
	criteria := make(map[string]json.RawMessage, MaxChoiceOptions+1)
	for i := 0; i <= MaxChoiceOptions; i++ {
		criteria[strings.Repeat("x", i+1)] = Null()
	}
	if err := (ChoiceQuestion{Instructions: "?", Criteria: criteria}).validate(); err == nil {
		t.Error("expected rejection over the 255-entry cap")
	}
	delete(criteria, strings.Repeat("x", MaxChoiceOptions+1))
	if err := (ChoiceQuestion{Instructions: "?", Criteria: criteria}).validate(); err != nil {
		t.Errorf("255 entries should be accepted: %v", err)
	}
}

// TestResponseRoundTrip decodes Choice and Score answers carrying the full
// documented response shape: Choice probabilities/confidence, and Score
// legend/distribution alongside score/confidence.
func TestResponseRoundTrip(t *testing.T) {
	body := `{"model":"jev-1.13.0","answers":{
		"pick":{"choice":"a","probabilities":{"a":0.7,"b":0.3},"confidence":0.7},
		"rate":{"score":0.66,"confidence":0.8,"legend":["low","medium","high"],"distribution":{"low":0.1,"medium":0.6,"high":0.3}}
	}}`
	resp, err := DecodeResponse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	pick, ok := resp.Answers["pick"].(ChoiceAnswer)
	if !ok || pick.Choice != "a" || pick.Confidence != 0.7 || pick.Probabilities["b"] != 0.3 {
		t.Errorf("pick = %#v", resp.Answers["pick"])
	}
	rate, ok := resp.Answers["rate"].(ScoreAnswer)
	if !ok || rate.Score != 0.66 || rate.Confidence != 0.8 {
		t.Fatalf("rate = %#v", resp.Answers["rate"])
	}
	if want := []string{"low", "medium", "high"}; len(rate.Legend) != 3 || rate.Legend[0] != want[0] || rate.Legend[2] != want[2] {
		t.Errorf("legend = %v", rate.Legend)
	}
	if rate.Distribution["medium"] != 0.6 {
		t.Errorf("distribution = %v", rate.Distribution)
	}
}

func TestResponseRoundTripWithoutLegendOrDistribution(t *testing.T) {
	resp, err := DecodeResponse([]byte(`{"answers":{"rate":{"score":0.5,"confidence":0.4}}}`))
	if err != nil {
		t.Fatal(err)
	}
	rate := resp.Answers["rate"].(ScoreAnswer)
	if rate.Score != 0.5 || rate.Legend != nil || rate.Distribution != nil {
		t.Errorf("rate = %#v", rate)
	}
}

func TestScoreAnswerPreservesUnknownLegendShape(t *testing.T) {
	resp, err := DecodeResponse([]byte(`{"answers":{"rate":{"score":0.4,"confidence":0.9,"legend":{"low":"sparse","high":"dense"},"distribution":{"low":0.6,"high":0.4}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	rate := resp.Answers["rate"].(ScoreAnswer)
	if rate.Score != 0.4 || len(rate.Legend) != 0 || len(rate.RawLegend) == 0 || rate.Distribution["low"] != 0.6 {
		t.Fatalf("score response shape lost: %+v", rate)
	}
}
