//go:build linux

package pty

import (
	"bufio"
	"strings"
	"testing"
	"time"
)

func TestShellRoundTrip(t *testing.T) {
	sess, err := Start(Config{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer sess.Close()

	marker := "DP_MARKER_12345"
	if _, err := sess.Write([]byte("echo " + marker + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	type res struct {
		line string
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		sc := bufio.NewScanner(sess)
		for sc.Scan() {
			if strings.Contains(sc.Text(), marker) {
				ch <- res{line: sc.Text()}
				return
			}
		}
		ch <- res{err: sc.Err()}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("scan: %v", r.err)
		}
		if !strings.Contains(r.line, marker) {
			t.Fatalf("marker not echoed, got %q", r.line)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for shell echo")
	}
}

func TestResize(t *testing.T) {
	sess, err := Start(Config{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer sess.Close()
	if err := sess.Resize(40, 120); err != nil {
		t.Fatalf("resize: %v", err)
	}
}
