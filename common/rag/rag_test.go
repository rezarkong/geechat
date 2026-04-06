package rag

import "testing"

func TestSplitTextPreservesWhitespace(t *testing.T) {
	text := "  abc\n"

	chunks := splitText(text, 100, 20)

	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0] != text {
		t.Fatalf("expected chunk %q, got %q", text, chunks[0])
	}
}

func TestSplitTextHandlesOverlapEqualChunkSize(t *testing.T) {
	text := "abcdefghij"

	chunks := splitText(text, 5, 5)

	expected := []string{"abcde", "fghij"}
	if len(chunks) != len(expected) {
		t.Fatalf("expected %d chunks, got %d: %#v", len(expected), len(chunks), chunks)
	}
	for i := range expected {
		if chunks[i] != expected[i] {
			t.Fatalf("chunk %d: expected %q, got %q", i, expected[i], chunks[i])
		}
	}
}
