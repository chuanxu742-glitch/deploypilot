package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestEditFileFollowsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "available.conf")
	link := filepath.Join(dir, "enabled.conf")
	if err := os.WriteFile(target, []byte("listen 80;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}
	if _, err := NewShellExecutor().EditFile(link, "listen 80;", "listen 8080;", false); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("edit replaced the symlink with a regular file")
	}
	data, _ := os.ReadFile(target)
	if !strings.Contains(string(data), "listen 8080;") {
		t.Fatalf("real file not edited: %q", data)
	}
}

func TestEditFileLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "app.env")
	if err := os.WriteFile(p, []byte("PORT=1\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := NewShellExecutor().EditFile(p, "PORT=1", "PORT=2", false); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("temp files left behind: %v", entries)
	}
	for _, entry := range entries {
		if entry.Name() != "app.env" && !strings.HasPrefix(entry.Name(), "app.env.bak-") {
			t.Fatalf("unexpected temporary file: %s", entry.Name())
		}
	}
}

func TestReadFileRejectsBinary(t *testing.T) {
	p := filepath.Join(t.TempDir(), "model.bin")
	if err := os.WriteFile(p, []byte{0x7f, 'E', 'L', 'F', 0, 0, 1, 2}, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := NewShellExecutor().ReadFile(p, 0, 0)
	if err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("binary file was returned as text: %v", err)
	}
}

func TestReadFilePagesOnRuneBoundaries(t *testing.T) {
	content := "a" + strings.Repeat("世界", 50) // 1 + 300 bytes; pages of 10 split runes
	p := filepath.Join(t.TempDir(), "zh.txt")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	ex := NewShellExecutor()
	var got strings.Builder
	offset := int64(0)
	for pages := 0; pages < 100; pages++ {
		r, err := ex.ReadFile(p, offset, 10)
		if err != nil {
			t.Fatal(err)
		}
		if !utf8.ValidString(r.Content) {
			t.Fatalf("page at %d split a character: %q", offset, r.Content)
		}
		got.WriteString(r.Content)
		if r.EOF {
			break
		}
		if r.NextOffset <= offset {
			t.Fatal("paging made no progress")
		}
		offset = r.NextOffset
	}
	if got.String() != content {
		t.Fatal("paged content does not reassemble the file")
	}
}
