package registry

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestEmbeddedRegistryValidates(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if r.RegistryVersion == "" || len(r.IDs()) == 0 {
		t.Fatalf("empty registry: %+v", r)
	}
	for _, id := range r.IDs() {
		s, _ := r.Set(id)
		if s.Policy.ActThreshold <= s.Policy.EscalateThreshold {
			t.Errorf("%s: act %v <= escalate %v", id, s.Policy.ActThreshold, s.Policy.EscalateThreshold)
		}
	}
}

func TestEmbeddedFileMatchesDisk(t *testing.T) {
	disk, err := os.ReadFile("questions.registry.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(disk) != string(registryJSON) {
		t.Fatal("embedded registry differs from file")
	}
}

// mutate parses the embedded registry generically, applies fn and re-marshals.
func mutate(t *testing.T, fn func(sets map[string]any)) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(registryJSON, &doc); err != nil {
		t.Fatal(err)
	}
	fn(doc["questionSets"].(map[string]any))
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func firstSet(sets map[string]any) map[string]any {
	for _, v := range sets {
		return v.(map[string]any)
	}
	return nil
}

func TestParseRejects(t *testing.T) {
	tests := []struct {
		name string
		raw  func(t *testing.T) []byte
		want string
	}{
		{"not json", func(*testing.T) []byte { return []byte("{") }, "registry"},
		{"missing version", func(*testing.T) []byte { return []byte(`{"questionSets":{}}`) }, "schema"},
		{"no sets", func(*testing.T) []byte { return []byte(`{"registryVersion":"1","questionSets":{}}`) }, "no question sets"},
		{"unknown surface", func(t *testing.T) []byte {
			return mutate(t, func(s map[string]any) { firstSet(s)["surface"] = "bogus" })
		}, "schema"},
		{"threshold out of range", func(t *testing.T) []byte {
			return mutate(t, func(s map[string]any) { firstSet(s)["policy"].(map[string]any)["actThreshold"] = 1.5 })
		}, "schema"},
		{"extra key", func(t *testing.T) []byte {
			return mutate(t, func(s map[string]any) { firstSet(s)["extra"] = true })
		}, "schema"},
		{"key id mismatch", func(t *testing.T) []byte {
			return mutate(t, func(s map[string]any) { firstSet(s)["id"] = "other" })
		}, "does not match id"},
		{"unknown primary", func(t *testing.T) []byte {
			return mutate(t, func(s map[string]any) { firstSet(s)["policy"].(map[string]any)["primaryQuestion"] = "nope" })
		}, "primaryQuestion"},
		{"act not above escalate", func(t *testing.T) []byte {
			return mutate(t, func(s map[string]any) {
				p := firstSet(s)["policy"].(map[string]any)
				p["actThreshold"], p["escalateThreshold"] = 0.5, 0.5
			})
		}, "exceed"},
		{"score legend too short", func(t *testing.T) []byte {
			return mutate(t, func(s map[string]any) {
				q := firstSet(s)["questions"].(map[string]any)
				for _, v := range q {
					v.(map[string]any)["type"] = "score"
					v.(map[string]any)["criteria"] = []string{"only"}
				}
			})
		}, "score criteria"},
		{"noul bad label", func(t *testing.T) []byte {
			return mutate(t, func(s map[string]any) {
				q := firstSet(s)["questions"].(map[string]any)
				for _, v := range q {
					v.(map[string]any)["type"] = "noul"
					v.(map[string]any)["criteria"] = map[string]string{"maybe": "x"}
				}
			})
		}, "noul criteria"},
		{"choice criteria array", func(t *testing.T) []byte {
			return mutate(t, func(s map[string]any) {
				q := firstSet(s)["questions"].(map[string]any)
				for _, v := range q {
					v.(map[string]any)["type"] = "choice"
					v.(map[string]any)["criteria"] = []string{"a", "b"}
				}
			})
		}, "choice criteria"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.raw(t))
			if err == nil {
				t.Fatal("want error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q lacks %q", err, tt.want)
			}
		})
	}
}

func TestThreshold(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	id := r.IDs()[0]
	s, _ := r.Set(id)
	if got, err := r.Threshold(id, "act"); err != nil || got != s.Policy.ActThreshold {
		t.Errorf("act = %v, %v", got, err)
	}
	if got, err := r.Threshold(id, "escalate"); err != nil || got != s.Policy.EscalateThreshold {
		t.Errorf("escalate = %v, %v", got, err)
	}
	if _, err := r.Threshold(id, "other"); err == nil {
		t.Error("bad which accepted")
	}
	if _, err := r.Threshold("nope", "act"); err == nil {
		t.Error("unknown set accepted")
	}
}
