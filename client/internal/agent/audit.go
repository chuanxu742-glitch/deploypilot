package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// AuditEntry is one recorded action.
type AuditEntry struct {
	Time     time.Time `json:"time"`
	Kind     string    `json:"kind"` // command | read_file | write_file | blocked | task | final
	Command  string    `json:"command,omitempty"`
	Path     string    `json:"path,omitempty"`
	ExitCode int       `json:"exit_code,omitempty"`
	Output   string    `json:"output,omitempty"`
	Note     string    `json:"note,omitempty"`
}

// Auditor appends audit entries.
type Auditor interface {
	Append(AuditEntry)
	io.Closer
}

// fileAuditor writes JSONL to ~/.deploypilot/audit-<ts>.jsonl.
type fileAuditor struct {
	mu sync.Mutex
	f  *os.File
}

// NewFileAuditor creates a timestamped audit log under dir.
func NewFileAuditor(dir string) (Auditor, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	name := fmt.Sprintf("audit-%s.jsonl", time.Now().Format("20060102-150405"))
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &fileAuditor{f: f}, nil
}

// DefaultAuditDir returns ~/.deploypilot.
func DefaultAuditDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".deploypilot"), nil
}

func (a *fileAuditor) Append(e AuditEntry) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	_, _ = a.f.Write(append(b, '\n'))
}

func (a *fileAuditor) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.f.Close()
}

// nopAuditor discards entries (used when auditing is unavailable).
type nopAuditor struct{}

// NewNopAuditor returns an auditor that records nothing.
func NewNopAuditor() Auditor { return nopAuditor{} }

func (nopAuditor) Append(AuditEntry) {}
func (nopAuditor) Close() error      { return nil }
