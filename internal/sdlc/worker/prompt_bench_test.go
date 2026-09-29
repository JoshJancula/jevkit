package worker

import "testing"

func BenchmarkBuildPromptFixedFixture(b *testing.B) {
	req := fixedPromptFixture()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		layout := buildPrompt(req)
		if layout.StablePrefixBytes == 0 || layout.StablePrefixFingerprint == "" {
			b.Fatal("missing stable prefix telemetry")
		}
	}
}
