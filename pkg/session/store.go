package session

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lcoder/lcoder/pkg/models"
)

// DefaultDir returns the default session directory.
func DefaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".lcoder", "sessions")
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
	if err := os.MkdirAll(filepath.Dir(sess.Path), 0o700); err != nil {
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
func (s *Session) AppendMissing(msgs []models.AgentMessage) error {
	have := make(map[string]bool, len(s.Messages))
	for _, m := range s.Messages {
		have[m.ID] = true
	}
	for _, m := range msgs {
		if m.ID == "" || have[m.ID] {
			continue
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

	return s.Save()
}

// Fork creates a new branch starting at msgID and switches the session to it.
// It returns the new branch id. The session's Messages are not duplicated; the
// branch is represented by the parent_id tree and the active branch pointer.
func (s *Session) Fork(msgID string) (string, error) {
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

	branchID := "branch-" + uuid.New().String()[:8]
	s.Branches = append(s.Branches, branchID)
	s.activeBranch = branchID
	s.branchHeads[branchID] = msgID
	return branchID, nil
}

// Save writes all messages to the session file using an atomic temp-file +
// rename so a crash mid-write cannot leave a truncated/corrupt JSONL.
func (s *Session) Save() error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
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
// it. Used when compaction commits: the runtime context (summary + recent tail)
// becomes the new on-disk state and the older raw messages are discarded.
func (s *Session) Replace(msgs []models.AgentMessage) error {
	s.Messages = append([]models.AgentMessage(nil), msgs...)
	s.initBranchState()
	return s.Save()
}

// ActiveMessages returns the messages on the current branch, reconstructed by
// walking the parent_id tree from the active branch head. For linear history
// (no parent ids), all messages are returned in their original order.
func (s *Session) ActiveMessages() []models.AgentMessage {
	head, ok := s.branchHeads[s.activeBranch]
	if !ok || head == "" {
		return append([]models.AgentMessage(nil), s.Messages...)
	}

	byID := make(map[string]models.AgentMessage, len(s.Messages))
	for _, m := range s.Messages {
		byID[m.ID] = m
	}

	// Compatibility: if the main branch head has no parent and there are earlier
	// messages, the session was written before branching and should be treated as
	// a single linear conversation.
	if s.activeBranch == mainBranch {
		if headMsg, ok := byID[head]; ok && (headMsg.ParentID == nil || *headMsg.ParentID == "") && len(s.Messages) > 1 {
			return append([]models.AgentMessage(nil), s.Messages...)
		}
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

// initBranchState rebuilds the active branch and branch head map from the
// loaded messages. It is called after Create, Load, and Replace.
func (s *Session) initBranchState() {
	s.activeBranch = mainBranch
	s.branchHeads = make(map[string]string)
	for _, m := range s.Messages {
		branchID := mainBranch
		if m.Metadata != nil {
			if bid, ok := m.Metadata["branch_id"].(string); ok && bid != "" {
				branchID = bid
			}
		}
		s.branchHeads[branchID] = m.ID
	}
}
