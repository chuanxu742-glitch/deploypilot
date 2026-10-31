package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

func utf16LE(s string) []byte {
	out := []byte{0xFF, 0xFE}
	for _, u := range utf16.Encode([]rune(s)) {
		out = append(out, byte(u), byte(u>>8))
	}
	return out
}

// A live run wrote a listing with Windows PowerShell's Out-File (UTF-16LE with
// a BOM) and read_file rejected it as binary.
func TestReadFileDecodesUTF16WithBOM(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "list.txt")
	text := "FullName  Length\r\nC:\\work\\app.py  15\r\n部署 ✓ 😀\r\n"
	if err := os.WriteFile(path, utf16LE(text), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := NewShellExecutor().ReadFile(path, 0, 0)
	if err != nil {
		t.Fatalf("UTF-16 text rejected: %v", err)
	}
	if r.Content != text || !r.EOF {
		t.Fatalf("decoded %q eof=%v, want %q", r.Content, r.EOF, text)
	}

	// Paging in small pieces reassembles the text, including the surrogate pair.
	var got strings.Builder
	offset := int64(0)
	for i := 0; i < 100; i++ {
		page, err := NewShellExecutor().ReadFile(path, offset, 6)
		if err != nil {
			t.Fatal(err)
		}
		got.WriteString(page.Content)
		if page.EOF {
			break
		}
		if page.NextOffset <= offset {
			t.Fatalf("paging stalled at %d", offset)
		}
		offset = page.NextOffset
	}
	if got.String() != text {
		t.Fatalf("paged decode %q, want %q", got.String(), text)
	}
}

func TestReadFileStillRejectsBinary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blob.bin")
	if err := os.WriteFile(path, []byte{0x7f, 'E', 'L', 'F', 0, 0, 1, 2}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewShellExecutor().ReadFile(path, 0, 0); err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("binary file accepted: %v", err)
	}
}
