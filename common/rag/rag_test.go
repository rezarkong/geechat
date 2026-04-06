package rag

import (
	"testing"
	"unicode/utf8"
)

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

func TestSplitTextUsesByteLimit(t *testing.T) {
	text := "你好世界你好世界"

	chunks := splitText(text, 7, 2)

	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for i, chunk := range chunks {
		if !utf8.ValidString(chunk) {
			t.Fatalf("chunk %d is not valid utf8: %q", i, chunk)
		}
		if len(chunk) > 7 {
			t.Fatalf("chunk %d exceeds byte limit: %d", i, len(chunk))
		}
	}
}
