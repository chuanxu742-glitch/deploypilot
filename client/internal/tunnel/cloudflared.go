// Package tunnel manages the cloudflared binary and a quick tunnel that
// exposes the loopback web UI on a public trycloudflare.com address.
package tunnel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"time"
)

// Version is the pinned cloudflared release.
const Version = "2026.10.5"

// checksums holds the verified SHA-256 of the pinned cloudflared binaries,
// keyed by "GOOS/GOARCH". These were computed from the official
// 2026.10.5 release assets and are enforced on every download.
var checksums = map[string]string{
	"linux/amd64":   "e1bc0cf084fd9e590554fe769a86b1db46701c3f847a43d902f7b974a4e16298",
	"linux/arm64":   "72e0d05a67e09da87cf495e409a28c3cc5994dd23fc66a982eebea6a55923b7d",
	"windows/amd64": "db4e4c413612a0916f3e1679d3caaf376f94911952d163028fb726fb2e0516a9",
}

// downloadBases are tried in order. The official GitHub host is first;
// mirrors help on networks where GitHub's release host is unreachable.
// Override entirely with DP_CLOUDFLARED_MIRROR (comma-separated bases that
// each prefix the full GitHub URL).
var downloadBases = []string{
	"https://github.com",
	"https://gh-proxy.com/https://github.com",
	"https://ghfast.top/https://github.com",
}

// assetName returns the release asset file name for the current platform.
func assetName() string {
	name := fmt.Sprintf("cloudflared-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// platformKey identifies the current platform in the checksum table.
func platformKey() string {
	return runtime.GOOS + "/" + runtime.GOARCH
}

// BinDir returns the directory where the managed binary is cached.
func BinDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".deploypilot", "bin"), nil
}

// EnsureBinary returns the path to a verified cloudflared binary, downloading
// it if necessary. If DP_CLOUDFLARED_BIN is set, that path is used as-is
// (useful on unsupported dev platforms); otherwise the platform must have a
// pinned checksum.
func EnsureBinary(ctx context.Context) (string, error) {
	if override := os.Getenv("DP_CLOUDFLARED_BIN"); override != "" {
		if _, err := os.Stat(override); err != nil {
			return "", fmt.Errorf("DP_CLOUDFLARED_BIN %q: %w", override, err)
		}
		return override, nil
	}

	want, ok := checksums[platformKey()]
	if !ok {
		return "", fmt.Errorf("no pinned cloudflared checksum for %s; set DP_CLOUDFLARED_BIN", platformKey())
	}

	dir, err := BinDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	binName := fmt.Sprintf("cloudflared-%s", Version)
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	dest := filepath.Join(dir, binName)

	// Reuse a previously verified binary.
	if err := verifyChecksum(dest, want); err == nil {
		return dest, nil
	}

	if err := download(ctx, dest, want); err != nil {
		return "", err
	}
	if err := os.Chmod(dest, 0o755); err != nil {
		return "", err
	}
	return dest, nil
}

// download fetches the asset from the configured bases, writing to dest only
// after the checksum verifies.
func download(ctx context.Context, dest, want string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(dest), ".cloudflared-*.tmp")
	if err != nil {
		return err
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	if err := file.Close(); err != nil {
		return err
	}
	var lastErr error
	for _, base := range mirrorBases() {
		if err := ctx.Err(); err != nil {
			return err
		}
		url := fmt.Sprintf("%s/cloudflare/cloudflared/releases/download/%s/%s", base, Version, assetName())
		if err := fetch(ctx, url, tmp); err != nil {
			lastErr = err
			continue
		}
		if err := verifyChecksum(tmp, want); err != nil {
			lastErr = fmt.Errorf("checksum mismatch from %s: %w", base, err)
			continue
		}
		if err := os.Chmod(tmp, 0o755); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.Rename(tmp, dest); err != nil {
			// Another instance may have just installed the same verified version.
			// Never remove a shared cache entry to make room for our own copy.
			if verifyChecksum(dest, want) == nil {
				return nil
			}
			return err
		}
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no download source available")
	}
	return fmt.Errorf("download cloudflared: %w", lastErr)
}

func mirrorBases() []string {
	if v := os.Getenv("DP_CLOUDFLARED_MIRROR"); v != "" {
		var out []string
		for _, b := range splitComma(v) {
			if b != "" {
				out = append(out, b)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return downloadBases
}

func splitComma(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			out = append(out, trimSpace(s[start:i]))
			start = i + 1
		}
	}
	out = append(out, trimSpace(s[start:]))
	return out
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

// Slow-source detection: if a source delivers fewer than minWindowBytes in
// any rateWindow, the download is aborted so the next mirror is tried
// instead of crawling for minutes (common for github.com from China).
var (
	rateWindow     = 10 * time.Second
	minWindowBytes = int64(512 * 1024)
)

// errTooSlow is returned when a source falls below the minimum rate.
var errTooSlow = fmt.Errorf("download too slow")

// countingReader counts bytes read so a watchdog can measure throughput.
type countingReader struct {
	r io.Reader
	n atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

func fetch(ctx context.Context, url, dest string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	ctx, cancelSlow := context.WithCancelCause(ctx)
	defer cancelSlow(nil)
	// Start measuring before DNS, TLS and response headers, not just body reads.
	body := &countingReader{}
	window, minBytes := rateWindow, minWindowBytes
	done := make(chan struct{})
	defer close(done)
	go func() {
		t := time.NewTicker(window)
		defer t.Stop()
		var last int64
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				cur := body.n.Load()
				if cur-last < minBytes {
					cancelSlow(errTooSlow)
					return
				}
				last = cur
			}
		}
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "deploypilot")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if context.Cause(ctx) == errTooSlow {
			return fmt.Errorf("GET %s: %w", url, errTooSlow)
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	body.r = resp.Body
	if _, err := io.Copy(f, body); err != nil {
		if cause := context.Cause(ctx); cause != nil && cause == errTooSlow {
			return fmt.Errorf("GET %s: %w", url, errTooSlow)
		}
		return err
	}
	return f.Close()
}

// verifyChecksum returns nil if the file at path has the expected sha256.
func verifyChecksum(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return fmt.Errorf("sha256 %s != expected %s", got, want)
	}
	return nil
}
