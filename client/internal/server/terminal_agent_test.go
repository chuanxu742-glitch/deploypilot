package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestTerminalReconnectKeepsAgentDirectoryAndShell(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip("PTY unavailable")
	}
	srv, cookie := newLLMServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	header := http.Header{"Cookie": {cookie.Name + "=" + cookie.Value}}
	connect := func() *websocket.Conn {
		t.Helper()
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/ws/term", header)
		if err != nil {
			t.Fatal(err)
		}
		conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		var reset map[string]any
		if err = conn.ReadJSON(&reset); err != nil || reset["type"] != "terminal_reset" {
			t.Fatal("missing reset", reset, err)
		}
		return conn
	}
	first := connect()
	original, err := srv.sharedTerminal()
	if err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if runtime.GOOS == "windows" {
		if info, err := os.Stat("D:\\"); err == nil && info.IsDir() {
			target = "D:\\"
		}
	}
	command := "cd " + shellQuote(target)
	if runtime.GOOS == "windows" {
		command = "Set-Location -LiteralPath " + psQuote(target)
	}
	if _, _, err = srv.newExecutor().Run(context.Background(), command, 10*time.Second, nil); err != nil {
		t.Fatal(err)
	}
	first.Close()
	second := connect()
	defer second.Close()
	reconnected, err := srv.sharedTerminal()
	if err != nil || reconnected != original {
		t.Fatal("reconnect replaced shell", err)
	}
	file := filepath.Join(t.TempDir(), "manual-cwd.txt")
	manual := "pwd > " + shellQuote(file) + "\n"
	if runtime.GOOS == "windows" {
		manual = "[IO.File]::WriteAllText(" + psQuote(file) + ",(Get-Location).Path)\r"
	}
	if err = second.WriteMessage(websocket.BinaryMessage, []byte(manual)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(file)
		if err == nil && strings.TrimSpace(string(data)) == target {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("manual terminal did not inherit Agent directory", target)
}

func terminalMessage(t *testing.T, updates <-chan []byte) map[string]any {
	t.Helper()
	select {
	case data, ok := <-updates:
		if !ok {
			t.Fatal("terminal subscription closed")
		}
		var message map[string]any
		if err := json.Unmarshal(data, &message); err != nil {
			t.Fatal(err)
		}
		return message
	case <-time.After(time.Second):
		t.Fatal("terminal update missing")
		return nil
	}
}

func TestTerminalWebSocketReceivesAgentState(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip("PTY unavailable on this platform")
	}
	srv, cookie := newLLMServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	header := http.Header{}
	header.Add("Cookie", cookie.Name+"="+cookie.Value)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/ws/term", header)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	read := func() map[string]any {
		t.Helper()
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		for {
			kind, data, err := conn.ReadMessage()
			if err != nil {
				t.Fatal(err)
			}
			if kind != websocket.TextMessage {
				continue
			}
			var message map[string]any
			if err := json.Unmarshal(data, &message); err != nil {
				t.Fatal(err)
			}
			if message["type"] == "terminal_reset" {
				continue
			}
			return message
		}
	}
	if got := read(); got["state"] != "idle" {
		t.Fatal(got)
	}
	srv.acquireAgent()
	if got := read(); got["state"] != "running" {
		t.Fatal(got)
	}
	srv.markAgentStopping()
	if got := read(); got["state"] != "stopping" {
		t.Fatal(got)
	}
	srv.releaseAgent()
	if got := read(); got["state"] != "idle" {
		t.Fatal(got)
	}
}

func TestTerminalAgentStateAndInputOwnership(t *testing.T) {
	srv := New(Options{})
	updates, unsubscribe := srv.subscribeTerminalAgent()
	defer unsubscribe()
	if got := terminalMessage(t, updates); got["state"] != "idle" {
		t.Fatal(got)
	}
	var shell bytes.Buffer
	if _, err := srv.writeTerminalInput(&shell, []byte("before")); err != nil {
		t.Fatal(err)
	}
	if !srv.acquireAgent() {
		t.Fatal("could not start")
	}
	if got := terminalMessage(t, updates); got["state"] != "running" {
		t.Fatal(got)
	}
	_, _ = srv.writeTerminalInput(&shell, []byte("must not execute"))
	srv.markAgentStopping()
	if got := terminalMessage(t, updates); got["state"] != "stopping" {
		t.Fatal(got)
	}
	_, _ = srv.writeTerminalInput(&shell, []byte("cleanup must stay locked"))
	_, _ = srv.writeTerminalInput(&shell, []byte("final is not cleanup"))
	srv.releaseAgent()
	if got := terminalMessage(t, updates); got["state"] != "idle" {
		t.Fatal(got)
	}
	_, _ = srv.writeTerminalInput(&shell, []byte("after"))
	if shell.String() != "beforeafter" {
		t.Fatalf("unexpected manual input: %q", shell.String())
	}
}

func TestTerminalJoiningDuringCleanupStaysReadOnly(t *testing.T) {
	srv := New(Options{})
	srv.acquireAgent()
	srv.markAgentStopping()
	updates, unsubscribe := srv.subscribeTerminalAgent()
	if got := terminalMessage(t, updates); got["state"] != "stopping" {
		t.Fatal(got)
	}
	unsubscribe()
	unsubscribe()
	if _, ok := <-updates; ok {
		t.Fatal("subscription not closed")
	}
	srv.releaseAgent()
}
