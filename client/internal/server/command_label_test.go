package server

import (
	"strings"
	"testing"
)

func TestCommandLabelFitsOneTerminalLine(t *testing.T) {
	cells := func(s string) int {
		n := 0
		for _, r := range s {
			if runeIsWide(r) {
				n += 2
			} else {
				n++
			}
		}
		return n
	}
	long := `if (!(Test-Path "C:\Users\me\AppData\Local\Temp\run\s3")) { New-Item -ItemType Directory -Path "C:\Users\me\AppData\Local\Temp\run\s3" }`
	chinese := strings.Repeat("检查服务状态", 30)
	for _, cols := range []int{32, 80, 83, 100, 240} {
		for _, command := range []string{long, chinese, "a\nb\tc"} {
			label := commandLabel(command, cols)
			if line := len("> . dp # ") + cells(label); line >= cols && cols > 40 {
				t.Errorf("cols=%d: echoed line takes %d cells: %q", cols, line, label)
			}
			if strings.ContainsAny(label, "\r\n\t") {
				t.Errorf("control characters survived: %q", label)
			}
		}
	}
	if got := commandLabel("Get-Date", 100); got != "Get-Date" {
		t.Errorf("short command changed: %q", got)
	}
	if got := commandLabel(strings.Repeat("x", 500), 1000); len(got) != 183 {
		t.Errorf("wide terminals still cap the label at 180 runes: %d", len(got))
	}
}
