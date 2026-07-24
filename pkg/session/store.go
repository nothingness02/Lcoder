package session

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lcoder/lcoder/internal/fsutil"
	"github.com/lcoder/lcoder/internal/paths"
	"github.com/lcoder/lcoder/pkg/models"
)

// DefaultDir returns the default session directory.
func DefaultDir() string {
	return paths.LCoderHome("sessions")
}

// hashCWD creates a stable directory name for a project path.
func hashCWD(cwd string) string {
	sum := sha256.Sum256([]byte(cwd))
	return fmt.Sprintf("%x", sum)[:16]
}

// Store persists session data.
type Store struct {
	Dir string
}

// NewStore creates a session store.
func NewStore(dir string) *Store {
	if dir == "" {
		dir = DefaultDir()
	}
	return &Store{Dir: dir}
}

// Session is a persisted conversation. It supports linear history and optional
// in-memory branching: each message records its parent message id, and Fork
// creates a new branch from an existing message. Old sessions without parent
// ids are treated as linear history.
type Session struct {
	ID        string
	Path      string
	CWD       string
	Messages  []models.AgentMessage
	CreatedAt int64
	ParentID  *string `json:"parent_id,omitempty"`
	Branches  []string

	activeBranch string
	branchHeads  map[string]string
}

const mainBranch = "main"

// Create initializes a new session for the given working directory.
// It does not write a session file until the first message is appended, so
// opening the app and quitting without any conversation produces no record.
func (s *Store) Create(cwd string) (*Session, error) {
	id := uuid.New().String()[:12]
	sess := &Session{
		ID:        id,
		Path:      s.sessionPath(cwd, id),
		CWD:       cwd,
		Messages:  []models.AgentMessage{},
		CreatedAt: time.Now().Unix(),
	}
	sess.initBranchState()
	if err := fsutil.EnsurePrivateDir(filepath.Dir(sess.Path)); err != nil {
		return nil, err
	}
	return sess, nil
}

// Load reads a session by its file path.
func (s *Store) Load(path string) (*Session, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sess := &Session{Path: path}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var msg models.AgentMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			return nil, fmt.Errorf("invalid session line: %w", err)
		}
		sess.Messages = append(sess.Messages, msg)
		if sess.ID == "" && msg.Metadata != nil {
			if id, ok := msg.Metadata["session_id"].(string); ok {
				sess.ID = id
			}
		}
		if sess.CWD == "" && msg.Metadata != nil {
			if cwd, ok := msg.Metadata["cwd"].(string); ok {
				sess.CWD = cwd
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	sess.initBranchState()
	return sess, nil
}

// LoadByID loads a session by project and session id.
func (s *Store) LoadByID(cwd, id string) (*Session, error) {
	return s.Load(s.sessionPath(cwd, id))
}

// List returns metadata for sessions in a project.
func (s *Store) List(cwd string) ([]Session, error) {
	dir := filepath.Join(s.Dir, hashCWD(cwd))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var sessions []Session
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		sess, err := s.Load(path)
		if err != nil {
			continue
		}
		sessions = append(sessions, *sess)
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].modifiedTime() > sessions[j].modifiedTime()
	})
	return sessions, nil
}

// MostRecent returns the most recently modified session for a project.
func (s *Store) MostRecent(cwd string) (*Session, error) {
	sessions, err := s.List(cwd)
	if err != nil {
		return nil, err
	}
	if len(sessions) == 0 {
		return nil, fmt.Errorf("no sessions found")
	}
	return &sessions[0], nil
}

// AppendMissing appends every message from msgs whose ID is not already present
// in the session, preserving order. The TUI and one-shot runner only Append the
// user message at submit time; the agent's assistant and tool_result messages
// live in its context window and must be mirrored in here after a turn so they
// actually reach disk. Dedup is by message ID, making repeated calls idempotent.
//
// When the active branch already carries a compaction entry, a runtime summary
// message (Metadata["compacted"] == true) is skipped: the entry already
// represents it, and persisting the raw summary would duplicate it in
// EffectiveMessages. Branches without an entry (legacy sessions, degraded
// folds) keep the old behavior and persist such summaries as normal messages.
func (s *Session) AppendMissing(msgs []models.AgentMessage) error {
	have := make(map[string]bool, len(s.Messages))
	for _, m := range s.Messages {
		have[m.ID] = true
	}
	hasEntry := false
	for _, m := range s.ActiveMessages() {
		if IsCompactionEntry(m) {
			hasEntry = true
			break
		}
	}
	for _, m := range msgs {
		if m.ID == "" || have[m.ID] {
			continue
		}
		if hasEntry {
			if compacted, _ := m.Metadata["compacted"].(bool); compacted {
				continue
			}
		}
		if err := s.Append(m); err != nil {
			return err
		}
		have[m.ID] = true
	}
	return nil
}

// Append adds a message to the current branch and persists it.
// The new message's ParentID is set to the current branch head when empty,
// establishing the parent_id tree used by Fork and ActiveMessages.
func (s *Session) Append(msg models.AgentMessage) error {
	if err := s.stage(msg); err != nil {
		return err
	}
	return s.Save()
}

// stage applies the common metadata/parent wiring and appends the message to
// the in-memory list (shared by Append and AppendCompactionEntry).
func (s *Session) stage(msg models.AgentMessage) error {
	if msg.Metadata == nil {
		msg.Metadata = make(map[string]any)
	}
	msg.Metadata["session_id"] = s.ID
	msg.Metadata["cwd"] = s.CWD
	msg.Metadata["saved_at"] = time.Now().UnixMilli()
	msg.Metadata["branch_id"] = s.activeBranch

	if msg.ID == "" {
		msg.ID = uuid.New().String()[:12]
	}
	if msg.ParentID == nil || *msg.ParentID == "" {
		if head, ok := s.branchHeads[s.activeBranch]; ok && head != "" {
			msg.ParentID = &head
		}
	}

	s.Messages = append(s.Messages, msg)
	s.branchHeads[s.activeBranch] = msg.ID
	return nil
}

// Metadata keys for compaction entries.
const (
	MetaType             = "type"
	MetaTypeCompaction   = "compaction"
	MetaFirstKeptEntryID = "first_kept_entry_id"
	MetaTokensBefore     = "tokens_before"
)

// IsCompactionEntry reports whether m is an append-only compaction entry.
func IsCompactionEntry(m models.AgentMessage) bool {
	if m.Metadata == nil {
		return false
	}
	v, ok := m.Metadata[MetaType].(string)
	return ok && v == MetaTypeCompaction
}

// AppendCompactionEntry appends a compaction entry to the session without
// rewriting existing lines: raw history is never discarded. The entry carries
// the summary text and the id of the first kept message; EffectiveMessages
// uses it to rebuild the compacted view. Parent is the current branch head,
// so the branch chain stays continuous through the entry.
func (s *Session) AppendCompactionEntry(summary, firstKeptEntryID string, tokensBefore int) error {
	msg := models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: summary})
	msg.Metadata[MetaType] = MetaTypeCompaction
	msg.Metadata[MetaFirstKeptEntryID] = firstKeptEntryID
	msg.Metadata[MetaTokensBefore] = tokensBefore

	if err := s.stage(msg); err != nil {
		return err
	}
	// Persist the staged copy (which carries the parent wiring), not the
	// caller's by-value msg.
	return s.appendLine(s.Messages[len(s.Messages)-1])
}

// Metadata keys for custom entries written by extensions.
const (
	MetaTypeCustom = "custom"
	MetaCustomType = "custom_type"
	MetaCustomData = "data"
)

// CustomEntry is an extension-owned record read back from the session file.
type CustomEntry struct {
	CustomType string
	Data       json.RawMessage
}

// IsCustomEntry reports whether m is an extension custom entry.
func IsCustomEntry(m models.AgentMessage) bool {
	if m.Metadata == nil {
		return false
	}
	v, ok := m.Metadata[MetaType].(string)
	return ok && v == MetaTypeCustom
}

// AppendCustomEntry appends an extension-owned entry to the current branch.
// The entry carries parent_id/branch_id like any message, so it follows fork
// and branch semantics, but role=custom keeps it out of context views. Data is
// validated and compacted (normalized) before staging, so invalid JSON is
// rejected without poisoning the in-memory session, and the in-memory bytes
// match what a later reload would produce.
func (s *Session) AppendCustomEntry(customType string, data json.RawMessage) error {
	var buf bytes.Buffer
	if err := json.Compact(&buf, data); err != nil {
		return fmt.Errorf("session: custom entry %q: invalid JSON data: %w", customType, err)
	}
	msg := models.NewAgentMessage(models.RoleCustom)
	msg.Metadata[MetaType] = MetaTypeCustom
	msg.Metadata[MetaCustomType] = customType
	msg.Metadata[MetaCustomData] = json.RawMessage(buf.Bytes())
	if err := s.stage(msg); err != nil {
		return err
	}
	return s.appendLine(s.Messages[len(s.Messages)-1])
}

// CustomEntries returns the custom entries on the active branch whose
// custom_type starts with prefix. An empty prefix returns ALL custom entries,
// and the match is a plain string prefix ("ext" also matches "ext2/..."), so
// extensions should use the "<ext-name>/" convention.
//
// Data is normalized JSON: compaction at append time and a file reload both
// drop insignificant whitespace and object key order. After a reload the
// metadata decodes as generic any, so integral values must stay within the
// float64-safe range (< 2^53) or they lose precision.
func (s *Session) CustomEntries(prefix string) []CustomEntry {
	var out []CustomEntry
	for _, m := range s.activeChain() {
		if !IsCustomEntry(m) {
			continue
		}
		customType, _ := m.Metadata[MetaCustomType].(string)
		if !strings.HasPrefix(customType, prefix) {
			continue
		}
		// After a file reload the metadata value decodes as generic any, so
		// re-marshal instead of asserting to json.RawMessage.
		raw, err := json.Marshal(m.Metadata[MetaCustomData])
		if err != nil {
			continue
		}
		out = append(out, CustomEntry{CustomType: customType, Data: raw})
	}
	return out
}

// appendLine persists exactly one message by appending it to the session file,
// preserving every existing byte. Creates the file if it does not exist yet.
func (s *Session) appendLine(msg models.AgentMessage) error {
	if err := fsutil.EnsurePrivateDir(filepath.Dir(s.Path)); err != nil {
		return err
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chmod(s.Path, 0o600)
}

// Fork creates a new branch starting at msgID and switches the session to it.
// It returns the new branch id. The session's Messages are not duplicated; the
// branch is represented by the parent_id tree and the active branch pointer.
// An empty msgID forks at the root (before the first message), mirroring pi's
// resetLeaf: the next appended message starts the branch with no parent.
func (s *Session) Fork(msgID string) (string, error) {
	if msgID != "" {
		found := false
		for _, m := range s.Messages {
			if m.ID == msgID {
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("session: cannot fork: message %q not found", msgID)
		}
	}

	branchID := "branch-" + uuid.New().String()[:8]
	s.Branches = append(s.Branches, branchID)
	s.activeBranch = branchID
	s.branchHeads[branchID] = msgID
	return branchID, nil
}

// Save writes all messages to the session file using an atomic temp-file +
// rename so a crash mid-write cannot leave a truncated/corrupt JSONL.
func (s *Session) Save() error {
	if err := fsutil.EnsurePrivateDir(filepath.Dir(s.Path)); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.Path), ".session-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	for _, msg := range s.Messages {
		data, err := json.Marshal(msg)
		if err != nil {
			tmp.Close()
			return err
		}
		if _, err := tmp.Write(append(data, '\n')); err != nil {
			tmp.Close()
			return err
		}
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.Path); err != nil {
		return err
	}
	return os.Chmod(s.Path, 0o600)
}

// Replace overwrites the session's entire conversation with msgs and persists
// it.
//
// Deprecated: compaction persistence now uses AppendCompactionEntry, which is
// append-only and never discards raw messages. Replace is kept for tests.
func (s *Session) Replace(msgs []models.AgentMessage) error {
	s.Messages = append([]models.AgentMessage(nil), msgs...)
	s.initBranchState()
	return s.Save()
}

// ActiveMessages returns the messages on the current branch, reconstructed by
// walking the parent_id tree from the active branch head, with extension
// custom entries (role=custom) filtered out — they persist on disk but never
// enter model context. A branch forked at the root yields no messages until
// one is appended. Legacy files are returned as a single linear conversation.
func (s *Session) ActiveMessages() []models.AgentMessage {
	chain := s.activeChain()
	out := chain[:0] // in-place filter; chain is already a fresh slice
	for _, m := range chain {
		if m.Role == models.RoleCustom || IsCustomEntry(m) {
			continue
		}
		out = append(out, m)
	}
	return out
}

// activeChain is the unfiltered branch walk (includes custom entries).
// A branch forked at the root has an empty head and yields no messages until
// one is appended. Legacy files (written before branch metadata existed) are
// returned as a single linear conversation.
func (s *Session) activeChain() []models.AgentMessage {
	head, ok := s.branchHeads[s.activeBranch]
	if !ok {
		return append([]models.AgentMessage(nil), s.Messages...)
	}
	if head == "" {
		// Explicit root fork: the branch starts before the first message.
		return nil
	}

	byID := make(map[string]models.AgentMessage, len(s.Messages))
	for _, m := range s.Messages {
		byID[m.ID] = m
	}

	headMsg, ok := byID[head]
	if !ok {
		return append([]models.AgentMessage(nil), s.Messages...)
	}
	// Compatibility: every message appended by current code carries branch_id
	// metadata, so a head without it means the file is a legacy linear
	// conversation rather than a tree.
	if _, ok := headMsg.Metadata["branch_id"]; !ok {
		return append([]models.AgentMessage(nil), s.Messages...)
	}

	var branch []models.AgentMessage
	for cur := head; cur != ""; {
		m, ok := byID[cur]
		if !ok {
			break
		}
		branch = append(branch, m)
		if m.ParentID == nil {
			break
		}
		cur = *m.ParentID
	}

	// Reverse so the oldest ancestor comes first.
	for i, j := 0, len(branch)-1; i < j; i, j = i+1, j-1 {
		branch[i], branch[j] = branch[j], branch[i]
	}
	return branch
}

// EffectiveMessages returns the compacted view of the active branch: the
// newest compaction entry's summary plus the branch messages from its
// first_kept_entry_id onwards (falling back to all post-entry messages when
// that id is not on the branch). Without any compaction entry it is identical
// to ActiveMessages. Raw messages always remain on disk; this is only the
// view fed to the runtime context.
func (s *Session) EffectiveMessages() []models.AgentMessage {
	active := s.ActiveMessages()
	entryIdx := -1
	for i := len(active) - 1; i >= 0; i-- {
		if IsCompactionEntry(active[i]) {
			entryIdx = i
			break
		}
	}
	if entryIdx < 0 {
		return active
	}

	entry := active[entryIdx]
	after := active[entryIdx+1:]

	// The kept tail normally starts at first_kept_entry_id, which may sit
	// before the entry (the entry is appended after the messages it
	// summarizes). Search the whole branch for it and take everything from
	// there up to the entry, then the post-entry messages. When the id is not
	// on the branch, fall back to the post-entry messages only.
	kept := after
	if firstKept, _ := entry.Metadata[MetaFirstKeptEntryID].(string); firstKept != "" {
		for i := 0; i < entryIdx; i++ {
			if active[i].ID == firstKept {
				kept = append(append([]models.AgentMessage(nil), active[i:entryIdx]...), after...)
				break
			}
		}
	}

	summary := entry
	summary.Metadata = make(map[string]any, len(entry.Metadata)+1)
	for k, v := range entry.Metadata {
		summary.Metadata[k] = v
	}
	delete(summary.Metadata, MetaType)
	summary.Metadata["compacted"] = true

	out := make([]models.AgentMessage, 0, len(kept)+1)
	out = append(out, summary)
	out = append(out, kept...)
	return out
}

// SwitchBranch changes the active branch. It returns an error if the branch
// does not exist or has no recorded head.
func (s *Session) SwitchBranch(branchID string) error {
	if branchID == mainBranch {
		s.activeBranch = mainBranch
		return nil
	}
	found := false
	for _, b := range s.Branches {
		if b == branchID {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("session: branch %q not found", branchID)
	}
	if _, ok := s.branchHeads[branchID]; !ok {
		return fmt.Errorf("session: branch %q has no head", branchID)
	}
	s.activeBranch = branchID
	return nil
}

// ActiveBranch returns the id of the currently selected branch.
func (s *Session) ActiveBranch() string {
	return s.activeBranch
}

func (s *Store) sessionPath(cwd, id string) string {
	return filepath.Join(s.Dir, hashCWD(cwd), id+".jsonl")
}

func (s *Session) modifiedTime() int64 {
	info, err := os.Stat(s.Path)
	if err != nil {
		return 0
	}
	return info.ModTime().Unix()
}

// SessionID returns the session identifier.
func (s *Session) SessionID() string {
	return s.ID
}

// initBranchState rebuilds the branch registry and branch head map from the
// loaded messages. It is called after Create, Load, and Replace. Mirroring
// pi's leaf semantics, the active branch resumes where the file's last line
// left off, so returning to a session lands on the branch that was being
// worked on instead of resetting to main.
func (s *Session) initBranchState() {
	s.activeBranch = mainBranch
	s.branchHeads = make(map[string]string)
	s.Branches = nil
	seen := make(map[string]bool)
	for _, m := range s.Messages {
		branchID := branchOf(m)
		s.branchHeads[branchID] = m.ID
		if branchID != mainBranch && !seen[branchID] {
			seen[branchID] = true
			s.Branches = append(s.Branches, branchID)
		}
	}
	if n := len(s.Messages); n > 0 {
		s.activeBranch = branchOf(s.Messages[n-1])
	}
}

// branchOf returns the branch a message was appended to, defaulting to main
// for messages written before branch metadata existed.
func branchOf(m models.AgentMessage) string {
	if m.Metadata != nil {
		if bid, ok := m.Metadata["branch_id"].(string); ok && bid != "" {
			return bid
		}
	}
	return mainBranch
}
