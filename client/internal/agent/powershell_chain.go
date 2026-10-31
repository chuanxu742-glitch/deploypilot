package agent

import "strings"

// Windows PowerShell 5.1 has no && or || (pwsh 7 added them), so a bash
// habit like `cd app && python main.py` fails to parse and costs a model
// turn. In real-model runs about one run_command in five did this.
//
// RewritePowerShellChain turns a top-level chain of only && or only || into
// the 5.1 form: `a && b` runs b when a succeeded, `a || b` when it failed.
// $? is false after a native command exits non-zero or a cmdlet writes an
// error, which matches the bash meaning closely enough for these chains.
// Operators inside quotes, here-strings, comments, (), {} or [] are left
// alone, and so are mixed chains, whose grouping differs between shells.
func RewritePowerShellChain(command string) (string, bool) {
	parts, op := splitTopLevelChain(command)
	if len(parts) < 2 || op == "" {
		return command, false
	}
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			return command, false
		}
	}
	// After a handled failure bash reports the fallback's status, so the
	// failed command's exit code must not outlive it.
	open := "; if ($?) { "
	if op == "||" {
		open = "; if (-not $?) { $global:LASTEXITCODE = 0; "
	}
	var b strings.Builder
	b.WriteString(strings.TrimSpace(parts[0]))
	for _, p := range parts[1:] {
		b.WriteString(open + strings.TrimSpace(p))
	}
	b.WriteString(strings.Repeat(" }", len(parts)-1))
	return b.String(), true
}

// splitTopLevelChain splits command at top-level && or || operators. op is
// "&&" or "||" when only one kind occurs, and "" for a mixed chain.
func splitTopLevelChain(command string) (parts []string, op string) {
	depth, start := 0, 0
	mixed := false
	rs := []rune(command)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == '`': // escape character
			i++
		case r == '#' && (i == 0 || isSpace(rs[i-1])):
			// A comment runs to the end of the line; stop scanning it.
			for i < len(rs) && rs[i] != '\n' {
				i++
			}
		case r == '@' && i+1 < len(rs) && (rs[i+1] == '\'' || rs[i+1] == '"'):
			// Here-string: @' ... '@ with the closing quote at a line start.
			q := rs[i+1]
			i += 2
			for i < len(rs) && !(rs[i] == q && i+1 < len(rs) && rs[i+1] == '@' && (i == 0 || rs[i-1] == '\n')) {
				i++
			}
			i++
		case r == '\'':
			for i++; i < len(rs); i++ {
				if rs[i] == '\'' {
					if i+1 < len(rs) && rs[i+1] == '\'' {
						i++ // '' is a literal quote
						continue
					}
					break
				}
			}
		case r == '"':
			for i++; i < len(rs) && rs[i] != '"'; i++ {
				if rs[i] == '`' {
					i++
				}
			}
		case r == '(' || r == '{' || r == '[':
			depth++
		case r == ')' || r == '}' || r == ']':
			depth--
		case depth == 0 && i+1 < len(rs) && ((r == '&' && rs[i+1] == '&') || (r == '|' && rs[i+1] == '|')):
			found := string(rs[i : i+2])
			if op == "" {
				op = found
			} else if op != found {
				mixed = true
			}
			parts = append(parts, string(rs[start:i]))
			i++
			start = i + 1
		}
	}
	if len(parts) == 0 {
		return nil, ""
	}
	parts = append(parts, string(rs[start:]))
	if mixed || depth != 0 {
		return parts, ""
	}
	return parts, op
}

func isSpace(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' }
