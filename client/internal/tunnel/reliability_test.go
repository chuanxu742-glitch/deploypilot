package tunnel

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Use the test executable as cloudflared so lifecycle tests exercise real pipes
// and process cancellation on every supported OS without contacting Cloudflare.
func TestMain(m *testing.M) {
	if mode := os.Getenv("DP_TEST_TUNNEL_PROCESS"); mode != "" {
		var pidfile string
		for i, arg := range os.Args {
			if arg == "--pidfile" && i+1 < len(os.Args) {
				pidfile = os.Args[i+1]
			}
		}
		if mode == "exit" {
			os.Exit(0)
		}
		if mode == "flood" {
			fmt.Fprint(os.Stdout, strings.Repeat("x", 2*1024*1024)+"\n")
		}
		out := os.Stdout
		if mode == "not-ready" {
			out = os.Stderr
		}
		fmt.Fprintln(out, "https://local-test.trycloudflare.com")
		if mode != "not-ready" {
			time.Sleep(200 * time.Millisecond)
			if pidfile != "" {
				_ = os.WriteFile(pidfile, []byte(strconv.Itoa(os.Getpid())), 0o600)
			}
		}
		if mode == "flap" {
			time.Sleep(300 * time.Millisecond)
			os.Exit(3)
		}
		for {
			time.Sleep(time.Second)
		}
	}
	os.Exit(m.Run())
}

func TestStartDrainsBothStreamsAndLargeLines(t *testing.T) {
	for _, mode := range []string{"stdout", "flood"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("DP_TEST_TUNNEL_PROCESS", mode)
			exe, _ := os.Executable()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			start := time.Now()
			tun, err := Start(ctx, exe, 12345)
			if err != nil {
				t.Fatal(err)
			}
			tun.Close()
			if tun.URL != "https://local-test.trycloudflare.com" {
				t.Fatal(tun.URL)
			}
			if time.Since(start) < 200*time.Millisecond {
				t.Fatal("returned before first connection")
			}
			select {
			case <-tun.Done():
			default:
				t.Fatal("process not reaped")
			}
		})
	}
}

func TestStartURLAloneIsNotReady(t *testing.T) {
	t.Setenv("DP_TEST_TUNNEL_PROCESS", "not-ready")
	exe, _ := os.Executable()
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	tun, err := Start(ctx, exe, 12345)
	if tun != nil {
		tun.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("URL without a connection must time out, got %v", err)
	}
}

func TestStartEarlyCleanExitIsError(t *testing.T) {
	t.Setenv("DP_TEST_TUNNEL_PROCESS", "exit")
	exe, _ := os.Executable()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := Start(ctx, exe, 12345)
	if err == nil || strings.Contains(err.Error(), "%!w") {
		t.Fatalf("need useful early exit error, got %v", err)
	}
}

func TestDownloadSkipsStalledHeaders(t *testing.T) {
	oldWin := rateWindow
	rateWindow = 100 * time.Millisecond
	t.Cleanup(func() { rateWindow = oldWin })
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer slow.Close()
	payload := []byte("verified binary")
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(payload) }))
	defer fast.Close()
	t.Setenv("DP_CLOUDFLARED_MIRROR", slow.URL+","+fast.URL)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := download(ctx, filepath.Join(t.TempDir(), "binary"), fmt.Sprintf("%x", sha256.Sum256(payload))); err != nil {
		t.Fatal(err)
	}
}

func TestDownloadCanceledCleansPartialFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		_, _ = w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	t.Setenv("DP_CLOUDFLARED_MIRROR", srv.URL)
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if err := download(ctx, filepath.Join(dir, "binary"), "unused"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 0 {
		t.Fatalf("partial files left: %v", files)
	}
}

func TestConcurrentDownloadsUseIndependentFiles(t *testing.T) {
	var arrivals sync.WaitGroup
	arrivals.Add(2)
	payload := []byte(strings.Repeat("binary", 4096))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		arrivals.Done()
		arrivals.Wait()
		_, _ = w.Write(payload)
	}))
	defer srv.Close()
	t.Setenv("DP_CLOUDFLARED_MIRROR", srv.URL)
	dest := filepath.Join(t.TempDir(), "binary")
	want := fmt.Sprintf("%x", sha256.Sum256(payload))
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { results <- download(context.Background(), dest, want) }()
	}
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
	if err := verifyChecksum(dest, want); err != nil {
		t.Fatal(err)
	}
}
