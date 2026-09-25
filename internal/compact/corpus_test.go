package compact

import (
	"path/filepath"
	"testing"
)

func TestSyntheticCorpusReportsRecallWithSavings(t *testing.T) {
	cases, err := ReadCorpus(filepath.Join("..", "..", "testdata", "compact-corpus"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 12 {
		t.Fatalf("corpus cases=%d", len(cases))
	}
	m := EvaluateCorpus(cases, func(c CorpusCase) string {
		r := Compact(c.Command, c.Original(), "", c.Exit, Options{})
		if r.Compacted {
			return r.Stdout
		}
		return c.Original()
	})
	if m.Cases != 12 || m.BytesBefore <= m.BytesAfter || m.FailureRecall() < 1 || m.Recall() < 0.95 {
		t.Fatalf("metrics=%+v", m)
	}
}

func TestSyntheticCorpusEvaluatesLiveDecisionPathOffline(t *testing.T) {
	cases, err := ReadCorpus(filepath.Join("..", "..", "testdata", "compact-corpus"))
	if err != nil {
		t.Fatal(err)
	}
	modelCalls := 0
	m := EvaluateCorpus(cases, func(c CorpusCase) string {
		original := c.Original()
		a := &decidingAsker{}
		jr, _ := JevCompact(c.Command, original, "", c.Exit, a, JevOptions{
			Enabled: true, RawPointer: rawFixture(t, original), AuthoritativeExit: true,
		})
		modelCalls += len(a.requests)
		return jr.Body
	})
	if modelCalls == 0 || m.FailureRecall() != 1 || m.Recall() < 0.95 || m.Saved() <= 0 || m.Promotable() {
		t.Fatalf("model path metrics=%+v calls=%d", m, modelCalls)
	}
}

func BenchmarkCompactionSyntheticCorpus(b *testing.B) {
	cases, err := ReadCorpus(filepath.Join("..", "..", "testdata", "compact-corpus"))
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, c := range cases {
			Compact(c.Command, c.Original(), "", c.Exit, Options{})
		}
	}
}
