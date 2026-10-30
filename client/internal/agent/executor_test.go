package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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

func TestWriteAndEditFileCreatePermissionPreservingBackups(t *testing.T) {
	ex := NewShellExecutor()
	p := filepath.Join(t.TempDir(), "config.txt")
	if err := os.WriteFile(p, []byte("old value"), 0o640); err != nil {
		t.Fatal(err)
	}
	originalInfo, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := ex.WriteFile(p, "written value"); err != nil {
		t.Fatal(err)
	}
	backups, err := filepath.Glob(p + ".bak-*")
	if err != nil || len(backups) != 1 {
		t.Fatalf("WriteFile backups: %v %v", backups, err)
	}
	if data, err := os.ReadFile(backups[0]); err != nil || string(data) != "old value" {
		t.Fatalf("WriteFile backup content: %q %v", data, err)
	}
	backupInfo, err := os.Stat(backups[0])
	if err != nil || backupInfo.Mode().Perm() != originalInfo.Mode().Perm() {
		t.Fatalf("backup mode %v, original %v, error %v", backupInfo.Mode().Perm(), originalInfo.Mode().Perm(), err)
	}
	if _, err := ex.EditFile(p, "written", "edited", false); err != nil {
		t.Fatal(err)
	}
	backups, err = filepath.Glob(p + ".bak-*")
	if err != nil || len(backups) != 2 {
		t.Fatalf("EditFile backups: %v %v", backups, err)
	}
	found := false
	for _, path := range backups {
		data, readErr := os.ReadFile(path)
		if readErr == nil && string(data) == "written value" {
			found = true
		}
	}
	if !found {
		t.Fatal("EditFile did not preserve the pre-edit content")
	}
	newFile := filepath.Join(filepath.Dir(p), "new.txt")
	if err := ex.WriteFile(newFile, "new"); err != nil {
		t.Fatal(err)
	}
	if backups, _ := filepath.Glob(newFile + ".bak-*"); len(backups) != 0 {
		t.Fatalf("new file unexpectedly got backups: %v", backups)
	}
}

func TestShellExecutorLogsFullOutput(t *testing.T) {
	command := "printf START; printf '%020000d' 0; printf MIDDLE_MARKER; printf '%020000d' 0; printf 'END\\n'"
	if runtime.GOOS == "windows" {
		command = "Write-Output ('START' + ('x' * 20000) + 'MIDDLE_MARKER' + ('y' * 20000) + 'END')"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, output, err := NewShellExecutor().Run(ctx, command, 30*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	const prefix = "[full output: "
	start := strings.LastIndex(output, prefix)
	if start < 0 || !strings.HasSuffix(output, "]") {
		t.Fatalf("output lacks log reference: %q", output[len(output)-min(len(output), 200):])
	}
	path := strings.TrimSuffix(output[start+len(prefix):], "]")
	t.Cleanup(func() { _ = os.Remove(path) })
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read full output %s: %v", path, err)
	}
	if len(data) < 40000 || !strings.Contains(string(data), "START") || !strings.Contains(string(data), "MIDDLE_MARKER") || !strings.Contains(string(data), "END") {
		t.Fatalf("full output log %s lacks the complete long command output", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat log: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("log permissions: mode=%v err=%v", info.Mode().Perm(), err)
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
