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
	// Filesystem creation / raw disk writes (xvd: Xen/AWS EC2 disks).
	regexp.MustCompile(`\bmkfs\b`),
	regexp.MustCompile(`\bdd\b[^\n]*\bof=/dev/(sd|nvme|vd|xvd|hd|mmcblk|disk)`),
	regexp.MustCompile(`>\s*/dev/(sd|nvme|vd|xvd|hd|mmcblk)`),
	// Windows disk and boot destruction.
	regexp.MustCompile(`(?i)\b(Format-Volume|Clear-Disk|Initialize-Disk|Remove-Partition)\b`),
	regexp.MustCompile(`(?i)\bdiskpart\b`),
	regexp.MustCompile(`(?i)\bformat(\.com)?\s+[a-z]:`),
	regexp.MustCompile(`(?i)\bbcdedit\b[^\n]*\s/delete\b`),
	regexp.MustCompile(`(?i)\bvssadmin\b[^\n]*\bdelete\s+shadows\b`),
	regexp.MustCompile(`(?i)\bcipher\b[^\n]*\s/w:`),
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
	regexp.MustCompile(`(?i)\b(Stop-Computer|Restart-Computer)\b`),
	regexp.MustCompile(`(?i)` + cmdStart + `shutdown(\.exe)?\s+/[srpgh]\b`),
}

// Windows recursive deletion of a drive root or the Windows directory needs
// three independent conditions, which RE2 cannot express in one pattern.
var (
	winDeleteVerb  = regexp.MustCompile(`(?i)(^|[\s;&|(])(Remove-Item|ri|rm|del|erase|rd|rmdir)\s`)
	winRecursive   = regexp.MustCompile(`(?i)(\s-r(ecurse)?\b|\s/s\b)`)
	winRootTargets = regexp.MustCompile(`(?i)(^|\s)["']?([a-z]:\\?|[a-z]:\\windows\\?|\$env:(systemroot|systemdrive|windir)\\?)["']?(\s|$|;)`)
)

func windowsRootWipe(c string) bool {
	return winDeleteVerb.MatchString(c) && winRecursive.MatchString(c) && winRootTargets.MatchString(c)
}

// unquote removes quote characters so `rm -rf "/"` is checked like `rm -rf /`.
func unquote(s string) string { return strings.NewReplacer(`"`, "", `'`, "").Replace(s) }

// quotedRe matches single- or double-quoted shell strings.
var quotedRe = regexp.MustCompile(`'[^']*'|"(?:[^"\\]|\\.)*"`)

// stripQuoted replaces quoted strings with an empty pair of quotes.
func stripQuoted(s string) string {
	return quotedRe.ReplaceAllString(s, `""`)
}

// CheckCommand returns a non-empty reason when command is blocked.
func CheckCommand(command string) (blocked bool, reason string) {
	c := strings.TrimSpace(command)
	for _, candidate := range []string{c, unquote(c)} {
		for _, re := range dangerousPatterns {
			if re.MatchString(candidate) {
				return true, "command matches a destructive-operation safety rule and was blocked: " + re.String()
			}
		}
		if windowsRootWipe(candidate) {
			return true, "recursive deletion of a drive root or the Windows directory is not allowed"
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
