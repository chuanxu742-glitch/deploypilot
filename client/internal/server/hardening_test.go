package server

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateUTF8RuneBoundary(t *testing.T) {
	// A string of multi-byte runes (each "世" is 3 bytes).
	s := strings.Repeat("世", 100) // 300 bytes
	for _, max := range []int{0, 1, 2, 3, 4, 100, 299, 300, 301} {
		got := truncateUTF8(s, max)
		if len(got) > max {
			t.Fatalf("max=%d: result %d bytes exceeds limit", max, len(got))
		}
		if !utf8.ValidString(got) {
			t.Fatalf("max=%d: result is not valid UTF-8: %q", max, got)
		}
		if max >= len(s) && got != s {
			t.Fatalf("max=%d: should return the whole string unchanged", max)
		}
	}
}

func TestTruncateUTF8ASCIIUnchanged(t *testing.T) {
	s := "hello world"
	if got := truncateUTF8(s, 100); got != s {
		t.Fatalf("short ASCII string was altered: %q", got)
	}
	if got := truncateUTF8(s, 5); got != "hello" {
		t.Fatalf("ASCII truncation = %q, want %q", got, "hello")
	}
}
