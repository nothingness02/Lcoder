package observability

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// AuditRecord describes a single tool invocation for security auditing.
type AuditRecord struct {
	Timestamp   int64          `json:"timestamp"`
	SessionID   string         `json:"session_id"`
	Turn        int            `json:"turn"`
	ToolCallID  string         `json:"tool_call_id"`
	ToolName    string         `json:"tool_name"`
	Args        map[string]any `json:"args"`
	Decision    string         `json:"decision"` // allow | ask | deny | block
	Allowed     bool           `json:"allowed"`
	Blocked     bool           `json:"blocked"`
	BlockReason string         `json:"block_reason,omitempty"`
	IsError     bool           `json:"is_error"`
	Error       string         `json:"error,omitempty"`
	DurationMs  int64          `json:"duration_ms"`
}

// AuditLogger writes audit records.
type AuditLogger interface {
	Log(record AuditRecord) error
	Close() error
}

// FileAuditLogger writes audit records to a JSONL file.
type FileAuditLogger struct {
	path string
	file *os.File
	mu   sync.Mutex
}

// NewFileAuditLogger creates an audit logger backed by a JSONL file.
func NewFileAuditLogger(path string) (*FileAuditLogger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	return &FileAuditLogger{path: path, file: f}, nil
}

// Log writes a single audit record.
func (l *FileAuditLogger) Log(record AuditRecord) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal audit record: %w", err)
	}
	if _, err := l.file.Write(data); err != nil {
		return err
	}
	if _, err := l.file.WriteString("\n"); err != nil {
		return err
	}
	return nil
}

// Close flushes and closes the underlying file.
func (l *FileAuditLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	return l.file.Close()
}

// DefaultAuditPath returns the default audit log path for a session.
func DefaultAuditPath(sessionID string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".lcoder", "audit", sessionID+".jsonl")
}
