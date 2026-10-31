//go:build windows

package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/deploypilot/deploypilot/internal/agent"
)

// The rewritten chains keep bash's meaning in the real shared terminal:
// the right side runs only after success (&&) or failure (||), and the
// reported exit code is the failing command's.
func TestRewrittenChainsBehaveLikeBash(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir()) // command logs stay out of the real profile
	shell, err := newSharedShell(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	cases := []struct {
		command   string
		code      int
		has, lack string
	}{
		{`Write-Output first && Write-Output second`, 0, "second", ""},
		{`cmd /c exit 3 && Write-Output never`, 3, "", "never"},
		{`Get-Item C:\no-such-dir-dp && Write-Output never`, 1, "", "never"},
		{`cmd /c exit 2 || Write-Output fallback`, 0, "fallback", ""},
		{`Write-Output fine || Write-Output never`, 0, "fine", "never"},
		{`Set-Location $env:TEMP && (Get-Location).Path`, 0, "Temp", ""},
	}
	for _, c := range cases {
		rewritten, ok := agent.RewritePowerShellChain(c.command)
		if !ok {
			t.Fatalf("not rewritten: %q", c.command)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		code, out, err := shell.Run(ctx, rewritten, nil)
		cancel()
		if err != nil {
			t.Fatalf("%q: %v", rewritten, err)
		}
		// Error messages echo the source line, so match whole output lines.
		lines := map[string]bool{}
		for _, line := range strings.Split(out, "\n") {
			lines[strings.TrimSpace(line)] = true
		}
		if code != c.code || (c.has != "" && !strings.Contains(out, c.has)) || (c.lack != "" && lines[c.lack]) {
			t.Errorf("%q -> %q: code=%d output=%q; want code=%d with %q without %q", c.command, rewritten, code, out, c.code, c.has, c.lack)
		}
	}
}
