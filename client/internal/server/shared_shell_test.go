package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSharedShellKeepsDirectoryEnvironmentAndManualInput(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip("PTY unavailable")
	}
	shell, err := newSharedShell(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	dir := t.TempDir()
	var change, query, manual string
	marker := filepath.Join(t.TempDir(), "manual-ready")
	if runtime.GOOS == "windows" {
		change = "Set-Location '" + strings.ReplaceAll(dir, "'", "''") + "'; $env:DP_SESSION_TEST='shared'"
		query = "(Get-Location).Path; $env:DP_SESSION_TEST"
		manual = "Set-Location '" + strings.ReplaceAll(filepath.Dir(dir), "'", "''") + "'; [IO.File]::WriteAllText('" + strings.ReplaceAll(marker, "'", "''") + "','ready')\r"
	} else {
		change = "cd '" + dir + "'; export DP_SESSION_TEST=shared"
		query = "pwd; printf '%s\\n' \"$DP_SESSION_TEST\""
		manual = "cd '" + filepath.Dir(dir) + "'; printf ready > '" + marker + "'\n"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if code, out, err := shell.Run(ctx, change, nil); err != nil || code != 0 {
		t.Fatalf("change code=%d err=%v out=%s", code, err, out)
	}
	// XTerm sends focus-out when the user clicks the Agent composer.
	if _, err := shell.Write([]byte("\x1b[O")); err != nil {
		t.Fatal(err)
	}
	if code, out, err := shell.Run(ctx, query, nil); err != nil || code != 0 || !strings.Contains(out, dir) || !strings.Contains(out, "shared") {
		t.Fatalf("persistent state code=%d err=%v out=%s", code, err, out)
	}
	if _, err := shell.Write([]byte(manual)); err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
	if code, out, err := shell.Run(ctx, query, nil); err != nil || code != 0 || !strings.Contains(out, filepath.Dir(dir)) {
		t.Fatalf("manual state code=%d err=%v out=%s", code, err, out)
	}
	if shell.Cwd() != filepath.Dir(dir) {
		t.Fatalf("cwd=%q", shell.Cwd())
	}
}

func TestSharedShellRestoresDirectoryAndHistory(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip("PTY unavailable")
	}
	storage := t.TempDir()
	target := t.TempDir()
	shell, err := newSharedShell(storage)
	if err != nil {
		t.Fatal(err)
	}
	command := "cd " + shellQuote(target) + "; echo persisted-output"
	if runtime.GOOS == "windows" {
		command = "Set-Location -LiteralPath " + psQuote(target) + "; Write-Output 'persisted-output'"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, _, err = shell.Run(ctx, command, nil); err != nil {
		t.Fatal(err)
	}
	// ConPTY may deliver the screen update after the execution result side channel.
	time.Sleep(150 * time.Millisecond)
	shell.Close()
	restored, err := newSharedShell(storage)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if restored.Cwd() != target {
		t.Fatalf("cwd %q", restored.Cwd())
	}
	history, _, unsubscribe := restored.Subscribe()
	defer unsubscribe()
	var output strings.Builder
	for _, chunk := range history {
		output.Write(chunk)
	}
	if !strings.Contains(output.String(), "persisted-output") {
		t.Fatal("terminal history lost")
	}
}

type persistenceTestSession struct {
	dead chan struct{}
	once sync.Once
}

func (s *persistenceTestSession) Read([]byte) (int, error)    { return 0, io.EOF }
func (s *persistenceTestSession) Write(p []byte) (int, error) { return len(p), nil }
func (s *persistenceTestSession) Close() error {
	s.once.Do(func() { close(s.dead) })
	return nil
}
func (s *persistenceTestSession) Resize(uint16, uint16) error { return nil }

func TestSharedShellPersistenceDoesNotHoldHistoryLock(t *testing.T) {
	dir := t.TempDir()
	readyPath := filepath.Join(dir, "shell-ready")
	if err := os.WriteFile(readyPath, []byte("/latest"), 0600); err != nil {
		t.Fatal(err)
	}
	dead := make(chan struct{})
	shell := &sharedShell{
		session:     &persistenceTestSession{dead: dead},
		dir:         dir,
		cwd:         "/initial",
		chunks:      [][]byte{[]byte("before")},
		subscribers: make(map[*shellFeed]struct{}),
		dead:        dead,
		readyPath:   readyPath,
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var writerMu sync.Mutex
	firstWrite := true
	shell.writeSnapshot = func(path string, data []byte) error {
		writerMu.Lock()
		block := firstWrite
		firstWrite = false
		writerMu.Unlock()
		if block {
			close(entered)
			<-release
		}
		return atomicPrivateWrite(path, data)
	}

	persistDone := make(chan struct{})
	go func() { shell.persist(false); close(persistDone) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("persistence writer was not entered")
	}
	_, feed, unsubscribe := shell.Subscribe()

	// History append and subscription must remain available while the disk
	// writer is blocked; the final Close snapshot must include this update.
	updated := make(chan struct{})
	go func() {
		shell.mu.Lock()
		shell.keepLocked([]byte("after"))
		shell.fanOutLocked([]byte("after"))
		shell.mu.Unlock()
		close(updated)
	}()
	select {
	case <-updated:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("history lock blocked by persistence writer")
	}
	history, _, unsubscribeHistory := shell.Subscribe()
	if len(history) != 2 || string(history[1]) != "after" {
		t.Fatalf("subscription history = %q", history)
	}
	select {
	case got := <-feed.output:
		if string(got) != "after" {
			t.Fatalf("live output = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive output")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- shell.Close() }()
	close(release)
	select {
	case <-persistDone:
	case <-time.After(2 * time.Second):
		t.Fatal("blocked persistence did not finish")
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close deadlocked behind persistence")
	}
	unsubscribe()
	unsubscribeHistory()

	data, err := os.ReadFile(filepath.Join(dir, "terminal.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot terminalSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Cwd != "/latest" || len(snapshot.Chunks) != 2 || string(snapshot.Chunks[1]) != "after" {
		t.Fatalf("final snapshot = %#v", snapshot)
	}
}

func TestSharedShellExitStatusAndCancellation(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip("PTY unavailable")
	}
	shell, err := newSharedShell(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fail, ok, wait := "false", "printf success", "sleep 30"
	if runtime.GOOS == "windows" {
		fail = "cmd /c exit 7"
		ok = "Write-Output success"
		wait = "Start-Sleep -Seconds 30"
	}
	if code, _, err := shell.Run(ctx, fail, nil); err != nil || code == 0 {
		t.Fatalf("failure: %d %v", code, err)
	}
	if code, out, err := shell.Run(ctx, ok, nil); err != nil || code != 0 || !strings.Contains(out, "success") {
		t.Fatalf("next command: %d %v %s", code, err, out)
	}
	short, stop := context.WithTimeout(ctx, 300*time.Millisecond)
	defer stop()
	start := time.Now()
	if _, _, err := shell.Run(short, wait, nil); err == nil {
		t.Fatal("cancel returned success")
	}
	if time.Since(start) > 4*time.Second {
		t.Fatal("cancellation did not finish")
	}
}

func TestSharedShellLogsFullPTYOutput(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip("PTY unavailable")
	}
	shell, err := newSharedShell(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	command := "printf START; head -c 20000 /dev/zero | tr '\\0' x; printf MIDDLE_MARKER; head -c 20000 /dev/zero | tr '\\0' y; printf 'END\\n'"
	if runtime.GOOS == "windows" {
		command = "Write-Output ('START' + ('x' * 20000) + 'MIDDLE_MARKER' + ('y' * 20000) + 'END')"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, output, err := shell.Run(ctx, command, nil)
	if err != nil {
		t.Fatalf("shared shell command error: %v; output tail=%q", err, output[len(output)-min(len(output), 200):])
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
	recorded := strings.NewReplacer("\r", "", "\n", "", "\x1b", "").Replace(string(data))
	if len(data) < 40000 || !strings.Contains(recorded, "START") || !strings.Contains(recorded, "MIDDLE_MARKER") || !strings.Contains(recorded, "END") {
		t.Fatalf("PTY log %s lacks the complete long output (bytes=%d)", path, len(data))
	}
	history, _, unsubscribe := shell.Subscribe()
	defer unsubscribe()
	if strings.Contains(string(bytes.Join(history, nil)), "DP_OUTPUT_DONE_") {
		t.Fatal("internal output marker leaked into terminal history")
	}
}

func TestOutputMarkerIsHiddenAcrossChunks(t *testing.T) {
	var pending string
	marker := "DP_OUTPUT_DONE_123"
	got := hideOutputMarker(&pending, "first\nDP_OUT", marker)
	got += hideOutputMarker(&pending, "PUT_DONE_123last\n", marker)
	if got != "first\nlast\n" || pending != "" {
		t.Fatalf("visible=%q pending=%q", got, pending)
	}
}

func TestSessionExecutorRelativeFilesFollowShell(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip("PTY unavailable")
	}
	srv := New(Options{HistoryDir: t.TempDir()})
	defer srv.Shutdown(context.Background())
	executor := srv.newExecutor()
	target := t.TempDir()
	command := "cd " + shellQuote(target)
	if runtime.GOOS == "windows" {
		command = "Set-Location -LiteralPath " + psQuote(target)
	}
	if _, _, err := executor.Run(context.Background(), command, 10*time.Second, nil); err != nil {
		t.Fatal(err)
	}
	if err := executor.WriteFile("relative.txt", "correct directory"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(target, "relative.txt"))
	if err != nil || string(data) != "correct directory" {
		t.Fatal("relative write missed shared cwd", err)
	}
}

// A viewer that cannot keep up with a burst is told to redraw instead of being
// disconnected, and the redraw neither loses nor repeats output.
func TestSlowViewerIsResyncedNotDropped(t *testing.T) {
	shell := &sharedShell{subscribers: make(map[*shellFeed]struct{})}
	_, feed, unsubscribe := shell.Subscribe()
	defer unsubscribe()
	shell.mu.Lock()
	for i := 0; i < 300; i++ { // more than the 256-chunk queue
		chunk := []byte(fmt.Sprintf("line %03d\n", i))
		shell.keepLocked(chunk)
		shell.fanOutLocked(chunk)
	}
	shell.mu.Unlock()
	select {
	case <-feed.lagged:
	default:
		t.Fatal("overflow must signal lag")
	}
	redraw := shell.Resync(feed)
	if got := string(redraw[len(redraw)-1]); got != "line 299\n" {
		t.Fatalf("redraw must end with the newest output, got %q", got)
	}
	select {
	case data, ok := <-feed.output:
		t.Fatalf("queued output must be discarded by resync (ok=%v, %q)", ok, data)
	default:
	}
	// Live output after the redraw still arrives, so the feed is not closed.
	shell.mu.Lock()
	shell.keepLocked([]byte("after\n"))
	shell.fanOutLocked([]byte("after\n"))
	shell.mu.Unlock()
	if data, ok := <-feed.output; !ok || string(data) != "after\n" {
		t.Fatalf("live output after resync: ok=%v %q", ok, data)
	}
}

func TestResyncSendsOnlyRecentHistory(t *testing.T) {
	shell := &sharedShell{subscribers: make(map[*shellFeed]struct{})}
	_, feed, unsubscribe := shell.Subscribe()
	defer unsubscribe()
	chunk := bytes.Repeat([]byte("x"), 64*1024)
	shell.mu.Lock()
	for i := 0; i < 24; i++ { // 1.5 MiB of history
		shell.keepLocked(chunk)
	}
	shell.mu.Unlock()
	size := 0
	for _, c := range shell.Resync(feed) {
		size += len(c)
	}
	if size < resyncTailBytes || size > resyncTailBytes+len(chunk) {
		t.Fatalf("resync sent %d bytes, want about %d", size, resyncTailBytes)
	}
}
