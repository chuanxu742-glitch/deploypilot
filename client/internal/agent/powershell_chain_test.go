package agent

import "testing"

func TestRewritePowerShellChain(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{`cd "C:\app" && python main.py`, `cd "C:\app"; if ($?) { python main.py }`, true},
		{`a && b && c`, `a; if ($?) { b; if ($?) { c } }`, true},
		{`nvidia-smi || echo none`, `nvidia-smi; if (-not $?) { $global:LASTEXITCODE = 0; echo none }`, true},
		// Left alone: nothing to rewrite, or rewriting would change meaning.
		{`Get-Date`, `Get-Date`, false},
		{`echo "a && b"`, `echo "a && b"`, false},
		{`echo 'it''s && fine'`, `echo 'it''s && fine'`, false},
		{"echo `&`& b", "echo `&`& b", false},
		{`if ($x) { a && b }`, `if ($x) { a && b }`, false},
		{`a && b || c`, `a && b || c`, false},
		{`a &&`, `a &&`, false},
		{`&& a`, `&& a`, false},
		{`Get-Date # then && later`, `Get-Date # then && later`, false},
		{"$s = @'\na && b\n'@; $s", "$s = @'\na && b\n'@; $s", false},
		{`& "C:\tool.exe" -x`, `& "C:\tool.exe" -x`, false},
		{`Get-Process | Select-Object Id`, `Get-Process | Select-Object Id`, false},
	}
	for _, c := range cases {
		got, ok := RewritePowerShellChain(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("%q:\n got %q %v\nwant %q %v", c.in, got, ok, c.want, c.ok)
		}
	}
}
