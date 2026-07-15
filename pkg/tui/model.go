package tui

import (
	"context"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/checkpoint"
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm"
	"github.com/lcoder/lcoder/pkg/mcp"
	"github.com/lcoder/lcoder/pkg/session"
	"github.com/lcoder/lcoder/pkg/skills"
	"github.com/lcoder/lcoder/pkg/task"
	"github.com/lcoder/lcoder/pkg/tui/components"
)

// uiState is the explicit state-machine enum for the top-level model.
type uiState int

const (
	stateStartup uiState = iota
	stateInput
	stateProcessing
	stateConfirm
	stateSessionPicker
	stateExtensions
	stateProvider
)

// Model is the single top-level bubbletea model for the Lcoder TUI.
type Model struct {
	width, height int
	cwd           string

	agent           AgentRunner
	session         SessionWriter
	store           SessionStore
	checkpointStore checkpoint.Store
	bus             *events.Bus

	unsubscribe        func()
	persistUnsubscribe func()
	eventCh       chan events.Event
	runner        *runnerQueue
	runnerCancel  context.CancelFunc

	state uiState

	// Conversation history, rebuilt into the viewport each frame.
	blocks     []block
	components []components.BlockComponent
	viewport   viewport.Model

	// Streaming state for the in-flight assistant message.
	streaming   bool
	streamLive  string
	streamMsgID string
	turnTools   []toolResultEntry

	input   InputModel
	spinner spinner
	paste   *pasteStash
	history *inputHistory

	// Slash menu (inline dropdown over the composer within stateInput).
	menuVisible  bool
	menuSelected int

	// File mention menu (@-triggered file picker within stateInput).
	fileMenuVisible  bool
	fileMenuSelected int
	fileMenuItems    []string

	// Command output panel (ephemeral, above the composer within stateInput).
	cmdPanel cmdPanel

	// Overlays (reused from existing files).
	picker   SessionPickerModel
	extPanel ExtensionsPanelModel

	toolsExpanded bool

	// Task sidebar: tasks declared via the todo_write tool, the user's manual
	// hide override, and the cached main-content width set by updateSizes.
	tasks             []task.Task
	taskSidebarHidden bool
	mainWidth         int

	header      headerInfo
	headerFrame int

	model      string
	themeStyle string
	totalCost  float64
	errMsg     string

	// compacting is set between CompactionStarted and the next terminal event
	// (commit/error/message/agent-end); the status line shows an indicator.
	compacting bool

	// capabilities of the active model, shown in /status (from the catalog).
	capabilities []string

	skills      []skills.SkillMeta
	modeManager *agent.ModeManager

	// Provider-config wizard dependencies and state.
	llmClient          *llm.Client
	cfg                config.Config
	provPanel          providerPanel
	needsProviderSetup bool

	// MCP registry for the /mcp management panel.
	mcpRegistry *mcp.Registry

	// Confirmation panel for interactive permission approvals.
	confirm confirmPanel

	// suggestion (ghost text) state.
	completedTurns int
	suggestion     string
}

// NewModel keeps the exact signature the call sites and tests rely on.
func NewModel(bus *events.Bus, ag AgentRunner, session SessionWriter, store SessionStore, cwd, sessionID, model, themeStyle string, httpTools []HTTPToolItem, mcpRegistry *mcp.Registry, modeManager *agent.ModeManager, llmClient *llm.Client, cfg config.Config, checkpointStore checkpoint.Store, needsProviderSetup bool, loadedSkillCatalog ...skills.SkillMeta) *Model {
	// Theme override: honor explicit "light"/"dark", else auto-detect.
	switch themeStyle {
	case "light":
		darkBgOnce.Do(func() { darkBg = false })
	case "dark":
		darkBgOnce.Do(func() { darkBg = true })
	}
	warmBackgroundColor()

	vp := viewport.New(80, 15)
	m := &Model{
		agent:              ag,
		session:            session,
		store:              store,
		checkpointStore:    checkpointStore,
		cwd:                cwd,
		bus:                bus,
		eventCh:            make(chan events.Event, 64),
		state:              stateStartup,
		viewport:           vp,
		input:              NewInputModel(),
		spinner:            newSpinner(),
		paste:              newPasteStash(),
		history:            newInputHistory(),
		extPanel:           ExtensionsPanelModel{HTTPTools: httpTools, MCPServers: mcpServers(mcpRegistry)},
		model:              model,
		themeStyle:         themeStyle,
		skills:             loadedSkillCatalog,
		modeManager:        modeManager,
		llmClient:          llmClient,
		cfg:                cfg,
		needsProviderSetup: needsProviderSetup,
		mcpRegistry:        mcpRegistry,
		header:             headerInfo{model: model, cwd: cwd, version: "0.1"},
	}
	// Restore the display from the agent's already-loaded context window so a
	// session reloaded at startup shows its prior conversation (and task
	// sidebar), matching what /sessions does via loadSession.
	if msgs := ag.AllMessages(); len(msgs) > 0 {
		m.blocks = blocksFromMessages(msgs)
		m.components = componentsFromBlocks(m.blocks)
	}
	if ag.TaskManager() != nil {
		m.tasks = ag.TaskManager().List()
	}
	m.unsubscribe = bus.Subscribe(m.onEvent)
	m.persistUnsubscribe = bus.Subscribe(m.persistFromEvent)
	runnerCtx, cancel := context.WithCancel(context.Background())
	m.runnerCancel = cancel
	m.runner = newRunnerQueue(ag, session)
	m.runner.Start(runnerCtx)
	if needsProviderSetup {
		m.openProviderPanel()
	}
	return m
}

// SetCapabilities records the active model's catalog capabilities for /status.
func (m *Model) SetCapabilities(caps []string) {
	m.capabilities = caps
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		waitForEventCmd(m.eventCh),
		waitForRunnerResultCmd(m.runner.Results()),
		headerTick(),
	)
}

// onEvent is the events.Bus callback; forwards events to the channel the UI drains.
func (m *Model) onEvent(ctx context.Context, ev events.Event) error {
	select {
	case m.eventCh <- ev:
	case <-ctx.Done():
	}
	return nil
}

// persistFromEvent mirrors the agent's context window into the active session
// after each turn and records compaction entries. It lives on the model so that
// /sessions and /new switches use the current session, not the startup session.
func (m *Model) persistFromEvent(ctx context.Context, ev events.Event) error {
	sess, ok := m.session.(*session.Session)
	if !ok {
		return nil
	}
	switch e := ev.(type) {
	case events.CompactionCommittedEvent:
		// Append-only: record the compaction entry; raw messages stay on disk.
		// Degraded folds (breaker open) carry no summary and persist nothing.
		if !e.Degraded && e.Summary != "" {
			_ = sess.AppendCompactionEntry(e.Summary, e.FirstKeptID, e.TokensBefore)
			// Mirror the kept tail now: with the entry on disk, AppendMissing
			// skips the runtime summary and appends only the not-yet-persisted
			// kept messages, so a crash before run end cannot lose them.
			_ = sess.AppendMissing(m.agent.AllMessages())
		}
	case events.MessageEndEvent, events.ToolExecutionEndEvent, events.AgentEndEvent:
		_ = sess.Save()
	}
	return nil
}

// Close cleans up the event subscription and runner queue.
func (m *Model) Close() {
	if m.runnerCancel != nil {
		m.runnerCancel()
	}
	if m.persistUnsubscribe != nil {
		m.persistUnsubscribe()
	}
	if m.unsubscribe != nil {
		m.unsubscribe()
	}
}

// appendBlock adds a block and marks the viewport dirty.
func (m *Model) appendBlock(b block) {
	m.blocks = append(m.blocks, b)
	m.components = append(m.components, toComponent(b))
	m.rebuildViewport()
}

// addSystem appends a dim system line.
func (m *Model) addSystem(text string) {
	m.appendBlock(block{kind: components.BlockSystem, raw: text})
}

// addUser appends a full-width user bar, tagging any resolvable @file mentions
// as attachments shown beneath the bar.
func (m *Model) addUser(text string) {
	m.appendBlock(block{kind: components.BlockUser, raw: text, attachments: mentionLabels(m.cwd, text)})
}

// updateSizes recomputes layout after a resize, reserving the task sidebar's
// fixed column when it is visible.
func (m *Model) updateSizes() {
	mw := m.mainContentWidth()
	m.mainWidth = mw
	m.input.SetWidth(mw - 2)
	m.input.SyncHeight()
	bottom := m.bottomHeight()
	vh := m.height - bottom
	if vh < 3 {
		vh = 3
	}
	m.viewport.Width = mw
	m.viewport.Height = vh
	m.rebuildViewport()
}
