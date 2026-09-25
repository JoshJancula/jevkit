package redact

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OWNER/jevkit/internal/jev"
)

func TestApplyJSONPreservesValidJSON(t *testing.T) {
	r := mustNew(t, Options{})
	tests := []struct {
		name string
		in   string
	}{
		{"object with nested array", `{"a":"clean text","b":[1,2,"clean too"],"c":null,"d":true,"e":3.5}`},
		{"array of objects", `[{"x":"one"},{"x":"two"}]`},
		{"plain string", `"just a string"`},
		{"number", `42`},
		{"empty object", `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, _, err := r.ApplyJSON([]byte(tt.in))
			if err != nil {
				t.Fatal(err)
			}
			if !json.Valid(out) {
				t.Fatalf("output is not valid JSON: %s", out)
			}
			var got, want any
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tt.in), &want); err != nil {
				t.Fatal(err)
			}
			gb, _ := json.Marshal(got)
			wb, _ := json.Marshal(want)
			if string(gb) != string(wb) {
				t.Errorf("shape changed: got %s want %s", gb, wb)
			}
		})
	}
}

func TestApplyJSONRedactsRecursively(t *testing.T) {
	const secret = "typesafe-secret-zzz-777"
	r := mustNew(t, Options{Key: secret})
	in := `{"instructions":"header ` + secret + ` trailing","criteria":{"a":["nested ` + secret + ` value"],"b":{"deep":"more ` + secret + ` here"}}}`
	out, hits, err := r.ApplyJSON([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), secret) {
		t.Fatalf("secret survived: %s", out)
	}
	if !json.Valid(out) {
		t.Fatalf("output is not valid JSON: %s", out)
	}
	if len(hits) == 0 {
		t.Error("expected at least one hit")
	}
}

func TestApplyJSONRedactsSemanticKeys(t *testing.T) {
	const secret = "typesafe-secret-key-in-name-000000"
	r := mustNew(t, Options{Key: secret})
	in := `{"` + secret + `":"value"}`
	out, _, err := r.ApplyJSON([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), secret) {
		t.Fatalf("secret survived in a key: %s", out)
	}
}

func TestApplyJSONKeyCollisionFailsClosed(t *testing.T) {
	r := mustNew(t, Options{})
	// Two distinct GitHub tokens are each, in full, a HARD-redacted match: the
	// whole key becomes the same marker, silently dropping one entry unless
	// ApplyJSON detects the collision and fails closed.
	in := `{"ghp_aaaaaaaaaaaaaaaaaaaaaaaa":"a","ghp_bbbbbbbbbbbbbbbbbbbbbbbb":"b"}`
	if _, _, err := r.ApplyJSON([]byte(in)); err == nil {
		t.Fatal("expected a collision error")
	} else if jev.CodeOf(err) != jev.CodeRejected {
		t.Errorf("err = %v, want CodeRejected", err)
	}
}

func TestApplyJSONInvalidJSONRejected(t *testing.T) {
	r := mustNew(t, Options{})
	if _, _, err := r.ApplyJSON([]byte(`{not json`)); jev.CodeOf(err) != jev.CodeRejected {
		t.Errorf("err = %v, want CodeRejected", err)
	}
}

// TestSeededSecretNeverReachesFakeTransport builds a jev.ChoiceQuestion whose
// criteria carry a seeded secret inside nested JSON, redacts it with
// ApplyJSON, sends it through a real jev.Client against a local test server
// (the transport), and asserts the secret is absent from the request body
// the server received, and from any returned error.
func TestSeededSecretNeverReachesFakeTransport(t *testing.T) {
	const secret = "typesafe-secret-must-not-leak-424242"
	r := mustNew(t, Options{Key: secret})

	criteriaJSON := `{"a":{"rationale":"because ` + secret + ` triggered it"},"b":"clean"}`
	redacted, _, err := r.ApplyJSON([]byte(criteriaJSON))
	if err != nil {
		t.Fatal(err)
	}
	var criteria map[string]json.RawMessage
	if err := json.Unmarshal(redacted, &criteria); err != nil {
		t.Fatal(err)
	}

	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		bodies = append(bodies, b)
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `{"answers":{"answer":{"choice":"a","confidence":0.5}}}`)
	}))
	defer srv.Close()

	client := jev.New(jev.Config{Endpoint: srv.URL, Model: "jev-latest", Timeout: 2000000000, MaxRetries: 0},
		func() (string, error) { return secret, nil })
	req := jev.Request{
		State:     "clean state",
		Questions: map[string]jev.Question{"answer": jev.ChoiceQuestion{Instructions: "which?", Criteria: criteria}},
	}
	resp, err := client.Ask(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := resp.Answers["answer"]; !ok {
		t.Fatal("missing answer")
	}
	if len(bodies) != 1 {
		t.Fatalf("calls = %d", len(bodies))
	}
	if strings.Contains(string(bodies[0]), secret) {
		t.Fatalf("secret reached the transport: %s", bodies[0])
	}
}
