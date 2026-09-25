package compact

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CorpusCase is one labeled original. Repeat expands synthetic noise in memory
// so committed fixtures stay small. Gold contains exact diagnostic source lines.
type CorpusCase struct {
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Command     string   `json:"command"`
	Exit        int      `json:"exit"`
	Prefix      string   `json:"prefix"`
	Repeat      string   `json:"repeat"`
	RepeatCount int      `json:"repeatCount"`
	Suffix      string   `json:"suffix"`
	Gold        []string `json:"gold"`
}

func (c CorpusCase) Original() string {
	return c.Prefix + strings.Repeat(c.Repeat, c.RepeatCount) + c.Suffix
}

func ReadCorpus(dir string) ([]CorpusCase, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	cases := make([]CorpusCase, 0, len(paths))
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var c CorpusCase
		if err := json.Unmarshal(b, &c); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if c.Name == "" || c.Command == "" || len(c.Gold) == 0 || c.RepeatCount < 0 {
			return nil, fmt.Errorf("%s: incomplete corpus label", path)
		}
		cases = append(cases, c)
	}
	return cases, nil
}

type CorpusMetrics struct {
	Cases               int `json:"cases"`
	Failures            int `json:"failures"`
	GoldLines           int `json:"goldLines"`
	RetainedGold        int `json:"retainedGold"`
	FailureGoldLines    int `json:"failureGoldLines"`
	RetainedFailureGold int `json:"retainedFailureGold"`
	BytesBefore         int `json:"bytesBefore"`
	BytesAfter          int `json:"bytesAfter"`
}

func (m CorpusMetrics) Recall() float64 {
	if m.GoldLines == 0 {
		return 1
	}
	return float64(m.RetainedGold) / float64(m.GoldLines)
}
func (m CorpusMetrics) FailureRecall() float64 {
	if m.FailureGoldLines == 0 {
		return 1
	}
	return float64(m.RetainedFailureGold) / float64(m.FailureGoldLines)
}
func (m CorpusMetrics) Saved() int { return m.BytesBefore - m.BytesAfter }
func (m CorpusMetrics) Promotable() bool {
	return m.Cases >= 300 && m.FailureRecall() == 1 && m.Recall() >= 0.95
}

// EvaluateCorpus reports recall and savings jointly for a candidate compactor.
func EvaluateCorpus(cases []CorpusCase, compactFn func(CorpusCase) string) CorpusMetrics {
	var m CorpusMetrics
	for _, c := range cases {
		original, out := c.Original(), compactFn(c)
		m.Cases++
		m.BytesBefore += len(original)
		m.BytesAfter += len(out)
		if c.Exit != 0 {
			m.Failures++
		}
		retained := make(map[string]bool)
		for _, line := range splitLines(out) {
			retained[line] = true
		}
		for _, gold := range c.Gold {
			m.GoldLines++
			if c.Exit != 0 {
				m.FailureGoldLines++
			}
			if retained[gold] {
				m.RetainedGold++
				if c.Exit != 0 {
					m.RetainedFailureGold++
				}
			}
		}
	}
	return m
}
