package main

import (
	"strings"
	"testing"

	"github.com/deploypilot/deploypilot/internal/console"
)

func TestContextBudgetConfiguration(t *testing.T) {
	if n, err := contextBudget("32768"); err != nil || n != 32768 {
		t.Fatalf("configured budget: %d %v", n, err)
	}
	if n, err := contextBudget(""); err != nil || n != 131072 {
		t.Fatalf("default budget: %d %v", n, err)
	}
	for _, value := range []string{"invalid", "-1", "0", "16000", "2097153"} {
		if _, err := contextBudget(value); err == nil {
			t.Errorf("invalid budget accepted: %s", value)
		}
	}
}

func TestStepBudgetConfiguration(t *testing.T) {
	if n, err := stepBudget(""); err != nil || n != 40 {
		t.Fatalf("default steps: %d %v", n, err)
	}
	if n, err := stepBudget("80"); err != nil || n != 80 {
		t.Fatalf("configured steps: %d %v", n, err)
	}
	for _, value := range []string{"x", "0", "4", "201", "-3"} {
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
		if got := qrBlock(c.caps, link); !strings.Contains(got, c.expect) {
			t.Errorf("%s: output lacks %q", c.name, c.expect)
		}
	}
}
