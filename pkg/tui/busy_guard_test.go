package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/host"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/testutil"
	"github.com/lcoder/lcoder/pkg/tui/components"
)

// lastSystemBlock returns the raw text of the most recent system block, or ""
// when none exists.
func lastSystemBlock(m *Model) string {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind == components.BlockSystem {
			return stripANSI(m.blocks[i].raw)
		}
	}
	return ""
}

// --- C1: run events must not clobber overlay state ---

func TestAgentStartDoesNotClobberOverlay(t *testing.T) {
	for _, st := range []uiState{stateSessionPicker, stateExtensions, stateProvider, stateConfirm, stateStartup} {
		m, _, _ := newTestModel()
		m.state = st
		m.handleEvent(events.AgentStartEvent{})
		if m.state != st {
			t.Fatalf("AgentStart must not change overlay state %v, got %v", st, m.state)
		}
	}
}

func TestAgentStartTransitionsFromInputAndProcessing(t *testing.T) {
	for _, st := range []uiState{stateInput, stateProcessing} {
		m, _, _ := newTestModel()
		m.state = st
		m.handleEvent(events.AgentStartEvent{})
		if m.state != stateProcessing {
			t.Fatalf("AgentStart from state %v must enter processing, got %v", st, m.state)
		}
	}
}

func TestAgentEndDoesNotClobberOverlay(t *testing.T) {
	for _, st := range []uiState{stateInput, stateSessionPicker, stateExtensions, stateProvider, stateConfirm} {
		m, _, _ := newTestModel()
		m.state = st
		m.handleEvent(events.AgentEndEvent{})
		if m.state != st {
			t.Fatalf("AgentEnd must not change state %v, got %v", st, m.state)
		}
	}
}

func TestAgentEndReturnsFromProcessingToInput(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateProcessing
	m.handleEvent(events.AgentEndEvent{})
	if m.state != stateInput {
		t.Fatalf("AgentEnd from processing must return to input, got %v", m.state)
	}
}

// --- C2: a goal stopping without terminal run events resets processing ---

func TestGoalPausedWhileProcessingResetsToInput(t *testing.T) {
	for _, status := range []string{"paused", "blocked"} {
		m, _, _ := newTestModel()
		m.state = stateProcessing
		m.input.SetProcessing(true)
		m.handleEvent(events.GoalUpdatedEvent{
			Objective: "fix the test",
			Status:    status,
			Reason:    "turn budget exhausted",
		})
		if m.state != stateInput {
			t.Fatalf("goal %s while processing must reset to input, got %v", status, m.state)
		}
		if got := lastSystemBlock(m); !strings.Contains(got, "turn budget exhausted") {
			t.Fatalf("reset must surface the reason, last system block = %q", got)
		}
	}
}

func TestGoalClearedWhileProcessingResetsToInput(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateProcessing
	m.input.SetProcessing(true)
	m.handleEvent(events.GoalUpdatedEvent{Status: ""})
	if m.state != stateInput {
		t.Fatalf("cleared goal while processing must reset to input, got %v", m.state)
	}
	if m.goal != nil {
		t.Fatalf("cleared goal must nil the local copy, got %+v", m.goal)
	}
}

func TestGoalActiveWhileProcessingKeepsState(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateProcessing
	m.handleEvent(events.GoalUpdatedEvent{Objective: "fix", Status: "active"})
	if m.state != stateProcessing {
		t.Fatalf("active goal update must not leave processing, got %v", m.state)
	}
}

func TestGoalPausedWhileNotProcessingDoesNotAddWarning(t *testing.T) {
	m, _, _ := newTestModel()
	m.state = stateInput
	before := len(m.blocks)
	m.handleEvent(events.GoalUpdatedEvent{Objective: "fix", Status: "paused", Reason: "user"})
	if len(m.blocks) != before {
		t.Fatalf("goal pause outside processing must not add a system block, %d -> %d", before, len(m.blocks))
	}
}

// --- C3: processing status line height changes resync the bottom layout ---

func TestAgentStartEventResyncsBottomLayout(t *testing.T) {
	m, _, _ := newTestModel()
	m.updateSizes()
	m.state = stateInput
	idleRows := m.bottomRows

	updated, _ := m.Update(EventMsg{Event: events.AgentStartEvent{}})
	m2 := updated.(*Model)
	if m2.state != stateProcessing {
		t.Fatalf("expected processing, got %v", m2.state)
	}
	want := lipgloss.Height(m2.bottomRegion())
	if m2.bottomRows != want {
		t.Fatalf("bottomRows = %d, want rendered height %d", m2.bottomRows, want)
	}
	if m2.bottomRows <= idleRows {
		t.Fatalf("processing status line must grow the bottom region: %d -> %d", idleRows, m2.bottomRows)
	}
	if m2.viewport.Height != m2.height-m2.bottomRows-m2.topBarHeight() {
		t.Fatalf("viewport height not resynced: %d", m2.viewport.Height)
	}

	updated, _ = m2.Update(AgentDoneMsg{})
	m3 := updated.(*Model)
	want = lipgloss.Height(m3.bottomRegion())
	if m3.bottomRows != want {
		t.Fatalf("after AgentDone bottomRows = %d, want rendered height %d", m3.bottomRows, want)
	}
}

// --- C4: busy/closed errors surface as friendly warnings ---

func TestRetryBusyShowsWarning(t *testing.T) {
	ag := &testutil.FakeAgent{
		Messages: []models.AgentMessage{models.UserMessage("do it")},
		BusyErr:  host.ErrAgentBusy,
	}
	m := newTestCoreModel(ag)
	defer m.Close()
	m.state = stateInput

	if cmd := m.retryLast(); cmd != nil {
		t.Fatal("busy retry must not submit a follow-up command")
	}
	if got := lastSystemBlock(m); !strings.Contains(got, "agent is running") {
		t.Fatalf("expected busy warning, last system block = %q", got)
	}
	if m.state != stateInput {
		t.Fatalf("busy retry must not enter processing, got %v", m.state)
	}
	if len(ag.TruncateAfterCalls) != 0 {
		t.Fatalf("busy TruncateAfter must be refused without recording, got %v", ag.TruncateAfterCalls)
	}
}

func TestNewSessionBusyShowsWarning(t *testing.T) {
	ag := &testutil.FakeAgent{SessionIDVal: "s1", BusyErr: host.ErrAgentBusy}
	m := newTestCoreModel(ag)
	defer m.Close()
	m.state = stateInput

	m.dispatchSlash("/new")
	if got := lastSystemBlock(m); !strings.Contains(got, "agent is running") {
		t.Fatalf("expected busy warning, last system block = %q", got)
	}
	if ag.NewSessionCount != 0 {
		t.Fatalf("busy NewSession must be refused, count = %d", ag.NewSessionCount)
	}
	if ag.SessionID() != "s1" {
		t.Fatalf("session must stay %q, got %q", "s1", ag.SessionID())
	}
}

func TestOpenSessionBusyShowsWarning(t *testing.T) {
	ag := &testutil.FakeAgent{
		SessionIDVal: "s1",
		SessionMsgs:  map[string][]models.AgentMessage{"s2": {models.UserMessage("q2")}},
		BusyErr:      host.ErrAgentBusy,
	}
	m := newTestCoreModel(ag)
	defer m.Close()

	m.openSessionByID("s2")
	if got := lastSystemBlock(m); !strings.Contains(got, "agent is running") {
		t.Fatalf("expected busy warning, last system block = %q", got)
	}
	if ag.SessionID() != "s1" {
		t.Fatalf("busy OpenSession must not switch sessions, got %q", ag.SessionID())
	}
}

func TestSwitchModeBusyShowsWarning(t *testing.T) {
	ag := &testutil.FakeAgent{ModeName: "code", BusyErr: host.ErrAgentBusy}
	m := newTestCoreModel(ag)
	defer m.Close()

	m.switchMode("plan")
	if got := lastSystemBlock(m); !strings.Contains(got, "agent is running") {
		t.Fatalf("expected busy warning, last system block = %q", got)
	}
	if ag.Mode() != "code" {
		t.Fatalf("busy SetMode must leave the mode untouched, got %q", ag.Mode())
	}
}

func TestRestoreCheckpointBusyShowsWarning(t *testing.T) {
	ag := &testutil.FakeAgent{SessionIDVal: "s1", BusyErr: host.ErrAgentBusy}
	m := newTestCoreModel(ag)
	defer m.Close()

	m.restoreCheckpoint()
	if got := lastSystemBlock(m); !strings.Contains(got, "agent is running") {
		t.Fatalf("expected busy warning, last system block = %q", got)
	}
	if ag.RestoredCheckpoint != "" {
		t.Fatalf("busy RestoreCheckpoint must be refused, got %q", ag.RestoredCheckpoint)
	}
}

func TestCoreClosedShowsWarning(t *testing.T) {
	ag := &testutil.FakeAgent{SessionIDVal: "s1", BusyErr: host.ErrCoreClosed}
	m := newTestCoreModel(ag)
	defer m.Close()

	m.openSessionByID("s2")
	if got := lastSystemBlock(m); !strings.Contains(got, "core is closed") {
		t.Fatalf("expected closed-core warning, last system block = %q", got)
	}
}

func TestNonBusyErrorKeepsOriginalPresentation(t *testing.T) {
	ag := &testutil.FakeAgent{
		SessionIDVal:   "s1",
		OpenSessionErr: errors.New("disk corrupted"),
	}
	m := newTestCoreModel(ag)
	defer m.Close()

	m.openSessionByID("s2")
	if got := lastSystemBlock(m); !strings.Contains(got, "open session: disk corrupted") {
		t.Fatalf("non-busy error must keep the original message, got %q", got)
	}
}

// --- C5: small-fix regressions ---

func TestReloadFromCoreReseedsGoal(t *testing.T) {
	ag := &testutil.FakeAgent{SessionIDVal: "s1"}
	m := newTestCoreModel(ag)
	defer m.Close()
	m.width = 80
	m.height = 24

	ag.StartGoal("pursue this", 0, 0)
	m.reloadFromCore()
	if m.goal == nil || m.goal.Objective != "pursue this" {
		t.Fatalf("reloadFromCore must re-seed the goal copy, got %+v", m.goal)
	}

	ag.CancelGoal()
	m.reloadFromCore()
	if m.goal != nil {
		t.Fatalf("reloadFromCore must drop a cleared goal, got %+v", m.goal)
	}
}

func TestGoalStartClearsStaleUIState(t *testing.T) {
	m := newTestCoreModel(&fakeAgent{})
	defer m.Close()
	m.state = stateInput
	m.errMsg = "previous failure"
	m.notice = "copied"
	m.focusedBlockIndex = 2

	m.dispatchSlash("/goal fix the failing test")
	if m.errMsg != "" || m.notice != "" || m.focusedBlockIndex != -1 {
		t.Fatalf("goal start must clear errMsg/notice/focus, got %q %q %d", m.errMsg, m.notice, m.focusedBlockIndex)
	}
}
