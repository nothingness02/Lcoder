// Package testutil provides reusable test fixtures for Lcoder packages.
package testutil

import (
	"context"
	"fmt"
	"sync"

	"github.com/lcoder/lcoder/pkg/agentapi"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/task"
)

// FakeAgent is a minimal implementation of agentapi.CoreAPI for TUI tests.
// All fields are exported so tests can program or inspect behavior. Every
// method takes the mutex, so tests can safely read or reprogram fields while
// the TUI runner delivers prompts from a goroutine (program fields before
// starting the model, or hold no expectations about intermediate states).
type FakeAgent struct {
	mu             sync.Mutex
	Prompts        []models.AgentMessage
	Messages       []models.AgentMessage
	ModeName       string
	TasksVal       []task.Task
	SwitchedModel  models.ModelRef
	SwitchedBudget contextmgr.TokenBudget
	SessionIDVal   string
	// ThinkingVal is returned by Thinking ("" default); SwitchThinking
	// records its argument for test inspection.
	ThinkingVal      string
	SwitchedThinking string
	// ContextStatsVal is returned by ContextStats so tests can program the
	// context-budget figures the TUI status line consumes.
	ContextStatsVal agentapi.ContextStats
	// MicroCompactStatusVal is returned by MicroCompactStatus ("" = disabled).
	MicroCompactStatusVal string
	// GoalVal is the fake goal record manipulated by the goal methods.
	GoalVal *agentapi.GoalState
	// EndReason is returned by LastEndReason (zero value = "").
	EndReason events.AgentEndReason

	// SkillsBlockVal records the last SetSkillsBlock content.
	SkillsBlockVal string
	// SetModeErr, when non-nil, is returned by SetMode; ModeName is left
	// unchanged, mirroring the host's refuse-then-don't-touch semantics.
	SetModeErr error
	// BusyErr, when non-nil, is returned by run submissions (Prompt,
	// Continue) and state-changing operations (SetMode, OpenSession,
	// NewSession, TruncateAfter, RestoreCheckpoint) with no side effects,
	// mirroring the host's in-flight refusal (host.ErrAgentBusy). Tests set
	// it to host.ErrAgentBusy to exercise the TUI's busy-error paths.
	BusyErr error

	// SessionsList is returned by ListSessions (the session picker).
	SessionsList []agentapi.SessionInfo
	// SessionMsgs programs OpenSession: the messages swapped in per id.
	SessionMsgs map[string][]models.AgentMessage
	// OpenSessionErr, when non-nil, is returned by OpenSession.
	OpenSessionErr error
	// NewSessionCount counts NewSession calls.
	NewSessionCount int
	// TruncateAfterCalls records every TruncateAfter message id.
	TruncateAfterCalls []string
	// RenamedSessions records RenameSession calls (id → title).
	RenamedSessions map[string]string

	// CheckpointIDs backs ListCheckpoints.
	CheckpointIDs []string
	// SavedCheckpointCount counts SaveCheckpoint calls.
	SavedCheckpointCount int
	// SaveCheckpointErr, when non-nil, is returned by SaveCheckpoint.
	SaveCheckpointErr error
	// RestoredCheckpoint records the id passed to RestoreCheckpoint;
	// RestoreMsgs, when non-nil, replaces Messages on restore.
	RestoredCheckpoint string
	RestoreMsgs        []models.AgentMessage
	RestoreErr         error

	// UsageSummaryVal is returned by UsageSummary; UsageLedgerVal by
	// UsageLedger. SessionUsage/SessionLedger, when non-nil, program the
	// per-session values OpenSession swaps in (mirroring the host, where the
	// ledger is a property of the session, not the core).
	UsageSummaryVal agentapi.UsageSummary
	UsageLedgerVal  map[string]models.LLMUsage
	SessionUsage    map[string]agentapi.UsageSummary
	SessionLedger   map[string]map[string]models.LLMUsage
}

var _ agentapi.CoreAPI = (*FakeAgent)(nil)

func (f *FakeAgent) Prompt(_ context.Context, msg models.AgentMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.BusyErr != nil {
		return f.BusyErr
	}
	f.Prompts = append(f.Prompts, msg)
	return nil
}

func (f *FakeAgent) Continue(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.BusyErr != nil {
		return f.BusyErr
	}
	return nil
}
func (f *FakeAgent) AllMessages() []models.AgentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Messages
}
func (f *FakeAgent) PromptsLen() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Prompts)
}
func (f *FakeAgent) Mode() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ModeName == "" {
		return "code"
	}
	return f.ModeName
}

// SetMode records the requested mode (the real host swaps the underlying
// runner; the fake just flips the label) — only on success, mirroring the
// host's semantics where a refused switch leaves the active mode untouched.
func (f *FakeAgent) SetMode(mode string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.BusyErr != nil {
		return f.BusyErr
	}
	if f.SetModeErr != nil {
		return f.SetModeErr
	}
	f.ModeName = mode
	return nil
}

func (f *FakeAgent) SessionID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.SessionIDVal
}
func (f *FakeAgent) SetUserConfirm(agentapi.UserConfirmation) {}
func (f *FakeAgent) Steer(models.AgentMessage)                {}
func (f *FakeAgent) Abort()                                   {}
func (f *FakeAgent) ClearSkillFilter()                        {}
func (f *FakeAgent) SwitchModel(ref models.ModelRef, budget contextmgr.TokenBudget) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.SwitchedModel = ref
	f.SwitchedBudget = budget
}

// ContextStats returns the programmed context accounting.
func (f *FakeAgent) ContextStats() agentapi.ContextStats {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ContextStatsVal
}

// SetSkillsBlock records the skills block content.
func (f *FakeAgent) SetSkillsBlock(content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.SkillsBlockVal = content
}

// Tasks returns the programmed task snapshot.
func (f *FakeAgent) Tasks() []task.Task {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.TasksVal
}

// OpenSession swaps in the programmed messages for id, mirroring the host's
// atomic session switch (messages + session id + tasks rebuilt from history).
func (f *FakeAgent) OpenSession(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.BusyErr != nil {
		return f.BusyErr
	}
	if f.OpenSessionErr != nil {
		return f.OpenSessionErr
	}
	msgs, ok := f.SessionMsgs[id]
	if !ok {
		return fmt.Errorf("fake: unknown session %q", id)
	}
	f.Messages = msgs
	f.SessionIDVal = id
	f.TasksVal = tasksFromMessages(msgs)
	if f.SessionUsage != nil {
		f.UsageSummaryVal = f.SessionUsage[id]
	}
	if f.SessionLedger != nil {
		f.UsageLedgerVal = f.SessionLedger[id]
	}
	return nil
}

// NewSession starts a fresh empty session.
func (f *FakeAgent) NewSession() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.BusyErr != nil {
		return f.BusyErr
	}
	f.NewSessionCount++
	f.Messages = nil
	f.TasksVal = nil
	f.UsageSummaryVal = agentapi.UsageSummary{}
	f.UsageLedgerVal = nil
	f.SessionIDVal = fmt.Sprintf("fake-new-%d", f.NewSessionCount)
	return nil
}

// TruncateAfter records the call and prunes Messages to just after the given
// message id (an empty id clears the conversation), mirroring the host's
// fork-based truncation from the TUI's perspective.
func (f *FakeAgent) TruncateAfter(messageID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.BusyErr != nil {
		return f.BusyErr
	}
	f.TruncateAfterCalls = append(f.TruncateAfterCalls, messageID)
	if messageID == "" {
		f.Messages = nil
		return nil
	}
	for i, msg := range f.Messages {
		if msg.ID == messageID {
			f.Messages = f.Messages[:i+1]
			return nil
		}
	}
	return fmt.Errorf("fake: message %q not found", messageID)
}

// ListSessions returns the programmed session metadata.
func (f *FakeAgent) ListSessions() ([]agentapi.SessionInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.SessionsList, nil
}

// RenameSession records the rename and updates the matching SessionsList entry.
func (f *FakeAgent) RenameSession(sessionID, title string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.RenamedSessions == nil {
		f.RenamedSessions = make(map[string]string)
	}
	f.RenamedSessions[sessionID] = title
	for i, s := range f.SessionsList {
		if s.ID == sessionID {
			f.SessionsList[i].Title = title
		}
	}
	return nil
}

// Goal returns the fake's goal record (nil unless StartGoal was called).
func (f *FakeAgent) Goal() *agentapi.GoalState {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.GoalVal == nil {
		return nil
	}
	cp := *f.GoalVal
	return &cp
}

// StartGoal records an active goal so TUI tests can exercise /goal flows.
func (f *FakeAgent) StartGoal(objective string, turnBudget, tokenBudget int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.GoalVal = &agentapi.GoalState{Objective: objective, Status: agentapi.GoalActive, TurnBudget: turnBudget, TokenBudget: tokenBudget}
}

// PauseGoal marks the fake goal paused.
func (f *FakeAgent) PauseGoal(reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.GoalVal != nil && f.GoalVal.Status == agentapi.GoalActive {
		f.GoalVal.Status = agentapi.GoalPaused
		f.GoalVal.BlockReason = reason
	}
}

// ResumeGoal reactivates the fake goal.
func (f *FakeAgent) ResumeGoal() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.GoalVal != nil && (f.GoalVal.Status == agentapi.GoalPaused || f.GoalVal.Status == agentapi.GoalBlocked) {
		f.GoalVal.Status = agentapi.GoalActive
		f.GoalVal.BlockReason = ""
	}
}

// CancelGoal clears the fake goal.
func (f *FakeAgent) CancelGoal() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.GoalVal = nil
}

// LastEndReason returns the fake's programmed end reason.
func (f *FakeAgent) LastEndReason() events.AgentEndReason {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.EndReason
}

// SwitchThinking records the requested thinking value for test inspection.
func (f *FakeAgent) SwitchThinking(thinking string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.SwitchedThinking = thinking
}

// Thinking returns the programmed thinking value ("" default).
func (f *FakeAgent) Thinking() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ThinkingVal
}

// MicroCompactStatus returns the programmed trimming status ("" default).
func (f *FakeAgent) MicroCompactStatus() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.MicroCompactStatusVal
}

// SaveCheckpoint records the call and returns the current session id as the
// checkpoint id (matching the production store keying).
func (f *FakeAgent) SaveCheckpoint() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.SavedCheckpointCount++
	if f.SaveCheckpointErr != nil {
		return "", f.SaveCheckpointErr
	}
	return f.SessionIDVal, nil
}

// RestoreCheckpoint records the id and swaps in RestoreMsgs when programmed.
func (f *FakeAgent) RestoreCheckpoint(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.BusyErr != nil {
		return f.BusyErr
	}
	f.RestoredCheckpoint = id
	if f.RestoreErr != nil {
		return f.RestoreErr
	}
	if f.RestoreMsgs != nil {
		f.Messages = f.RestoreMsgs
	}
	return nil
}

// ListCheckpoints returns the programmed checkpoint ids.
func (f *FakeAgent) ListCheckpoints() ([]agentapi.CheckpointInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	infos := make([]agentapi.CheckpointInfo, 0, len(f.CheckpointIDs))
	for _, id := range f.CheckpointIDs {
		infos = append(infos, agentapi.CheckpointInfo{ID: id})
	}
	return infos, nil
}

// UsageSummary returns the programmed usage aggregate.
func (f *FakeAgent) UsageSummary() agentapi.UsageSummary {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.UsageSummaryVal
}

// UsageLedger returns the programmed per-message usage map.
func (f *FakeAgent) UsageLedger() map[string]models.LLMUsage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.UsageLedgerVal
}

// tasksFromMessages rebuilds the task list from history by finding the most
// recent todo_write tool call. Returns nil when none is present. (Same logic
// as the host's helper.)
func tasksFromMessages(msgs []models.AgentMessage) []task.Task {
	for i := len(msgs) - 1; i >= 0; i-- {
		for _, tc := range msgs[i].ToolCalls() {
			if tc.Name == task.ToolName {
				if tasks, err := task.Parse(tc.Arguments["todos"]); err == nil {
					return tasks
				}
			}
		}
	}
	return nil
}
