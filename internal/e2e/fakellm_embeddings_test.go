package e2e

import "testing"

func TestFakeEmbeddingDeterministicAndTokenAware(t *testing.T) {
	a1 := fakeEmbedding("the quick brown fox", 64)
	a2 := fakeEmbedding("the quick brown fox", 64)
	if len(a1) != 64 {
		t.Fatalf("dimension = %d", len(a1))
	}
	for i := range a1 {
		if a1[i] != a2[i] {
			t.Fatalf("embedding not deterministic at %d", i)
		}
	}

	cos := func(a, b []float32) float64 {
		var dot float64
		for i := range a {
			dot += float64(a[i]) * float64(b[i])
		}
		return dot // inputs are L2-normalized
	}
	query := fakeEmbedding("quick brown fox", 64)
	related := fakeEmbedding("a quick brown fox jumps", 64)
	unrelated := fakeEmbedding("completely different topic entirely", 64)
	if cos(query, related) <= cos(query, unrelated) {
		t.Fatalf("token overlap does not raise similarity: related=%f unrelated=%f",
			cos(query, related), cos(query, unrelated))
	}

	// Empty input still yields a unit-ish vector rather than zeros.
	empty := fakeEmbedding("", 8)
	var norm float64
	for _, v := range empty {
		norm += float64(v) * float64(v)
	}
	if norm == 0 {
		t.Fatal("empty input produced a zero vector")
	}
}
