package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/deploypilot/deploypilot/internal/llm"
)

// repetitionGuard compares consecutive calls and observed results, ignoring
// per-call metadata so new log filenames cannot disguise an unchanged result.
type repetitionGuard struct {
	last  [32]byte
	count int
}

func (g *repetitionGuard) observe(call llm.ToolCall, result llm.Message) (warn, stop bool) {
	args := call.Function.Arguments
	var decoded any
	if json.Unmarshal([]byte(args), &decoded) == nil {
		if canonical, err := json.Marshal(decoded); err == nil {
			args = string(canonical)
		}
	}
	var lines []string
	for _, line := range strings.Split(result.Content, "\n") {
		if strings.HasPrefix(line, "[call_id: ") || strings.HasPrefix(line, "[full output: ") {
			continue
		}
		lines = append(lines, line)
	}
	fingerprint := sha256.Sum256([]byte(call.Function.Name + "\x00" + args + "\x00" + strings.TrimSpace(strings.Join(lines, "\n"))))
	if g.count > 0 && fingerprint == g.last {
		g.count++
	} else {
		g.last, g.count = fingerprint, 1
	}
	return g.count == 3, g.count >= 4
}

type verificationRecord struct {
	Command, Result string
	seq             int // order the check succeeded in, for listing
}

// noteCheck records that a run_command or wait_for call started, so a
// rejected finish can say why an ID does not count. ok=false until it succeeds.
func (a *Agent) noteCheck(id string) {
	if a.checks == nil {
		a.checks = make(map[string]bool)
	}
	a.checks[id] = false
}

func (a *Agent) rememberVerification(id, command, result string) {
	if a.verification == nil {
		a.verification = make(map[string]verificationRecord)
	}
	if a.checks == nil {
		a.checks = make(map[string]bool)
	}
	a.checks[id] = true
	a.verification[id] = verificationRecord{command, clipContext(result, 1200), len(a.checks)}
}

// This checks evidence provenance, not the business meaning of a health check.
// A fresh run and any subsequent command/file mutation invalidate old evidence.
// Rejections name the reason and the IDs that would be accepted, because a
// model that is only told "not current" tends to rerun every check again.
func (a *Agent) finishEvidence(ids []string) (string, error) {
	if len(ids) == 0 {
		return "", fmt.Errorf("success=true requires verification_call_ids from successful run_command or wait_for results; execute a real check and cite its call_id. %s", a.currentEvidenceHint())
	}
	if len(ids) > 16 {
		return "", fmt.Errorf("at most 16 verification_call_ids are allowed")
	}
	var details, problems []string
	for _, id := range ids {
		evidence, ok := a.verification[id]
		if !ok {
			problems = append(problems, fmt.Sprintf("%q %s", id, a.evidenceProblem(id)))
			continue
		}
		details = append(details, evidenceBlock(id, evidence.Command, evidence.Result))
	}
	if len(problems) > 0 {
		return "", fmt.Errorf("verification not accepted: %s. %s", strings.Join(problems, "; "), a.currentEvidenceHint())
	}
	return strings.Join(details, "\n"), nil
}

// evidenceBlock formats one verification record for the final summary. The
// summary is rendered as Markdown, so raw command output would lose its line
// breaks and turn "----" rows or "*" into headings and emphasis. A fenced
// block keeps it literal; the fence is longer than any backtick run inside.
func evidenceBlock(id, command, result string) string {
	body := strings.TrimRight("> "+command+"\n"+result, "\n")
	longest, run := 0, 0
	for _, r := range body {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", max(3, longest+1))
	return fmt.Sprintf("%s\n%s\n%s\n%s", id, fence+"text", body, fence)
}

func (a *Agent) evidenceProblem(id string) string {
	succeeded, seen := a.checks[id]
	switch {
	case !seen:
		return "is not a run_command or wait_for call in this run (other tools and earlier runs cannot be cited)"
	case !succeeded:
		return "did not succeed (non-zero exit, error or timeout), so it cannot prove success"
	default:
		return "is no longer current: a later command, file change or operator instruction came after it"
	}
}

// currentEvidenceHint lists the IDs finish would accept now.
func (a *Agent) currentEvidenceHint() string {
	const rule = "Only the most recent run_command, and wait_for calls after it, count as current evidence; any later command or file change invalidates earlier IDs."
	if len(a.verification) == 0 {
		return rule + " No check is current now: run all final checks together in one run_command, then call finish citing only that call_id."
	}
	ids := make([]string, 0, len(a.verification))
	for id := range a.verification {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return a.verification[ids[i]].seq < a.verification[ids[j]].seq })
	return rule + " Currently accepted: " + strings.Join(ids, ", ") + "."
}
