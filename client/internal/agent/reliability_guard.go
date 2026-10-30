package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
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

type verificationRecord struct{ Command, Result string }

func (a *Agent) rememberVerification(id, command, result string) {
	if a.verification == nil {
		a.verification = make(map[string]verificationRecord)
	}
	a.verification[id] = verificationRecord{command, clipContext(result, 1200)}
}

// This checks evidence provenance, not the business meaning of a health check.
// A fresh run and any subsequent command/file mutation invalidate old evidence.
func (a *Agent) finishEvidence(ids []string) (string, error) {
	if len(ids) == 0 {
		return "", fmt.Errorf("success=true requires verification_call_ids from successful run_command or wait_for results; execute a real check and cite its call_id")
	}
	if len(ids) > 16 {
		return "", fmt.Errorf("at most 16 verification_call_ids are allowed")
	}
	var details []string
	for _, id := range ids {
		evidence, ok := a.verification[id]
		if !ok {
			return "", fmt.Errorf("verification call %q is not a successful current check; run verification again after changes", id)
		}
		details = append(details, fmt.Sprintf("- %s: %s\n%s", id, evidence.Command, evidence.Result))
	}
	return strings.Join(details, "\n"), nil
}
