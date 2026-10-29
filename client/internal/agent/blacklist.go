package agent

import (
	"regexp"
	"strings"
)

// dangerousPatterns are hard-blocked even in fully-automatic mode. They are
// matched against the raw command. The agent is told why a command was
// refused so it can choose a safer path.
var dangerousPatterns = []*regexp.Regexp{
	// Recursive root / home wipes.
	regexp.MustCompile(`\brm\b[^\n]*\s-[a-zA-Z]*r[a-zA-Z]*f?[^\n]*\s+(/|/\*|~|~/\*|\$HOME)(\s|$)`),
	regexp.MustCompile(`\brm\b[^\n]*\s-[a-zA-Z]*f[a-zA-Z]*r?[^\n]*\s+(/|/\*|~)(\s|$)`),
	// Filesystem creation / raw disk writes.
	regexp.MustCompile(`\bmkfs\b`),
	regexp.MustCompile(`\bdd\b[^\n]*\bof=/dev/(sd|nvme|vd|hd|mmcblk|disk)`),
	regexp.MustCompile(`>\s*/dev/(sd|nvme|vd|hd|mmcblk)`),
	// Whole-disk / partition wipes.
	regexp.MustCompile(`\bwipefs\b`),
	regexp.MustCompile(`\bshred\b[^\n]*\s/dev/`),
	// Fork bomb.
	regexp.MustCompile(`:\(\)\s*\{\s*:\|:&\s*\}\s*;`),
	// Recursive permission/ownership changes on the root directory.
	regexp.MustCompile(`\bchmod\b[^\n]*\s-R[^\n]*\s7{3}\s+/(\s|$)`),
	regexp.MustCompile(`\bchown\b[^\n]*\s-R[^\n]*\s+/(\s|$)`),
}

// cmdStart matches a position where a new shell command can begin: the start
// of input, after a separator (; & | newline ( or backtick), or after a
// command prefix such as sudo/exec/nohup/env and its flags.
const cmdStart = "(?:^|[;&|\\n(`]|\\b(?:sudo|exec|nohup|env)\\s+(?:-\\S+\\s+)*)\\s*"

// powerPatterns block power control only when it is used as a command. They
// run on the command with quoted strings removed, so read-only commands that
// merely mention the words (grep -E "shutdown|reboot", last -x) are allowed.
var powerPatterns = []*regexp.Regexp{
	regexp.MustCompile(cmdStart + `(?:/usr)?(?:/s?bin/)?(shutdown|reboot|poweroff|halt)\b`),
	regexp.MustCompile(cmdStart + `(?:/usr)?(?:/s?bin/)?init\s+[06]\b`),
	regexp.MustCompile(cmdStart + `(?:/usr)?(?:/s?bin/)?telinit\s+[06]\b`),
	regexp.MustCompile(`\bsystemctl\s+(?:-\S+\s+)*(poweroff|reboot|halt|kexec)\b`),
}

// quotedRe matches single- or double-quoted shell strings.
var quotedRe = regexp.MustCompile(`'[^']*'|"(?:[^"\\]|\\.)*"`)

// stripQuoted replaces quoted strings with an empty pair of quotes.
func stripQuoted(s string) string {
	return quotedRe.ReplaceAllString(s, `""`)
}

// CheckCommand returns a non-empty reason when command is blocked.
func CheckCommand(command string) (blocked bool, reason string) {
	c := strings.TrimSpace(command)
	for _, re := range dangerousPatterns {
		if re.MatchString(c) {
			return true, "command matches a destructive-operation safety rule and was blocked: " + re.String()
		}
	}
	unquoted := stripQuoted(c)
	for _, re := range powerPatterns {
		if m := re.FindString(unquoted); m != "" {
			return true, "power-control commands (shutdown/reboot/poweroff/halt) are not allowed: " + strings.TrimSpace(m)
		}
	}
	return false, ""
}
