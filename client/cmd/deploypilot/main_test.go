package main

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/deploypilot/deploypilot/internal/banner"
	"github.com/deploypilot/deploypilot/internal/console"
)

func TestContextBudgetConfiguration(t *testing.T) {
	if n, err := contextBudget("32768"); err != nil || n != 32768 {
		t.Fatalf("configured budget: %d %v", n, err)
	}
	if n, err := contextBudget(""); err != nil || n != 262144 {
		t.Fatalf("default budget: %d %v", n, err)
	}
	for _, value := range []string{"invalid", "-1", "0", "16000", "2097153"} {
		if _, err := contextBudget(value); err == nil {
			t.Errorf("invalid budget accepted: %s", value)
		}
	}
}

func TestStepBudgetConfiguration(t *testing.T) {
	for _, value := range []string{"", "0"} {
		if n, err := stepBudget(value); err != nil || n != 0 {
			t.Fatalf("%q must mean no step limit: %d %v", value, n, err)
		}
	}
	for _, value := range []string{"5", "80", "1000"} {
		n, err := stepBudget(value)
		if err != nil || strconv.Itoa(n) != value {
			t.Fatalf("configured steps %s: %d %v", value, n, err)
		}
	}
	for _, value := range []string{"x", "1", "4", "-3"} {
		if _, err := stepBudget(value); err == nil {
			t.Errorf("invalid step budget accepted: %s", value)
		}
	}
}

func TestQRBlockFitsTheTerminal(t *testing.T) {
	link := "https://abc-def-ghi.trycloudflare.com/#t=0123456789abcdef0123456789abcdef"
	cases := []struct {
		name   string
		caps   console.Caps
		expect string
	}{
		{"compact QR in a normal window", console.Caps{Terminal: true, Color: true, NarrowBlocks: true, Width: 120}, "▀"},
		{"colour cells when blocks are wide", console.Caps{Terminal: true, Color: true, Width: 120}, "\x1b[40m  "},
		{"link only without colour or narrow blocks", console.Caps{Terminal: true, Width: 120}, "无法正确显示二维码"},
		{"link only when the window is too narrow", console.Caps{Terminal: true, Color: true, NarrowBlocks: true, Width: 30}, "窗口宽度 30 列"},
		{"logs keep the QR code", console.Caps{NarrowBlocks: true}, "▀"},
	}
	for _, c := range cases {
		p := pairingFor(c.caps, "扫码", link, 10*time.Minute)
		opts := banner.DefaultOptions(c.caps, "0.4.9")
		got := banner.RenderPairing(opts, p)
		if !strings.Contains(got, c.expect) {
			t.Errorf("%s: output lacks %q", c.name, c.expect)
		}
		if !strings.Contains(got, link) || !strings.Contains(got, "10 分钟内有效") {
			t.Errorf("%s: link or validity note missing", c.name)
		}
	}
}
