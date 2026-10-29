package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFilePaging(t *testing.T) {
	ex := NewShellExecutor()
	dir := t.TempDir()
	p := filepath.Join(dir, "data.txt")
	content := strings.Repeat("abcdefghij", 5000) // 50000 bytes
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// First page with an explicit small max.
	r, err := ex.ReadFile(p, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Content) != 100 || r.Size != int64(len(content)) || r.EOF {
		t.Fatalf("unexpected first page: len=%d size=%d eof=%v", len(r.Content), r.Size, r.EOF)
	}
	if r.NextOffset != 100 {
		t.Fatalf("next offset = %d", r.NextOffset)
	}

	// Continue from next offset.
	r2, err := ex.ReadFile(p, r.NextOffset, 100)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Content != content[100:200] {
		t.Fatalf("second page mismatch")
	}

	// Hard cap applies.
	r3, err := ex.ReadFile(p, 0, 10*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(r3.Content) != len(content) || !r3.EOF {
		t.Fatalf("whole read: len=%d eof=%v", len(r3.Content), r3.EOF)
	}
}

func TestReadFileDirErrors(t *testing.T) {
	ex := NewShellExecutor()
	if _, err := ex.ReadFile(t.TempDir(), 0, 0); err == nil {
		t.Fatal("reading a directory should error")
	}
}

func TestEditFileUnique(t *testing.T) {
	ex := NewShellExecutor()
	p := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(p, []byte("port: 8000\nhost: 0.0.0.0\n"), 0o644)

	n, err := ex.EditFile(p, "port: 8000", "port: 9000", false)
	if err != nil || n != 1 {
		t.Fatalf("edit: n=%d err=%v", n, err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "port: 9000") || strings.Contains(string(b), "8000") {
		t.Fatalf("content after edit: %q", b)
	}
}

func TestEditFileAmbiguous(t *testing.T) {
	ex := NewShellExecutor()
	p := filepath.Join(t.TempDir(), "f.txt")
	os.WriteFile(p, []byte("x\nx\n"), 0o644)
	if _, err := ex.EditFile(p, "x", "y", false); err == nil {
		t.Fatal("ambiguous edit must error unless replace_all")
	}
	n, err := ex.EditFile(p, "x", "y", true)
	if err != nil || n != 2 {
		t.Fatalf("replace_all: n=%d err=%v", n, err)
	}
}

func TestEditFileNotFound(t *testing.T) {
	ex := NewShellExecutor()
	p := filepath.Join(t.TempDir(), "f.txt")
	os.WriteFile(p, []byte("hello"), 0o644)
	if _, err := ex.EditFile(p, "nope", "y", false); err == nil {
		t.Fatal("missing old_string must error")
	}
}

func TestAppendFile(t *testing.T) {
	ex := NewShellExecutor()
	p := filepath.Join(t.TempDir(), "sub", "env")
	if err := ex.AppendFile(p, "A=1\n"); err != nil {
		t.Fatal(err)
	}
	if err := ex.AppendFile(p, "B=2\n"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "A=1\nB=2\n" {
		t.Fatalf("append result: %q", b)
	}
}
