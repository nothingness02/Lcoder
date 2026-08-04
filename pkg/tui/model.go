package tui

import (
	"context"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/lcoder/lcoder/pkg/agentapi"
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/host"
	"github.com/lcoder/lcoder/pkg/llm"
	"github.com/lcoder/lcoder/pkg/mcp"
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

// DisplayConfig carries the display-only parameters of a TUI launch (no agent
// state): what to show in the header/status and whether to open the
// first-launch provider wizard.
type DisplayConfig struct {
	CWD                string
	ModelRef           string
	ThemeStyle         string
	Capabilities       []string
	NeedsProviderSetup bool
}

// Model is the single top-level bubbletea model for the Lcoder TUI.
type Model struct {
	width, height int
	cwd           string

	// agent is the stable agentapi.CoreAPI handle (a *host.Core in
	// production): mode switches swap the runner inside the host, and session
	// persistence happens there too — the TUI holds no session or checkpoint
	// store.
	agent AgentCore
	bus   *events.Bus

	unsubscribe  func()
	eventCh      chan events.Event
	runner       *runnerQueue
	runnerCancel context.CancelFunc

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
	// transcriptLines is the full rendered height of the conversation, tracked
	// for the scrollbar indicator.
	sched           frameScheduler
	rebuilds        int
	transcriptLines int

	input   InputModel
	spinner spinner
	paste   *pasteStash
	history *inputHistory

	// lastKeyAt and burstChars back paste-burst detection: Windows coninput
	// delivers pasted text as a stream of per-key events with no
	// bracketed-paste marker, so an Enter inside a fast key burst is a literal
	// newline, not a submit (see paste.go burstEnter/noteKey).
	lastKeyAt  time.Time
	burstChars int

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

	// mentionChips lists the resolved @file mention basenames for the live
	// chips row under the composer (nil when empty). Unresolvable mentions
	// are silently omitted; submit-time validateMentions is the only negative
	// feedback path.
	mentionChips []string

	// bottomRows caches the reserved bottom-region height so per-keystroke
	// growth (composer, menus, chips) resizes the viewport only on change.
	bottomRows int

	// Command output panel (ephemeral, above the composer within stateInput).
	cmdPanel cmdPanel

	// effortSel is the horizontal thinking-effort picker opened by /thinking.
	effortSel *effortSelector

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
	// ContextStats() walks blocks and estimates tokens, so it must not run
	// per-frame; refreshed at turn/compaction boundaries via
	// updateContextStats. -1 when unknown (no budget limit reported yet).
	contextPct      int
	contextUsedTok  int
	contextLimitTok int

	// compacting is set between CompactionStarted and the next terminal event
	// (commit/error/message/agent-end); the status line shows an indicator.
	compacting bool

	// capabilities of the active model, shown in /status (from the catalog).
	capabilities []string

	skills *skills.Catalog
	// modes is the startup snapshot of the mode catalog for the /modes panel.
	modes []agentapi.ModeInfo

	// goal is the local copy of the goal record: seeded from CoreAPI.Goal() at
	// startup and kept current by GoalUpdatedEvent (no polling).
	goal *agentapi.GoalState

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

	// copyFn writes text to the system clipboard (OSC 52 on Unix, Win32 API on
	// Windows); injectable for tests. notice is a transient dim line above the
	// composer (copy feedback), cleared on the next keystroke or submit.
	copyFn func(string) error
	notice string

	// inputHook intercepts plain user input before skill parsing/submission.
	// Returns (newText, proceed, reason). Nil means no interception.
	inputHook func(text string) (string, bool, string)
}

// NewModel builds the TUI model around the protocol handle and the local
// workbench services (see host.Services).
func NewModel(core AgentCore, services host.Services, display DisplayConfig) *Model {
	// Theme override: honor explicit "light"/"dark", else auto-detect.
	switch display.ThemeStyle {
	case "light":
		darkBgOnce.Do(func() { darkBg = false })
	case "dark":
		darkBgOnce.Do(func() { darkBg = true })
	}
	warmBackgroundColor()

	vp := viewport.New(80, 15)
	m := &Model{
		agent:              core,
		cwd:                display.CWD,
		bus:                services.Bus,
		eventCh:            make(chan events.Event, 64),
		state:              stateStartup,
		viewport:           vp,
		input:              NewInputModel(),
		spinner:            newSpinner(),
		paste:              newPasteStash(),
		history:            newInputHistory(),
		extPanel:           ExtensionsPanelModel{MCPServers: mcpServers(services.MCPRegistry)},
		model:              display.ModelRef,
		themeStyle:         display.ThemeStyle,
		capabilities:       display.Capabilities,
		skills:             services.SkillCatalog,
		modes:              services.Modes,
		llmClient:          services.LLMClient,
		cfg:                services.Config,
		needsProviderSetup: display.NeedsProviderSetup,
		mcpRegistry:        services.MCPRegistry,
		header:             headerInfo{model: display.ModelRef, cwd: display.CWD, version: "0.1"},
		topBar:             true,
		focusedBlockIndex:  -1,
		copyFn:             copyTextToClipboard,
		contextPct:         -1,
		contextUsedTok:     0,
		contextLimitTok:    0,
		sched:              frameScheduler{minInterval: termProfile(), now: time.Now},
	}
	// Restore the display from the agent's already-loaded context window so a
	// session reloaded at startup shows its prior conversation (and task
	// sidebar), matching what /sessions does via openSessionByID.
	if msgs := core.AllMessages(); len(msgs) > 0 {
		m.blocks = blocksFromMessages(msgs)
		m.components = componentsFromBlocks(m.blocks)
	}
	m.fileSuggester = newFileSuggester(display.CWD)
	m.fileSuggester.EnsureStarted() // prewarm: first @ usually hits a warm index
	// Subscribe before seeding from the core so no snapshot event (goal, task
	// list) can slip into the window between the seed reads and the
	// subscription; snapshot events are idempotent, so a replay is harmless.
	m.unsubscribe = services.Bus.Subscribe(m.onEvent)
	m.tasks = core.Tasks()
	m.goal = core.Goal()
	runnerCtx, cancel := context.WithCancel(context.Background())
	m.runnerCancel = cancel
	m.runner = newRunnerQueue(core)
	m.runner.Start(runnerCtx)
	if display.NeedsProviderSetup {
		m.openProviderPanel()
	}
	return m
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

// Close cleans up the event subscription and runner queue.
func (m *Model) Close() {
	if m.runnerCancel != nil {
		m.runnerCancel()
	}
	if m.fileSuggester != nil {
		m.fileSuggester.Stop()
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
	m.bottomRows = bottom
	vh := m.height - bottom - m.topBarHeight()
	if vh < 3 {
		vh = 3
	}
	m.viewport.Width = max(mw-1, 1) // rightmost column is the scrollbar indicator
	m.viewport.Height = vh
	m.rebuildViewport()
}
