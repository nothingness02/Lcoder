package tui

import (
	"context"
	"time"

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
	eventCh            chan events.Event
	runner             *runnerQueue
	runnerCancel       context.CancelFunc

	state uiState

	// Conversation history, rebuilt into the viewport each frame.
	blocks     []block
	components []components.BlockComponent
	viewport   viewport.Model

	// Streaming state for the in-flight assistant message.
	streaming          bool
	streamLive         string
	streamLiveThinking string
	streamMsgID        string
	turnTools          []toolResultEntry

	// sched coalesces stream-driven viewport rebuilds; rebuilds counts actual
	// rebuildViewport executions (acceptance probe for the frame scheduler).
	sched    frameScheduler
	rebuilds int

	input   InputModel
	spinner spinner
	paste   *pasteStash
	history *inputHistory

	// turnStartFrame is the spinner frame at which the current turn began, so the
	// processing status line can show the elapsed seconds.
	turnStartFrame int

	// Slash menu (inline dropdown over the composer within stateInput).
	menuVisible  bool
	menuSelected int

	// File mention menu (@-triggered file picker within stateInput).
	fileMenuVisible  bool
	fileMenuSelected int
	fileMenuItems    []string
	// fileMenuIndexing shows an "indexing…" placeholder while the suggester
	// warms up (first @ on a cold index).
	fileMenuIndexing bool
	// fileSuggester backs @-completion; prewarmed at startup so the first @
	// usually hits a warm cache. Stopped by Close.
	fileSuggester fileSuggester

	// Command output panel (ephemeral, above the composer within stateInput).
	cmdPanel cmdPanel

	// Overlays (reused from existing files).
	picker   SessionPickerModel
	extPanel ExtensionsPanelModel

	toolsExpanded     bool
	focusedBlockIndex int // -1 means no block is focused

	// Task sidebar: tasks declared via the todo_write tool, the user's manual
	// hide override, and the cached main-content width set by updateSizes.
	tasks             []task.Task
	taskSidebarHidden bool
	mainWidth         int

	header      headerInfo
	headerFrame int

	// topBar toggles the persistent identity strip above the transcript.
	topBar bool

	model      string
	themeStyle string
	totalCost  float64
	errMsg     string

	// contextPct caches context budget usage (0-100) for the status line,
	// with the raw token counts behind it (used/limit).
	// Stats() walks blocks and estimates tokens, so it must not run per-frame;
	// refreshed at turn/compaction boundaries via updateContextStats. -1 when
	// unknown (no budget limit reported yet).
	contextPct       int
	contextUsedTok   int
	contextLimitTok  int

	// compacting is set between CompactionStarted and the next terminal event
	// (commit/error/message/agent-end); the status line shows an indicator.
	compacting bool

	// capabilities of the active model, shown in /status (from the catalog).
	capabilities []string

	skills      *skills.Catalog
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

	// skillsBlockUpdater refreshes the agent's skills context block after a
	// runtime skill toggle (wired from app.go, which owns the agent).
	skillsBlockUpdater func(content string)

	// onSessionChange is notified whenever the active session is swapped, so the
	// compaction sink records folds to the session actually in use rather than the
	// one that happened to be open at startup.
	onSessionChange func(*session.Session)

	// inputHook intercepts plain user input before skill parsing/submission.
	// Returns (newText, proceed, reason). Nil means no interception.
	inputHook func(text string) (string, bool, string)
}

// NewModel keeps the exact signature the call sites and tests rely on.
func NewModel(bus *events.Bus, ag AgentRunner, session SessionWriter, store SessionStore, cwd, sessionID, model, themeStyle string, httpTools []HTTPToolItem, mcpRegistry *mcp.Registry, modeManager *agent.ModeManager, llmClient *llm.Client, cfg config.Config, checkpointStore checkpoint.Store, needsProviderSetup bool, skillCatalog *skills.Catalog) *Model {
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
		skills:             skillCatalog,
		modeManager:        modeManager,
		llmClient:          llmClient,
		cfg:                cfg,
		needsProviderSetup: needsProviderSetup,
		mcpRegistry:        mcpRegistry,
		header:             headerInfo{model: model, cwd: cwd, version: "0.1"},
		topBar:             true,
		focusedBlockIndex:  -1,
		contextPct:         -1,
		contextUsedTok:     0,
		contextLimitTok:    0,
		sched:              frameScheduler{minInterval: termProfile(), now: time.Now},
	}
	// Restore the display from the agent's already-loaded context window so a
	// session reloaded at startup shows its prior conversation (and task
	// sidebar), matching what /sessions does via loadSession.
	if msgs := ag.AllMessages(); len(msgs) > 0 {
		m.blocks = blocksFromMessages(msgs)
		m.components = componentsFromBlocks(m.blocks)
	}
	m.fileSuggester = newFileSuggester(cwd)
	m.fileSuggester.EnsureStarted() // prewarm: first @ usually hits a warm index
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

// SetOnSessionChange registers a callback fired when the active session changes.
// It is invoked immediately with the current session so the caller starts in sync.
// SetSkillsBlockUpdater wires the skills-block refresh used by the skills
// management panel.
func (m *Model) SetSkillsBlockUpdater(fn func(content string)) {
	m.skillsBlockUpdater = fn
}

func (m *Model) SetOnSessionChange(fn func(*session.Session)) {
	m.onSessionChange = fn
	if fn != nil {
		if sess, ok := m.session.(*session.Session); ok {
			fn(sess)
		}
	}
}

// SetInputHook installs the extension input hook.
func (m *Model) SetInputHook(hook func(text string) (string, bool, string)) {
	m.inputHook = hook
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
	switch ev.(type) {
	// Compactions are persisted by the context manager's CompactionSink
	// (agentsetup.SessionCompactionSink), inside the same call that folds the
	// context — not from here, where a missed event would silently leave the
	// session claiming the folded messages are still active.
	case events.TurnEndEvent, events.AgentEndEvent:
		// Mirror the completed turn's assistant/tool messages into the session
		// now. This handler runs synchronously inside the agent's TurnEnd
		// emission, which precedes the automatic checkpoint written at the
		// turn boundary — so the session on disk is always at least as new as
		// any checkpoint, and a crash cannot resurrect a checkpoint whose
		// messages were never saved.
		_ = sess.AppendMissing(m.agent.AllMessages())
	}
	return nil
}

// Close cleans up the event subscription and runner queue.
func (m *Model) Close() {
	if m.runnerCancel != nil {
		m.runnerCancel()
	}
	if m.fileSuggester != nil {
		m.fileSuggester.Stop()
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

// commitStartupHeader renders the final startup banner and commits it as the
// first conversation block, then transitions to the input state. Called when
// the startup animation finishes or the user presses a key to skip it.
func (m *Model) commitStartupHeader() {
	banner := renderHeader(m.header, headerTotalFrames-1, m.width)
	m.appendBlock(block{kind: components.BlockBanner, raw: banner})
	m.state = stateInput
	m.updateSizes()
}

// addUser appends a full-width user bar, tagging any resolvable @file mentions
// as attachments shown beneath the bar.
func (m *Model) addUser(text string) {
	m.appendBlock(block{kind: components.BlockUser, raw: text, attachments: mentionLabels(m.cwd, text)})
}

// updateSizes recomputes layout after a resize, reserving the task sidebar's
// fixed column when it is visible and the top bar's row when it is shown.
func (m *Model) updateSizes() {
	mw := m.mainContentWidth()
	m.mainWidth = mw
	m.input.SetWidth(mw - 2)
	m.input.SyncHeight()
	bottom := m.bottomHeight()
	vh := m.height - bottom - m.topBarHeight()
	if vh < 3 {
		vh = 3
	}
	m.viewport.Width = mw
	m.viewport.Height = vh
	m.rebuildViewport()
}
