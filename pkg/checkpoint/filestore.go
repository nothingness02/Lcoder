package checkpoint

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lcoder/lcoder/internal/fsutil"
)

// FileStore is a Store implementation that persists checkpoints as JSON files
// on the local filesystem. Checkpoints are grouped under one directory per
// session ID and versioned by turn + timestamp. Only the latest Retain
// checkpoints are kept for each session.
type FileStore struct {
	Dir    string
	Retain int
}

// NewFileStore creates a FileStore that writes checkpoints into dir.
func NewFileStore(dir string) *FileStore {
	return &FileStore{Dir: dir, Retain: 5}
}

const checkpointSuffix = ".checkpoint.json"

// Save persists cp under the session identified by id. The file is named with
// the checkpoint turn and a timestamp so that Load can recover the latest version.
func (fs *FileStore) Save(id string, cp *Checkpoint) error {
	if fs.Retain <= 0 {
		fs.Retain = 5
	}
	sessionDir := filepath.Join(fs.Dir, sanitize(id))

	turn := 0
	if cp.Runtime != nil {
		turn = cp.Runtime.Turn
	}
	name := fmt.Sprintf("%d-%d%s", turn, time.Now().UnixMilli(), checkpointSuffix)
	path := filepath.Join(sessionDir, name)

	data, err := cp.MarshalJSON()
	if err != nil {
		return err
	}
	if err := fsutil.WritePrivateFile(path, data); err != nil {
		return err
	}

	return fs.prune(sessionDir)
}

// Load reads the latest checkpoint stored for session id.
func (fs *FileStore) Load(id string) (*Checkpoint, error) {
	sessionDir := filepath.Join(fs.Dir, sanitize(id))
	path, err := fs.latestPath(sessionDir)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	cp := &Checkpoint{}
	if err := cp.UnmarshalJSON(data); err != nil {
		return nil, err
	}
	return cp, nil
}

// LoadLatest is an alias for Load, useful when callers want to be explicit.
func (fs *FileStore) LoadLatest(id string) (*Checkpoint, error) {
	return fs.Load(id)
}

// List returns the identifiers of all sessions that have at least one checkpoint.
func (fs *FileStore) List() ([]string, error) {
	entries, err := os.ReadDir(fs.Dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var ids []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if hasCheckpoints(filepath.Join(fs.Dir, entry.Name())) {
			ids = append(ids, entry.Name())
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// ListVersions returns the checkpoint filenames for a session, oldest first.
func (fs *FileStore) ListVersions(id string) ([]string, error) {
	sessionDir := filepath.Join(fs.Dir, sanitize(id))
	paths, err := checkpointPaths(sessionDir)
	if err != nil {
		return nil, err
	}
	return paths, nil
}

// Delete removes all checkpoint versions for the session.
func (fs *FileStore) Delete(id string) error {
	sessionDir := filepath.Join(fs.Dir, sanitize(id))
	if _, err := os.Stat(sessionDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		}
		return err
	}
	return os.RemoveAll(sessionDir)
}

func (fs *FileStore) prune(sessionDir string) error {
	paths, err := checkpointPaths(sessionDir)
	if err != nil {
		return err
	}
	if len(paths) <= fs.Retain {
		return nil
	}
	for _, p := range paths[:len(paths)-fs.Retain] {
		_ = os.Remove(p)
	}
	return nil
}

func (fs *FileStore) latestPath(sessionDir string) (string, error) {
	paths, err := checkpointPaths(sessionDir)
	if err != nil {
		return "", err
	}
	if len(paths) == 0 {
		return "", ErrNotFound
	}
	return paths[len(paths)-1], nil
}

func checkpointPaths(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var paths []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, checkpointSuffix) {
			continue
		}
		paths = append(paths, filepath.Join(dir, name))
	}
	sort.Slice(paths, func(i, j int) bool {
		return pathOrder(paths[i]) < pathOrder(paths[j])
	})
	return paths, nil
}

func hasCheckpoints(dir string) bool {
	paths, _ := checkpointPaths(dir)
	return len(paths) > 0
}

// pathOrder extracts a numeric ordering from a checkpoint filename. The filename
// is "<turn>-<timestamp>.checkpoint.json"; parse the timestamp for ordering.
func pathOrder(path string) int64 {
	base := filepath.Base(path)
	base = strings.TrimSuffix(base, checkpointSuffix)
	parts := strings.Split(base, "-")
	if len(parts) < 2 {
		return 0
	}
	if ts, err := strconv.ParseInt(parts[len(parts)-1], 10, 64); err == nil {
		return ts
	}
	return 0
}

func sanitize(id string) string {
	return strings.ReplaceAll(id, string(filepath.Separator), "_")
}
