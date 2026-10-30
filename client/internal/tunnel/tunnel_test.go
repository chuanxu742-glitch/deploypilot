package tunnel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTunnelURLPattern(t *testing.T) {
	cases := map[string]string{
		"2024-01-01 INF +----+ https://happy-cloud-1234.trycloudflare.com |": "https://happy-cloud-1234.trycloudflare.com",
		"no url here":             "",
		"https://example.com/foo": "",
		"prefix https://a-b-c.trycloudflare.com suffix": "https://a-b-c.trycloudflare.com",
	}
	for in, want := range cases {
		if got := urlRe.FindString(in); got != want {
			t.Errorf("urlRe.FindString(%q) = %q, want %q", in, got, want)
		}
	}
}

// scanForURL drains r through the same capture the tunnel uses for cloudflared logs.
func scanForURL(r io.Reader, out chan<- string) {
	_, _ = io.Copy(&urlCapture{out: out}, r)
}

func TestScanForURL(t *testing.T) {
	logs := strings.Join([]string{
		"starting tunnel",
		"registering connection",
		"+--------------------------------------+",
		"|  https://test-tunnel-xyz.trycloudflare.com  |",
		"+--------------------------------------+",
		"more logs after",
	}, "\n")
	ch := make(chan string, 1)
	go scanForURL(strings.NewReader(logs), ch)
	got := <-ch
	if got != "https://test-tunnel-xyz.trycloudflare.com" {
		t.Fatalf("got %q", got)
	}
}

func TestVerifyChecksum(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	data := []byte("hello deploypilot")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	want := hex.EncodeToString(sum[:])

	if err := verifyChecksum(p, want); err != nil {
		t.Fatalf("matching checksum should pass: %v", err)
	}
	if err := verifyChecksum(p, strings.Repeat("0", 64)); err == nil {
		t.Fatal("mismatched checksum must fail")
	}
}

func TestDownloadVerifies(t *testing.T) {
	payload := []byte("fake cloudflared binary contents")
	sum := sha256.Sum256(payload)
	want := hex.EncodeToString(sum[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	// Point the mirror list at our test server. The download() helper builds
	// a full release path under the base, which our server ignores.
	t.Setenv("DP_CLOUDFLARED_MIRROR", srv.URL)

	dir := t.TempDir()
	dest := filepath.Join(dir, "cloudflared")

	if err := download(context.Background(), dest, want); err != nil {
		t.Fatalf("download: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("downloaded content mismatch")
	}
}

func TestDownloadRejectsBadChecksum(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("tampered"))
	}))
	defer srv.Close()
	t.Setenv("DP_CLOUDFLARED_MIRROR", srv.URL)

	dir := t.TempDir()
	dest := filepath.Join(dir, "cloudflared")
	if err := download(context.Background(), dest, strings.Repeat("a", 64)); err == nil {
		t.Fatal("download must fail on checksum mismatch")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("dest must not be created on checksum failure")
	}
}

func TestPrintQR(t *testing.T) {
	link := "https://abc-def-ghi.trycloudflare.com/#t=0123456789abcdef0123456789abcdef"
	half, halfColumns := QR(link, true)
	cells, cellColumns := QR(link, false)
	if !strings.ContainsAny(half, "▀▄█") || strings.Contains(half, "\x1b[") {
		t.Fatalf("half-block QR uses block characters without escapes: %q", half[:80])
	}
	if strings.ContainsAny(cells, "▀▄█") || !strings.Contains(cells, "\x1b[40m  ") {
		t.Fatalf("colour-cell QR uses only spaces with ANSI backgrounds: %q", cells[:80])
	}
	// Measured in a real conhost window: this link gives 39 columns.
	if halfColumns != 39 {
		t.Fatalf("half-block QR is %d columns, want 39", halfColumns)
	}
	// Two columns per module plus a wider quiet zone.
	if cellColumns != 2*(halfColumns-2+4) {
		t.Fatalf("colour-cell QR is %d columns, want %d", cellColumns, 2*(halfColumns-2+4))
	}
	// Every colour-cell row has the same width, so it stays square.
	for _, row := range strings.Split(strings.TrimRight(cells, "\n"), "\n") {
		if got := strings.Count(row, " "); got != cellColumns {
			t.Fatalf("row is %d columns, want %d", got, cellColumns)
		}
	}
}

// A source that trickles data must be abandoned quickly in favor of the next
// mirror.
func TestDownloadSkipsSlowMirror(t *testing.T) {
	oldWin, oldMin := rateWindow, minWindowBytes
	rateWindow, minWindowBytes = 200*time.Millisecond, 1024
	t.Cleanup(func() { rateWindow, minWindowBytes = oldWin, oldMin })

	payload := []byte(strings.Repeat("x", 4096))
	sum := sha256.Sum256(payload)
	want := hex.EncodeToString(sum[:])

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "4096")
		for i := 0; i < 4096; i++ {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
			_, _ = w.Write(payload[i : i+1])
			w.(http.Flusher).Flush()
		}
	}))
	defer slow.Close()
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer fast.Close()

	t.Setenv("DP_CLOUDFLARED_MIRROR", slow.URL+","+fast.URL)
	dest := filepath.Join(t.TempDir(), "cloudflared")

	start := time.Now()
	if err := download(context.Background(), dest, want); err != nil {
		t.Fatalf("download: %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("slow mirror was not abandoned quickly: %s", d)
	}
}
