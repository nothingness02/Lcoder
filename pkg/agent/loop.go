package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/lcoder/lcoder/pkg/checkpoint"
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm"
	"github.com/lcoder/lcoder/pkg/memory"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/observability"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/task"
	"github.com/lcoder/lcoder/pkg/tools"
)

// DefaultCoreTools is the always-loaded core set under deferred tool loading.
// Everything else is reachable via tool_search.
var DefaultCoreTools = []string{"read", "bash", "edit", "ls", "grep"}

// ReminderProducer returns zero or more ephemeral system-reminder strings for the
// upcoming turn, given the current conversation. Producers run at each turn start;
// their output is injected for that turn only and cleared at the turn boundary.
type ReminderProducer func(messages []models.AgentMessage) []string

// UserConfirmation handles interactive permission approvals for tool calls.
type UserConfirmation interface {
	Confirm(ctx context.Context, info ToolCallInfo) (allow bool, err error)
	ConfirmWithScope(ctx context.Context, info ToolCallInfo) (ConfirmResult, error)
}

// ConfirmScope describes how widely a user-approved permission should apply.
type ConfirmScope int

const (
	ScopeDeny ConfirmScope = iota
	ScopeOnce
	ScopeProject
	ScopeGlobal
)

// ConfirmResult is the outcome of a scoped confirmation.
type ConfirmResult struct {
	Allow bool
	Scope ConfirmScope
}

// Config controls agent behavior.
type Config struct {
	SystemPrompt      string
	BaseSystemPrompt  string
	Model             models.ModelRef
	ToolExecutionMode models.ExecutionMode
	ContextManager    *contextmgr.Manager
	TransformContext  TransformContext
	BeforeToolCall    BeforeToolCallHook
	AfterToolCall     AfterToolCallHook
	ShouldStop        ShouldStopFunc
	Mode              string
	ModeManager       *ModeManager

	// UserConfirm handles interactive permission approvals. When the permission
	// engine returns Ask, the agent calls Confirm and blocks the tool call until
	// the user responds. CLI and TUI provide their own implementations.
	UserConfirm UserConfirmation

	// SessionID identifies the session this agent belongs to. It is used as the
	// default checkpoint key when auto-saving state.
	SessionID string

	// CheckpointStore persists automatic checkpoints between turns. If nil,
	// no automatic checkpoints are written.
	CheckpointStore checkpoint.Store

	// CheckpointInterval controls how often automatic checkpoints are written.
	// 0 means every turn (backward-compatible); values > 0 save every N turns.
	CheckpointInterval int

	// DeferredTools, when true, ships only CoreTools with full schemas plus
	// the tool_search meta-tool; every other registered tool is sent as a
	// name-only stub. tool_search is resolved locally (see executor.go),
	// never executed by the registry.
	DeferredTools bool

	// CoreTools is the set of tool names that keep their full schema under
	// deferred loading. Empty falls back to DefaultCoreTools.
	CoreTools []string

	// ReminderProducers return ephemeral system-reminder strings for the upcoming
	// turn. They run at each turn start; their output is injected for that turn
	// only and discarded at the turn boundary.
	ReminderProducers []ReminderProducer

	// MemoryInjector prefetches relevant memory entries each turn.
	MemoryInjector memory.MemoryInjector
}

// eventEmitter wraps the event bus and observability collector so subsystems
// can emit events without holding a reference to the whole Agent.
type eventEmitter struct {
	bus *events.Bus
	obs *observability.Collector
}

func (e *eventEmitter) emit(ctx context.Context, ev events.Event) {
	if e == nil || e.bus == nil {
		return
	}
	if err := e.bus.Emit(ctx, ev); err != nil {
		if e.obs != nil {
			_ = e.obs.RecordRuntimeError(err.Error())
		} else {
			fmt.Fprintf(os.Stderr, "event emit error: %v\n", err)
		}
	}
}

// emit routes events through the agent's emitter, lazily creating it if the
// agent was constructed directly rather than via New/NewWithObservability.
func (a *Agent) emit(ctx context.Context, ev events.Event) {
	if a.emitter == nil {
		a.emitter = &eventEmitter{bus: a.bus, obs: a.obsCollector}
	}
	a.emitter.emit(ctx, ev)
}

// Compile-time assertions that Agent exposes the UI-facing Runner surface and
// can be used as a ModeSwitcher.
var (
	_ Runner       = (*Agent)(nil)
	_ ModeSwitcher = (*Agent)(nil)
)

// Agent runs the LLM tool loop. It delegates streaming, tool execution, and
// state management to internal components while remaining the public API surface.
type Agent struct {
	cfg          Config
	mgr          *contextmgr.Manager
	llm          *llm.Client
	registry     *tools.Registry
	bus          *events.Bus
	obsCollector *observability.Collector
	emitter      *eventEmitter

	loopState *stateHolder
	streamer  *streamer
	executor  *executor
	taskMgr   *task.Manager
	cpMgr     *checkpointManager
	rc        *reminderCoordinator

	contextSnapshotRecorder *observability.ContextSnapshotRecorder
	memoryInjector          memory.MemoryInjector
}

// State describes the agent runtime state.
type State int

const (
	StateIdle State = iota
	StateStreaming
	StateExecutingTools
)

// TransformContext transforms messages before sending to the LLM.
// It can be used for compaction, pruning, or injecting context.
type TransformContext func(ctx context.Context, messages []models.AgentMessage) ([]models.AgentMessage, error)

// BeforeToolCallHook runs after argument validation and permission approval and
// may block execution.
type BeforeToolCallHook func(ctx context.Context, info ToolCallInfo) (*BeforeToolCallResult, error)

// ToolCallInfo is provided to hooks.
type ToolCallInfo struct {
	AssistantMessage models.AgentMessage
	ToolCall         models.ToolCallContent
	Args             map[string]any
	Context          []models.AgentMessage
}

// BashCommand returns the bash command from the tool call, or an empty string
// if the tool is not bash or has no command argument.
func (info ToolCallInfo) BashCommand() string {
	if info.ToolCall.Name != "bash" {
		return ""
	}
	if cmd, _ := info.Args["command"].(string); cmd != "" {
		return cmd
	}
	if cmd, _ := info.ToolCall.Arguments["command"].(string); cmd != "" {
		return cmd
	}
	return ""
}

// BeforeToolCallResult indicates whether a tool call should be blocked.
type BeforeToolCallResult struct {
	Block  bool
	Reason string
}

// AfterToolCallHook runs after a tool finishes and may modify its result.
type AfterToolCallHook func(ctx context.Context, info ToolCallResultInfo) (*AfterToolCallResult, error)

// ToolCallResultInfo is provided to the after hook.
type ToolCallResultInfo struct {
	AssistantMessage models.AgentMessage
	ToolCall         models.ToolCallContent
	Args             map[string]any
	Result           models.ToolExecutionResult
	IsError          bool
	Context          []models.AgentMessage
}

// AfterToolCallResult allows hooks to override result fields.
type AfterToolCallResult struct {
	Content   []models.ContentPart
	Details   map[string]any
	IsError   *bool
	Terminate bool
}

// ShouldStopFunc decides whether the loop should stop after a turn.
type ShouldStopFunc func(ctx context.Context, turn TurnSummary) (bool, error)

// TurnSummary provides context for a stop decision.
type TurnSummary struct {
	Message     models.AgentMessage
	ToolResults []models.AgentMessage
	Context     []models.AgentMessage
}

// New creates an agent.
func New(cfg Config, llmClient *llm.Client, registry *tools.Registry, perms *permissions.Engine, bus *events.Bus) *Agent {
	mgr := cfg.ContextManager
	if mgr == nil {
		mgr = contextmgr.NewManager(contextmgr.TokenBudget{
			MaxTotal:      128000,
			TargetTotal:   120000,
			ReserveOutput: 8192,
			MaxOutput:     16384,
		})
		mgr.SetBlock(contextmgr.NewBlock(contextmgr.BlockSystem, "system", contextmgr.StabilityStatic, 100,
			models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: cfg.SystemPrompt})))
	}
	ag := &Agent{
		cfg:      cfg,
		mgr:      mgr,
		llm:      llmClient,
		registry: registry,
		bus:      bus,
	}
	ag.emitter = &eventEmitter{bus: bus}
	ag.loopState = newStateHolder()
	ag.taskMgr = task.NewManager()
	ag.streamer = &streamer{cfg: &ag.cfg, llm: ag.llm, mgr: ag.mgr, emitter: ag.emitter}
	ag.executor = &executor{cfg: &ag.cfg, mgr: ag.mgr, registry: ag.registry, permissions: perms, emitter: ag.emitter, taskMgr: ag.taskMgr}
	ag.cpMgr = newCheckpointManager(ag)
	ag.rc = newReminderCoordinator(ag.taskMgr, cfg.ReminderProducers)
	ag.memoryInjector = normalizeMemoryInjector(cfg.MemoryInjector)
	return ag
}

// NewWithObservability creates an agent with an observability collector.
func NewWithObservability(cfg Config, llmClient *llm.Client, registry *tools.Registry, perms *permissions.Engine, bus *events.Bus, obs *observability.Collector) *Agent {
	ag := New(cfg, llmClient, registry, perms, bus)
	ag.obsCollector = obs
	ag.emitter.obs = obs
	ag.streamer.obs = obs
	return ag
}

// Subscribe registers an event handler.
func (a *Agent) Subscribe(handler events.Handler) func() {
	return a.bus.Subscribe(handler)
}

// State returns the current agent state.
func (a *Agent) State() State {
	return a.loopState.State()
}

// Steer injects a user message during the next safe boundary.
func (a *Agent) Steer(msg models.AgentMessage) {
	a.loopState.Steer(msg)
}

// FollowUp queues a message after the agent would otherwise stop.
func (a *Agent) FollowUp(msg models.AgentMessage) {
	a.loopState.FollowUp(msg)
}

// Abort signals the current run to stop gracefully. Safe to call multiple times.
func (a *Agent) Abort() {
	a.loopState.Abort()
}

// SwitchModel changes the model used for subsequent turns and re-sizes the
// context budget in place. Conversation history is preserved. Intended to be
// called from the TUI while the agent is idle (the provider overlay is modal).
func (a *Agent) SwitchModel(ref models.ModelRef, budget contextmgr.TokenBudget) {
	a.cfg.Model = ref
	if a.mgr != nil {
		a.mgr.SetBudget(budget)
	}
}

// SetMessages rebuilds the conversation from a flat message list.
func (a *Agent) SetMessages(msgs []models.AgentMessage) {
	a.mgr.SetMessages(msgs)
}

// SessionID returns the session this agent is currently associated with.
func (a *Agent) SessionID() string {
	return a.cfg.SessionID
}

// SetSessionID changes the session identifier the agent uses for checkpoints
// and session-scoped callbacks. It is used by the TUI when the user switches
// to a different session or starts a new one.
func (a *Agent) SetSessionID(id string) {
	a.cfg.SessionID = id
}

// AllMessages returns the full conversation from the context manager.
func (a *Agent) AllMessages() []models.AgentMessage {
	return a.mgr.AllMessages()
}

// TaskManager returns the agent's task manager.
func (a *Agent) TaskManager() *task.Manager {
	return a.taskMgr
}

// SetUserConfirm injects the interactive confirmation handler.
func (a *Agent) SetUserConfirm(uc UserConfirmation) {
	a.cfg.UserConfirm = uc
}

// Prompt starts a new agent run with a user message.
func (a *Agent) Prompt(ctx context.Context, msg models.AgentMessage) error {
	return a.run(ctx, []models.AgentMessage{msg})
}

// Continue resumes from the current context without adding a new message.
func (a *Agent) Continue(ctx context.Context) error {
	return a.run(ctx, nil)
}

// Mode returns the active mode name.
func (a *Agent) Mode() string {
	return a.cfg.Mode
}

// WithMode returns a copy of the agent with a different mode set.
// It clones the current context manager and runtime state so that the new agent
// continues the same conversation under the new mode.
func (a *Agent) WithMode(mode string) Runner {
	cfg := a.cfg
	cfg.Mode = mode
	cfg.ContextManager = a.mgr.Clone()

	emitter := a.emitter
	if emitter == nil {
		emitter = &eventEmitter{bus: a.bus, obs: a.obsCollector}
	}

	memoryInjector := a.memoryInjector
	if inj, ok := memoryInjector.(*memory.Injector); ok {
		memoryInjector = normalizeMemoryInjector(inj.WithManager(cfg.ContextManager))
	}
	cfg.MemoryInjector = memoryInjector

	fresh := &Agent{
		cfg:                     cfg,
		mgr:                     cfg.ContextManager,
		llm:                     a.llm,
		registry:                a.registry,
		bus:                     a.bus,
		obsCollector:            a.obsCollector,
		emitter:                 emitter,
		loopState:               newStateHolder(),
		streamer:                &streamer{cfg: &cfg, llm: a.llm, mgr: cfg.ContextManager, obs: a.obsCollector, emitter: emitter},
		executor:                newExecutor(&cfg, cfg.ContextManager, a.registry, a.executor.permissions, emitter, a.taskMgr),
		taskMgr:                 a.taskMgr,
		contextSnapshotRecorder: a.contextSnapshotRecorder,
		memoryInjector:          memoryInjector,
	}
	fresh.cpMgr = newCheckpointManager(fresh)
	fresh.rc = newReminderCoordinator(fresh.taskMgr, fresh.cfg.ReminderProducers)
	fresh.loopState.restore(a.loopState.snapshot())
	fresh.loopState.SetResuming(true)
	fresh.executor.restore(a.executor.snapshot())
	return fresh
}

func (a *Agent) run(ctx context.Context, initialPrompts []models.AgentMessage) error {
	// Derive a cancelable context for this run so Abort() can stop not just the
	// LLM stream but also in-flight tool calls, compaction, and checkpoint I/O.
	ctx, cancel := context.WithCancel(ctx)
	a.loopState.SetRunCancel(cancel)
	defer func() {
		cancel()
		a.loopState.SetRunCancel(nil)
	}()

	a.loopState.SetState(StateStreaming)
	a.loopState.ResetAbort()

	turn := a.loopState.StartRun()
	for _, msg := range initialPrompts {
		a.appendMessage(msg)
	}

	a.emit(ctx, events.AgentStartEvent{Base: events.Base{Type: events.AgentStart, Turn: turn}})

	endReason := events.EndReasonCompleted
	for {
		pending := a.loopState.DrainSteeringQueue()
		if len(pending) > 0 {
			for _, msg := range pending {
				a.appendMessage(msg)
			}
		}

		a.emit(ctx, events.TurnStartEvent{Base: events.Base{Type: events.TurnStart, Turn: turn}})

		a.refreshEphemeralReminders()

		if a.memoryInjector != nil {
			if userText := lastUserText(a.mgr.AllMessages()); userText != "" {
				if err := a.memoryInjector.Prefetch(ctx, userText); err != nil {
					if ctx.Err() == nil {
						a.emit(ctx, events.ErrorEvent{
							Base:    events.Base{Type: events.Error, Turn: turn},
							Message: "memory prefetch: " + err.Error(),
						})
					}
				}
			}
		}

		a.maybeCompact(ctx, turn)

		_, tools, modelRef, execMode := a.applyMode()

		assistantMsg, err := a.streamer.stream(
			ctx,
			turn,
			modelRef,
			tools,
			a.loopState.SetStreamAbort,
			a.loopState.ClearStreamAbort,
		)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				endReason = events.EndReasonInterrupted
			} else {
				endReason = events.EndReasonError
			}
			a.emit(ctx, events.ErrorEvent{Base: events.Base{Type: events.Error, Turn: turn}, Message: err.Error()})
			break
		}
		a.appendMessage(assistantMsg)

		toolCalls := assistantMsg.ToolCalls()
		var toolResults []models.AgentMessage
		terminate := false
		if len(toolCalls) > 0 {
			a.loopState.SetState(StateExecutingTools)
			toolResults, terminate = a.executor.execute(ctx, turn, assistantMsg, toolCalls, execMode)
			for _, r := range toolResults {
				a.appendMessage(r)
			}
			a.loopState.SetState(StateStreaming)
		}

		a.emit(ctx, events.TurnEndEvent{
			Base:        events.Base{Type: events.TurnEnd, Turn: turn},
			Message:     assistantMsg,
			ToolResults: toolResults,
		})

		if sink, ok := a.memoryInjector.(memory.MemorySink); ok && sink != nil {
			userText := lastUserText(a.mgr.AllMessages())
			assistantText := assistantMsg.Text()
			if err := sink.SyncTurn(ctx, userText, assistantText); err != nil {
				if ctx.Err() == nil {
					a.emit(ctx, events.ErrorEvent{
						Base:    events.Base{Type: events.Error, Turn: turn},
						Message: "memory sync_turn: " + err.Error(),
					})
				}
			}
		}

		turn++
		a.loopState.SetTurn(turn)

		// Persist an automatic checkpoint at the completed-turn boundary.
		// This is the only place where the run is in a known-safe state.
		a.cpMgr.MaybeCheckpoint(ctx, turn, checkpoint.ReasonAuto, a.emit)

		if terminate {
			endReason = events.EndReasonTerminated
			break
		}

		if a.shouldStop(ctx, assistantMsg, toolResults) {
			followUps := a.loopState.DrainFollowUpQueue()
			if len(followUps) == 0 {
				break
			}
			for _, msg := range followUps {
				a.appendMessage(msg)
			}
		}
	}

	if a.contextSnapshotRecorder != nil {
		if state, err := a.mgr.Snapshot(); err == nil {
			_ = a.contextSnapshotRecorder.Record(state, "end", turn)
		}
	}

	if sink, ok := a.memoryInjector.(memory.MemorySink); ok && sink != nil {
		if err := sink.OnSessionEnd(ctx, memory.SessionSummary{SessionID: a.cfg.SessionID, TurnCount: turn}); err != nil {
			if ctx.Err() == nil {
				a.emit(ctx, events.ErrorEvent{
					Base:    events.Base{Type: events.Error, Turn: turn},
					Message: "memory session_end: " + err.Error(),
				})
			}
		}
	}

	a.emit(ctx, events.AgentEndEvent{
		Base:     events.Base{Type: events.AgentEnd, Turn: turn},
		Reason:   endReason,
		Messages: a.mgr.AllMessages(),
	})
	a.loopState.SetState(StateIdle)
	return nil
}

// refreshEphemeralReminders stages reminders for the upcoming turn.
// It always includes any unfinished task reminder, then runs any configured
// ReminderProducers over the current conversation.
func (a *Agent) refreshEphemeralReminders() {
	a.mgr.ClearEphemeralReminders()
	reminders := a.rc.Reminders(a.mgr.AllMessages())
	if len(reminders) > 0 {
		a.mgr.SetEphemeralReminders(reminders)
	}
}

func (a *Agent) appendMessage(msg models.AgentMessage) {
	a.mgr.AppendRecent(msg)
}

// maybeCompact asks the context manager to commit a compaction at a turn
// boundary. The call is synchronous: when a compaction will run, a
// CompactionStarted event is emitted first so UIs can show an indicator for
// the (blocking) duration. On commit it emits CompactionCommitted (with the
// summary payload) so the persistence layer can append a CompactionEntry. A
// summarizer error is non-fatal; a canceled context (abort) is silent.
func (a *Agent) maybeCompact(ctx context.Context, turn int) {
	if level := a.mgr.PendingCompaction(); level != contextmgr.CompactionNone {
		a.emit(ctx, events.CompactionStartedEvent{Base: events.Base{Type: events.CompactionStarted, Turn: turn}})
	}
	level, res, err := a.mgr.MaybeCompactLeveled(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		a.emit(ctx, events.ErrorEvent{
			Base:    events.Base{Type: events.Error, Turn: turn},
			Message: "compaction: " + err.Error(),
		})
		return
	}
	if res.Committed && a.contextSnapshotRecorder != nil {
		if state, err := a.mgr.Snapshot(); err == nil {
			_ = a.contextSnapshotRecorder.Record(state, "compaction", turn)
		}
	}
	if res.Degraded {
		a.emit(ctx, events.ErrorEvent{
			Base:    events.Base{Type: events.Error, Turn: turn},
			Message: "compaction degraded: summarizer circuit open; older messages truncated without summary",
		})
	}
	if res.Committed {
		a.emit(ctx, events.CompactionCommittedEvent{
			Base:         events.Base{Type: events.CompactionCommitted, Turn: turn},
			Summary:      res.Summary,
			FirstKeptID:  res.FirstKeptID,
			TokensBefore: res.TokensBefore,
			Degraded:     res.Degraded,
		})
		if level == contextmgr.CompactionReactive {
			if total := a.mgr.Stats()["total"]; total > a.mgr.Budget().DropLimit() {
				a.emit(ctx, events.ErrorEvent{
					Base:    events.Base{Type: events.Error, Turn: turn},
					Message: "context still over drop limit after compaction; truncation backstop active",
				})
			}
		}
	}
}

// Stats returns context manager statistics if available.
func (a *Agent) Stats() map[string]int {
	return a.mgr.Stats()
}

// LatestAssistantMessage returns the most recent assistant message in context.
func (a *Agent) LatestAssistantMessage() (models.AgentMessage, bool) {
	msgs := a.mgr.AllMessages()
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == models.RoleAssistant {
			return msgs[i], true
		}
	}
	return models.AgentMessage{}, false
}

func (a *Agent) applyMode() (string, []models.ToolDefinition, models.ModelRef, models.ExecutionMode) {
	var systemParts []string
	if a.mgr != nil {
		if b, ok := a.mgr.GetBlock(contextmgr.BlockSystem, "system"); ok {
			systemParts = []string{b.Text()}
		}
	}

	tools := a.executor.baseToolDefinitions()
	modelRef := a.cfg.Model
	execMode := a.cfg.ToolExecutionMode

	if a.cfg.ModeManager == nil {
		return strings.Join(systemParts, "\n\n"), tools, modelRef, execMode
	}

	mode := a.cfg.ModeManager.Get(a.cfg.Mode)
	modeText := ""
	if mode.SystemPrompt != "" {
		modeText = "# Mode: " + mode.Name + "\n\n" + mode.SystemPrompt
	}

	switch a.mgr.ModePromptPriority() {
	case config.ModePromptPrepend:
		if modeText != "" {
			if len(systemParts) > 0 && systemParts[0] != "" {
				a.mgr.SetBlock(contextmgr.NewBlock(contextmgr.BlockSystem, "system", contextmgr.StabilityStatic, 100,
					models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: modeText + "\n\n" + strings.Join(systemParts, "\n\n")})))
			} else {
				a.mgr.SetBlock(contextmgr.NewBlock(contextmgr.BlockSystem, "system", contextmgr.StabilityStatic, 100,
					models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: modeText})))
			}
			a.mgr.RemoveBlock(contextmgr.BlockMode, "mode")
			if b, ok := a.mgr.GetBlock(contextmgr.BlockSystem, "system"); ok {
				systemParts = []string{b.Text()}
			} else {
				systemParts = nil
			}
		}
	case config.ModePromptReplace:
		if modeText != "" {
			a.mgr.SetBlock(contextmgr.NewBlock(contextmgr.BlockSystem, "system", contextmgr.StabilityStatic, 100,
				models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: modeText})))
			a.mgr.RemoveBlock(contextmgr.BlockMode, "mode")
			systemParts = []string{modeText}
		}
	default: // Append (including zero value)
		if modeText != "" {
			modeBlock := contextmgr.NewBlock(contextmgr.BlockMode, "mode", contextmgr.StabilityStable, 90,
				models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: modeText}))
			a.mgr.SetBlock(modeBlock)
			systemParts = append(systemParts, modeText)
		}
	}
	if len(mode.AllowedTools) > 0 {
		allowed := make(map[string]bool)
		for _, p := range mode.AllowedTools {
			allowed[p] = true
		}
		var filtered []models.ToolDefinition
		for _, t := range tools {
			if matchToolName(t.Name, allowed) {
				filtered = append(filtered, t)
			}
		}
		tools = filtered
	}
	if len(mode.DeniedTools) > 0 {
		denied := make(map[string]bool)
		for _, p := range mode.DeniedTools {
			denied[p] = true
		}
		var filtered []models.ToolDefinition
		for _, t := range tools {
			if !matchToolName(t.Name, denied) {
				filtered = append(filtered, t)
			}
		}
		tools = filtered
	}
	if mode.Model != "" {
		modelRef.ID = mode.Model
	}
	if mode.Provider != "" {
		modelRef.Provider = mode.Provider
	}
	if mode.ExecutionMode == "sequential" {
		execMode = models.ExecutionSequential
	} else if mode.ExecutionMode == "parallel" {
		execMode = models.ExecutionParallel
	}
	return strings.Join(systemParts, "\n\n"), tools, modelRef, execMode
}

func matchToolName(name string, patterns map[string]bool) bool {
	if patterns[name] {
		return true
	}
	for p := range patterns {
		if strings.HasSuffix(p, "*") && strings.HasPrefix(name, p[:len(p)-1]) {
			return true
		}
		if strings.HasPrefix(p, "*") && strings.HasSuffix(name, p[1:]) {
			return true
		}
	}
	return false
}

func (a *Agent) shouldStop(ctx context.Context, msg models.AgentMessage, toolResults []models.AgentMessage) bool {
	if a.cfg.ShouldStop == nil {
		// Default "natural completion": keep looping while the model is still
		// calling tools; stop on the first turn that produces no tool calls
		// (its final natural-language answer). This lets the model observe tool
		// results and decide for itself when the task is done, rather than
		// halting after a single turn. terminate, checked earlier in run(),
		// remains the explicit hard backstop.
		return len(msg.ToolCalls()) == 0
	}
	stop, err := a.cfg.ShouldStop(ctx, TurnSummary{
		Message:     msg,
		ToolResults: toolResults,
		Context:     a.mgr.AllMessages(),
	})
	if err != nil {
		return false
	}
	return stop
}

func lastUserText(msgs []models.AgentMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == models.RoleUser {
			return msgs[i].Text()
		}
	}
	return ""
}

// normalizeMemoryInjector returns a nil interface value when inj holds a typed-nil
// concrete pointer. This prevents the agent from storing a non-nil interface that
// panics when methods are called.
func normalizeMemoryInjector(inj memory.MemoryInjector) memory.MemoryInjector {
	if inj == nil {
		return nil
	}
	v := reflect.ValueOf(inj)
	switch v.Kind() {
	case reflect.Ptr, reflect.Slice, reflect.Map, reflect.Chan, reflect.Func, reflect.Interface:
		if v.IsNil() {
			return nil
		}
	}
	return inj
}
