package agenthost

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/lcoder/lcoder/pkg/session"
	"github.com/lcoder/lcoder/pkg/subagent"
)

// journalMetaType marks a session as a subagent journal and carries the
// ownership metadata resume validation needs (kimi-code's ownership check).
const journalMetaType = "subagent/meta"

// journalMeta is the custom entry written into every subagent journal.
type journalMeta struct {
	ParentSessionID string `json:"parent_session_id"`
	Profile         string `json:"profile"`
	Task            string `json:"task,omitempty"`
}

// journalStore persists subagent journals as regular sessions and tracks
// which agents are currently running (resume requires an idle journal).
type journalStore struct {
	mu      sync.Mutex
	running map[string]bool
}

func newJournalStore() *journalStore {
	return &journalStore{running: make(map[string]bool)}
}

// markRunning reports whether the agent could be marked running (false when
// it already is).
func (j *journalStore) markRunning(agentID string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.running[agentID] {
		return false
	}
	j.running[agentID] = true
	return true
}

func (j *journalStore) markIdle(agentID string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	delete(j.running, agentID)
}

// writeMeta records the journal metadata as a custom entry.
func writeMeta(sess *session.Session, meta journalMeta) error {
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return sess.AppendCustomEntry(journalMetaType, data)
}

// readMeta extracts the latest journal metadata entry (a resume appends a
// new one every run), or nil when the session is not a subagent journal.
func readMeta(sess *session.Session) *journalMeta {
	entries := sess.CustomEntries(journalMetaType)
	for i := len(entries) - 1; i >= 0; i-- {
		var m journalMeta
		if err := json.Unmarshal(entries[i].Data, &m); err == nil {
			return &m
		}
	}
	return nil
}

// validateResume loads the journal and checks the resume preconditions:
// the journal exists, is a subagent journal, belongs to this parent session
// (when the host knows its parent), and its profile is still available.
func (h *Host) validateResume(agentID string) (*session.Session, *journalMeta, subagent.Agent, error) {
	var zero subagent.Agent
	if h.cfg.SessionStore == nil {
		return nil, nil, zero, fmt.Errorf("resume is unavailable: no session store configured")
	}
	sess, err := h.cfg.SessionStore.LoadByID(h.cfg.CWD, agentID)
	if err != nil {
		return nil, nil, zero, fmt.Errorf("unknown subagent %q: no journal found", agentID)
	}
	meta := readMeta(sess)
	if meta == nil {
		return nil, nil, zero, fmt.Errorf("session %q is not a subagent journal", agentID)
	}
	if h.parentSessionID != "" && meta.ParentSessionID != "" && meta.ParentSessionID != h.parentSessionID {
		return nil, nil, zero, fmt.Errorf("subagent %q belongs to a different parent session", agentID)
	}
	profile, ok := h.cfg.Profiles[meta.Profile]
	if !ok {
		return nil, nil, zero, fmt.Errorf("journal %q references unknown profile %q", agentID, meta.Profile)
	}
	return sess, meta, profile, nil
}
