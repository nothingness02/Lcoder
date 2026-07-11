package codeindex

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// SnapshotVersion is the current on-disk index format version.
const SnapshotVersion = "1"

// FileMeta tracks a file's modification state for incremental updates.
type FileMeta struct {
	ModTime time.Time `json:"mod_time"`
	Size    int64     `json:"size"`
}

// Snapshot holds the persisted code index.
type Snapshot struct {
	Version     string              `json:"version"`
	UpdatedAt   time.Time           `json:"updated_at"`
	Files       map[string]FileMeta `json:"files"`
	Symbols     []Symbol            `json:"symbols"`
	Relations   []Relation          `json:"relations"`
	FailedFiles []string            `json:"failed_files"`
}

// NewSnapshot creates an empty snapshot with the current version.
func NewSnapshot() *Snapshot {
	return &Snapshot{
		Version:   SnapshotVersion,
		UpdatedAt: time.Now(),
		Files:     make(map[string]FileMeta),
	}
}

// LoadSnapshot reads a snapshot from disk. Returns an empty snapshot if the file does not exist.
func LoadSnapshot(path string) (*Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return NewSnapshot(), nil
		}
		return nil, err
	}
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	if s.Files == nil {
		s.Files = make(map[string]FileMeta)
	}
	return &s, nil
}

// SaveSnapshot writes a snapshot to disk atomically.
func SaveSnapshot(path string, s *Snapshot) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	s.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
