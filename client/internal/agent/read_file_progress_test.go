package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/deploypilot/deploypilot/internal/llm"
)

func TestReadFileSmallUTF8PageReportsRecoverableError(t *testing.T) {
	for _, text := range []string{"é!", "世界", "😀!"} {
		_, width := utf8.DecodeRuneInString(text)
		for limit := 1; limit < width; limit++ {
			t.Run(fmt.Sprintf("%s/%d", text, limit), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "text.txt")
				if err := os.WriteFile(path, []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
				r, err := NewShellExecutor().ReadFile(path, 0, int64(limit))
				t.Logf("max_bytes=%d content=%q next_offset=%d eof=%v error=%v", limit, r.Content, r.NextOffset, r.EOF, err)
				if err == nil || !strings.Contains(err.Error(), "max_bytes") || !strings.Contains(err.Error(), "4") {
					t.Fatalf("expected actionable error instead of successful non-progressing page: result=%+v error=%v", r, err)
				}
				// Retrying the same offset with enough room must preserve the character.
				r, err = NewShellExecutor().ReadFile(path, 0, 4)
				if err != nil || r.NextOffset <= 0 || !utf8.ValidString(r.Content) || !strings.HasPrefix(text, r.Content) {
					t.Fatalf("retry failed: result=%+v error=%v", r, err)
				}
			})
		}
	}
}

func TestReadFileSmallPagesPreserveProgressAndEOF(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		limit      int64
	}{
		{"empty", "", 1},
		{"ascii", "abc", 1},
		{"two_byte", "éé", 2},
		{"three_byte", "世界", 3},
		{"four_byte", "😀😃", 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "text.txt")
			if err := os.WriteFile(path, []byte(tc.text), 0600); err != nil {
				t.Fatal(err)
			}
			var got strings.Builder
			for offset := int64(0); ; {
				r, err := NewShellExecutor().ReadFile(path, offset, tc.limit)
				if err != nil || !utf8.ValidString(r.Content) || int64(len(r.Content)) > tc.limit {
					t.Fatalf("invalid page: result=%+v error=%v", r, err)
				}
				got.WriteString(r.Content)
				if r.EOF {
					break
				}
				if r.NextOffset <= offset {
					t.Fatalf("page made no progress: %+v", r)
				}
				offset = r.NextOffset
			}
			if got.String() != tc.text {
				t.Fatalf("got %q, want %q", got.String(), tc.text)
			}
		})
	}
}

func TestReadFileToolReportsStalledPageAndRecovers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "text.txt")
	if err := os.WriteFile(path, []byte("a世界"), 0600); err != nil {
		t.Fatal(err)
	}
	var done []Event
	a := New(Config{Executor: NewShellExecutor(), Emit: func(e Event) {
		if e.Type == EventToolDone {
			done = append(done, e)
		}
	}})
	read := func(offset, limit int64) string {
		args, err := json.Marshal(readFileArgs{Path: path, Offset: offset, MaxBytes: limit})
		if err != nil {
			t.Fatal(err)
		}
		_, result := a.runTool(context.Background(), 1, llm.ToolCall{ID: "read", Type: "function", Function: llm.FunctionCall{Name: toolReadFile, Arguments: string(args)}})
		return result.Content
	}
	if got := read(0, 2); !strings.Contains(got, "offset=1") || !strings.HasSuffix(got, "\na") {
		t.Fatalf("first page: %s", got)
	}
	got := read(1, 2)
	t.Logf("read_file(offset=1, max_bytes=2): %s", got)
	if !strings.Contains(got, "max_bytes") || strings.Contains(got, "more remains") {
		t.Errorf("tool must surface a recoverable error rather than repeat the same offset: %s", got)
	}
	if len(done) != 2 || done[1].Success {
		t.Errorf("non-progressing page was marked successful: %+v", done)
	}
	got = read(1, 4)
	if !strings.HasSuffix(got, "\n世") || !strings.Contains(got, "offset=4") {
		t.Fatalf("retry skipped content: %s", got)
	}
	if got := read(4, 4); !strings.HasSuffix(got, "\n界") || strings.Contains(got, "more remains") {
		t.Fatalf("last page: %s", got)
	}
	if len(done) != 4 || !done[2].Success || !done[3].Success {
		t.Fatalf("recovery was not successful: %+v", done)
	}
}
