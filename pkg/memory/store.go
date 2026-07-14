package memory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lcoder/lcoder/internal/fsutil"
	"github.com/lcoder/lcoder/internal/paths"
)

// Target identifies a memory channel.
type Target int

const (
	MemoryTarget Target = iota
	UserTarget
)

func targetName(t Target) string {
	switch t {
	case UserTarget:
		return "USER"
	default:
		return "MEMORY"
	}
}

// Limits holds per-channel character caps. Zero values fall back to defaults.
type Limits struct {
	MemoryCharLimit int
	UserCharLimit   int
}

// Store reads and writes memory files. It combines global (user home) and
// project (cwd) files for reads, and writes to the global file only.
type Store struct {
	globalDir        string
	projectDir       string
	limits           Limits
	writeMu          sync.Mutex
	cacheMu          sync.Mutex
	memoryCache      []string
	userCache        []string
	memoryCacheValid bool
	userCacheValid   bool
}

// NewStore creates a store rooted at cwd. The global directory is
// ~/.lcoder/memory.
func NewStore(cwd string) (*Store, error) {
	return &Store{
		globalDir:  paths.LCoderHome("memory"),
		projectDir: filepath.Join(cwd, ".lcoder", "memory"),
		limits: Limits{
			MemoryCharLimit: DefaultMemoryCharLimit,
			UserCharLimit:   DefaultUserCharLimit,
		},
	}, nil
}

// WithLimits overrides the default character limits.
func (s *Store) WithLimits(l Limits) *Store {
	s.limits = l
	return s
}

func (s *Store) limitFor(t Target) int {
	switch t {
	case UserTarget:
		if s.limits.UserCharLimit > 0 {
			return s.limits.UserCharLimit
		}
		return DefaultUserCharLimit
	default:
		if s.limits.MemoryCharLimit > 0 {
			return s.limits.MemoryCharLimit
		}
		return DefaultMemoryCharLimit
	}
}

func (s *Store) globalPath(t Target) string {
	name := targetName(t) + ".md"
	return filepath.Join(s.globalDir, name)
}

func (s *Store) projectPath(t Target) string {
	name := targetName(t) + ".md"
	return filepath.Join(s.projectDir, name)
}

func (s *Store) loadFile(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return parseEntries(string(data)), nil
}

func (s *Store) saveFile(path string, t Target, entries []string) error {
	data := formatFile(targetName(t), entries, s.limitFor(t))
	tmp := path + ".tmp"
	if err := fsutil.WritePrivateFile(tmp, []byte(data)); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// GlobalEntries returns entries from the global file.
func (s *Store) GlobalEntries(t Target) ([]string, error) {
	s.cacheMu.Lock()
	if t == MemoryTarget && s.memoryCacheValid {
		cached := append([]string(nil), s.memoryCache...)
		s.cacheMu.Unlock()
		return cached, nil
	}
	if t == UserTarget && s.userCacheValid {
		cached := append([]string(nil), s.userCache...)
		s.cacheMu.Unlock()
		return cached, nil
	}
	s.cacheMu.Unlock()

	entries, err := s.loadFile(s.globalPath(t))
	if err != nil {
		return nil, err
	}

	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if t == MemoryTarget {
		s.memoryCache = append([]string(nil), entries...)
		s.memoryCacheValid = true
	} else {
		s.userCache = append([]string(nil), entries...)
		s.userCacheValid = true
	}
	return entries, nil
}

func (s *Store) invalidateCache(t Target) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if t == MemoryTarget {
		s.memoryCacheValid = false
		s.memoryCache = nil
	} else {
		s.userCacheValid = false
		s.userCache = nil
	}
}

// ProjectEntries returns entries from the project file.
func (s *Store) ProjectEntries(t Target) ([]string, error) {
	return s.loadFile(s.projectPath(t))
}

func (s *Store) allEntries(t Target) ([]string, error) {
	global, err := s.GlobalEntries(t)
	if err != nil {
		return nil, err
	}
	project, err := s.ProjectEntries(t)
	if err != nil {
		return nil, err
	}
	return append(global, project...), nil
}

func (s *Store) textFor(t Target) (string, error) {
	entries, err := s.allEntries(t)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "", nil
	}
	title := "Agent memory"
	if t == UserTarget {
		title = "User profile"
	}
	return title + ":\n\n" + strings.Join(entries, "\n\n"), nil
}

// MemoryText returns the merged global+project memory text for injection.
func (s *Store) MemoryText() (string, error) { return s.textFor(MemoryTarget) }

// UserText returns the merged global+project user profile text for injection.
func (s *Store) UserText() (string, error) { return s.textFor(UserTarget) }

// Add appends a new entry to the global file. Duplicate entries are silently ignored.
func (s *Store) Add(t Target, content string) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return fmt.Errorf("content cannot be empty")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	entries, err := s.GlobalEntries(t)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e == content {
			return nil
		}
	}
	limit := s.limitFor(t)
	if charCount(entries)+len(content) > limit {
		return fmt.Errorf("%s at %d/%d chars. Adding this entry (%d chars) would exceed the limit. Consolidate now: use 'replace' to merge overlapping entries into shorter ones or 'remove' stale entries, then retry this add.", targetName(t), charCount(entries), limit, len(content))
	}
	entries = append(entries, content)
	if err := s.saveFile(s.globalPath(t), t, entries); err != nil {
		return err
	}
	s.invalidateCache(t)
	return nil
}

// Replace updates the unique entry matching oldText with content.
func (s *Store) Replace(t Target, oldText, content string) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return fmt.Errorf("content cannot be empty")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	entries, err := s.GlobalEntries(t)
	if err != nil {
		return err
	}
	idx, err := findEntryIndex(entries, oldText)
	if err != nil {
		return err
	}
	newEntries := make([]string, len(entries))
	copy(newEntries, entries)
	newEntries[idx] = content
	limit := s.limitFor(t)
	if charCount(newEntries) > limit {
		return fmt.Errorf("%s at %d/%d chars. Replacing would exceed the limit. Shorten the new content or remove other entries first.", targetName(t), charCount(entries), limit)
	}
	if err := s.saveFile(s.globalPath(t), t, newEntries); err != nil {
		return err
	}
	s.invalidateCache(t)
	return nil
}

// Remove deletes the unique entry matching oldText.
func (s *Store) Remove(t Target, oldText string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	entries, err := s.GlobalEntries(t)
	if err != nil {
		return err
	}
	idx, err := findEntryIndex(entries, oldText)
	if err != nil {
		return err
	}
	entries = append(entries[:idx], entries[idx+1:]...)
	if err := s.saveFile(s.globalPath(t), t, entries); err != nil {
		return err
	}
	s.invalidateCache(t)
	return nil
}

// UsageString returns "used/limit" for the global channel.
func (s *Store) UsageString(t Target) (string, error) {
	entries, err := s.GlobalEntries(t)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d/%d", charCount(entries), s.limitFor(t)), nil
}
