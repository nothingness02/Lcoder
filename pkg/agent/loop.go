package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/lcoder/lcoder/pkg/agentapi"
	"github.com/lcoder/lcoder/pkg/checkpoint"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/observability"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/skills"
	"github.com/lcoder/lcoder/pkg/task"
	"github.com/lcoder/lcoder/pkg/tools"
)

// DefaultCoreTools is the always-loaded core set under deferred tool loading.
// Everything else is reachable via tool_search. use_skill stays core so the
// model can always activate skills without a tool_search round-trip.
var DefaultCoreTools = []string{"read", "bash", "edit", "ls", "grep", skills.UseSkillToolName}

// ReminderProducer returns zero or more ephemeral system-reminder strings for the
// upcoming turn, given the current conversation. Producers run at each turn start;
// their output is injected for that turn only and cleared at the turn boundary.
type ReminderProducer func(messages []models.AgentMessage) []string

// Approval types live in the protocol package (pkg/agentapi); these aliases
// keep the agent package's internal code and all existing call sites
// unchanged.
type UserConfirmation = agentapi.UserConfirmation

type ToolCallInfo = agentapi.ToolCallInfo

// ConfirmScope describes how widely a user-approved permission should apply.
type ConfirmScope = agentapi.ConfirmScope

const (
	ScopeDeny    = agentapi.ScopeDeny
	ScopeOnce    = agentapi.ScopeOnce
	ScopeSession = agentapi.ScopeSession
	ScopeProject = agentapi.ScopeProject
	ScopeGlobal  = agentapi.ScopeGlobal
)

// ConfirmResult is the outcome of a scoped confirmation.
type ConfirmResult = agentapi.ConfirmResult

// Config controls agent behavior.
type Config struct {
	SystemPrompt     string
	BaseSystemPrompt string
	Model            models.ModelRef
	// MaxTurnsPerRun caps the number of provider turns in one Prompt run.
	// 0 means unlimited. Exceeding it ends the run IMMEDIATELY with
	// EndReasonMaxTurns — it does NOT pass the continuation chain (mirrors
	// Kimi's maxSteps throw: a hard, one-shot terminal condition).
	MaxTurnsPerRun   int
	ContextManager   *contextmgr.Manager
	TransformContext TransformContext
	BeforeToolCall   BeforeToolCallHook
	AfterToolCall    AfterToolCallHook
	ShouldStop       ShouldStopFunc
	// ContinuationDeciders decide whether the loop continues after a stop
	// signal. They run in registration order; the FIRST decider returning
	// (false, _) or (_, err) wins and the loop stops. All returning true
	// means continue; an empty chain stops (the pre-chain nil-hook behavior).
	// Built-in hard vetoes (goal budget, goal.go) run before this chain and
	// can only stop the loop, never continue it.
	ContinuationDeciders []ContinuationDecider
	// ExtraGuardPolicies are appended after the built-in mode/skill guard
	// policies (and still ahead of user rules). Extension permission hooks
	// plug in here (opencode's permission.ask equivalent).
	ExtraGuardPolicies []permissions.Policy
	Mode               string
	ModeManager        *ModeManager

	// CWD is the agent's working directory. It backs the executor's path
	// guard workspace boundary (executor.go) and project-scope rule
	// persistence; empty degrades to os.Getwd() for backward compatibility.
	CWD string

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
	goals     *goalHolder
	cpMgr     *checkpointManager
	injector  *InjectionManager

	contextSnapshotRecorder *observability.ContextSnapshotRecorder
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

// BeforeToolCallResult indicates whether a tool call should be blocked.
type BeforeToolCallResult struct {
	Block  bool
	Reason string
	// ModifiedArgs, when non-nil, replaces the parsed args used for execution.
	ModifiedArgs map[string]any
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
// Return true if the agent has completed its task and should stop.
type ShouldStopFunc func(ctx context.Context, turn TurnSummary) (bool, error)

// StopReason classifies why the loop is about to stop. Only StopEndTurn
// currently reaches the continuation chain; terminated / max_turns /
// interrupted / error are hard terminal conditions that bypass it.
type StopReason string

const (
	StopEndTurn     StopReason = "end_turn"    // 无 tool calls,自然完成
	StopTerminated  StopReason = "terminated"  // 工具 terminate 标记
	StopMaxTurns    StopReason = "max_turns"   // MaxTurnsPerRun 硬上限
	StopInterrupted StopReason = "interrupted" // abort / 用户中断
	StopError       StopReason = "error"       // stream 或执行错误
)

// StopContext is the continuation decision's input. It embeds TurnSummary and
// adds why the loop is stopping, how far it got, and an LLM handle so a
// decider can call the model (goal evaluation, summary generation).
type StopContext struct {
	TurnSummary
	Reason StopReason
	Turn   int
	Usage  models.LLMUsage
	LLM    *llm.Client
}

// ContinuationDecider decides whether the loop continues after a stop signal.
// Deciders run in registration order; the FIRST decider returning
// (false, _) or (_, err) wins and the loop stops. All returning true means
// continue. Registration order IS the priority: hard vetoes (goal budget)
// must run before soft continuations (steering drains). The chain is fixed
// at assembly time; goal's built-in veto prepends internally (see goal.go).
type ContinuationDecider func(ctx context.Context, stop StopContext) (bool, error)

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
	ag.goals = &goalHolder{}
	ag.goals.onChange = ag.emitGoalUpdated
	ag.streamer = &streamer{cfg: &ag.cfg, llm: ag.llm, mgr: ag.mgr, emitter: ag.emitter}
	ag.executor = &executor{cfg: &ag.cfg, mgr: ag.mgr, registry: ag.registry, permissions: perms, emitter: ag.emitter, taskMgr: ag.taskMgr, goals: ag.goals}
	ag.cpMgr = newCheckpointManager(ag)
	ag.injector = newInjectionManager(ag.taskMgr, &ag.cfg, cfg.ReminderProducers, ag.appendSwitchNotice)
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

// SwitchThinking replaces the resolved thinking value for subsequent turns.
// The value must already be validated by engine.ResolveThinking ("" = send
// nothing, "off", "on", or a model effort level). Runtime adjustment from
// the TUI; takes effect on the next BuildTurnRequest.
func (a *Agent) SwitchThinking(thinking string) {
	if a.mgr != nil {
		a.mgr.SetThinking(thinking)
	}
}

// Thinking returns the current resolved thinking value ("" = send nothing).
func (a *Agent) Thinking() string {
	if a.mgr != nil {
		return a.mgr.Thinking()
	}
	return ""
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

// ContextManager exposes the context manager for block-level updates (e.g.
// refreshing the skills block after a runtime skill toggle).
func (a *Agent) ContextManager() *contextmgr.Manager {
	return a.mgr
}

// ClearSkillFilter lifts any active skill tool restriction (used when the
// active skill is disabled at runtime).
func (a *Agent) ClearSkillFilter() {
	if a.executor != nil {
		a.executor.updateSkillFilter(map[string]any{skills.AllowedToolsDetailsKey: []string(nil)})
	}
}

// SetUserConfirm injects the interactive confirmation handler.
func (a *Agent) SetUserConfirm(uc UserConfirmation) {
	a.cfg.UserConfirm = uc
}

// SetBeforeToolCall replaces the before-tool-call hook. The executor holds a
// pointer to the agent config, so the change takes effect immediately.
// Intended for startup wiring while the agent is idle.
func (a *Agent) SetBeforeToolCall(hook BeforeToolCallHook) {
	a.cfg.BeforeToolCall = hook
}

// SetAfterToolCall replaces the after-tool-call hook. The executor holds a
// pointer to the agent config, so the change takes effect immediately.
// Intended for startup wiring while the agent is idle.
func (a *Agent) SetAfterToolCall(hook AfterToolCallHook) {
	a.cfg.AfterToolCall = hook
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
	if a.executor != nil {
		return a.executor.currentModeName()
	}
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
		goals:                   a.goals,
		contextSnapshotRecorder: a.contextSnapshotRecorder,
	}
	fresh.cpMgr = newCheckpointManager(fresh)
	fresh.executor.goals = a.goals // share the goal record across the mode switch
	// The goal holder is shared, so rewire its single emission hook to the
	// fresh agent: otherwise GoalUpdatedEvent keeps reading the old agent's
	// loopState (frozen Turn) and the closure pins the old agent in memory.
	// WithMode runs while idle, and the holder has exactly one owner after
	// the switch, so the rewire is safe.
	fresh.goals.onChange = fresh.emitGoalUpdated
	fresh.injector = newInjectionManager(fresh.taskMgr, &fresh.cfg, fresh.cfg.ReminderProducers, fresh.appendSwitchNotice)
	// Carry the injector bookkeeping across the switch. Left at zero, the mode
	// injector would see no previous mode and skip the notice that the old
	// mode's restrictions are lifted — the one thing a mode switch most needs to
	// say, and which the switch_mode tool path does emit.
	fresh.injector.Restore(a.injector.Snapshot())
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
	turnsThisRun := 0
	for _, msg := range initialPrompts {
		a.appendMessage(msg)
	}

	a.emit(ctx, events.AgentStartEvent{Base: events.Base{Type: events.AgentStart, Turn: turn}})

	endReason := events.EndReasonCompleted
	for {
		if a.cfg.MaxTurnsPerRun > 0 && turnsThisRun >= a.cfg.MaxTurnsPerRun {
			endReason = events.EndReasonMaxTurns
			break
		}
		turnsThisRun++

		pending := a.loopState.DrainSteeringQueue()
		if len(pending) > 0 {
			for _, msg := range pending {
				a.appendMessage(msg)
			}
		}

		a.emit(ctx, events.TurnStartEvent{Base: events.Base{Type: events.TurnStart, Turn: turn}})

		a.refreshEphemeralReminders(turn)

		a.maybeCompact(ctx, turn)

		_, tools := a.applyMode()
		modelRef := a.cfg.Model

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
			toolResults, terminate = a.executor.execute(ctx, turn, assistantMsg, toolCalls)
			for _, r := range toolResults {
				a.appendMessage(r)
			}
			a.loopState.SetState(StateStreaming)
		}

		usage, _ := a.streamer.takeUsage()
		a.emit(ctx, events.TurnEndEvent{
			Base:        events.Base{Type: events.TurnEnd, Turn: turn},
			Message:     assistantMsg,
			ToolResults: toolResults,
			Usage:       usage,
		})

		// Goal budget accounting: the run loop is the ONLY writer to the
		// ledger (turn boundary, after the turn's usage is known).
		if g := a.goals.get(); g != nil && g.Status == GoalActive {
			a.goals.mutate(func(live *GoalState) { live.RecordUsage(usage) })
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
			usage, _ := a.streamer.takeUsage()
			stop := StopContext{
				TurnSummary: TurnSummary{
					Message:     assistantMsg,
					ToolResults: toolResults,
					Context:     a.mgr.AllMessages(),
				},
				Reason: StopEndTurn,
				Turn:   turn,
				Usage:  usage,
				LLM:    a.llm,
			}
			// Built-in hard vetoes (goal budget, goal.go) run first: they can
			// only STOP the loop, never continue it.
			vetoed := false
			for _, veto := range a.builtinContinuationDeciders() {
				ok, err := veto(ctx, stop)
				if err != nil || !ok {
					vetoed = true
					break
				}
			}
			// Configured deciders decide continuation: first (false,_) or (_,err)
			// wins. An empty chain stops — the pre-chain nil-hook behavior.
			cont := false
			if !vetoed {
				cont = len(a.cfg.ContinuationDeciders) > 0
				for _, decide := range a.cfg.ContinuationDeciders {
					ok, err := decide(ctx, stop)
					if err != nil || !ok {
						cont = false
						break
					}
				}
			}
			if !cont {
				break
			}
		}
	}

	if a.contextSnapshotRecorder != nil {
		if state, err := a.mgr.Snapshot(); err == nil {
			_ = a.contextSnapshotRecorder.Record(state, "end", turn)
		}
	}

	a.loopState.SetEndReason(endReason)
	a.emit(ctx, events.AgentEndEvent{
		Base:     events.Base{Type: events.AgentEnd, Turn: turn},
		Reason:   endReason,
		Messages: a.mgr.AllMessages(),
	})
	a.loopState.SetState(StateIdle)
	return nil
}

// LastEndReason returns how the most recent run ended. It is synchronized by
// Prompt's return, so a driver calling Prompt serially can read it without
// subscribing to the event bus.
func (a *Agent) LastEndReason() events.AgentEndReason {
	return a.loopState.LastEndReason()
}

// AddContinuationDeciders appends deciders to the continuation chain after
// construction. Wiring-time only: it must be called before the first Prompt.
func (a *Agent) AddContinuationDeciders(fns ...ContinuationDecider) {
	a.cfg.ContinuationDeciders = append(a.cfg.ContinuationDeciders, fns...)
}

// AddGuardPolicies appends extra permission guard policies after
// construction. Wiring-time only: the executor re-reads them on every
// permission evaluation (installGuardPolicies), so they take effect from
// the next tool call.
func (a *Agent) AddGuardPolicies(policies ...permissions.Policy) {
	a.cfg.ExtraGuardPolicies = append(a.cfg.ExtraGuardPolicies, policies...)
}

// builtinContinuationDeciders returns hard vetoes that run before the
// configured chain. A built-in can only stop the loop — it is never
// consulted for continuation.
func (a *Agent) builtinContinuationDeciders() []ContinuationDecider {
	return []ContinuationDecider{a.goalBudgetVeto}
}

// refreshEphemeralReminders stages reminders for the upcoming turn. The
// injection manager asks each injector in turn; injectors decide for
// themselves whether to speak or stay silent (silence is the default), so
// most turns inject nothing.
func (a *Agent) refreshEphemeralReminders(turn int) {
	a.mgr.ClearEphemeralReminders()
	reminders := a.injector.Collect(InjectContext{Turn: turn, Messages: a.mgr.AllMessages()})
	if len(reminders) > 0 {
		a.mgr.SetEphemeralReminders(reminders)
	}
}

func (a *Agent) appendMessage(msg models.AgentMessage) {
	a.mgr.AppendRecent(msg)
}

// appendSwitchNotice persists a mode-switch release notice into the
// conversation as an ordinary user message (the same channel steering
// messages use). Unlike the standing reminders, which are ephemeral because
// they re-derive current state every turn, a switch is a one-shot event: the
// model should be able to find the transition point when it looks back over
// its history.
func (a *Agent) appendSwitchNotice(text string) {
	a.appendMessage(models.UserMessage(text))
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
		// A durable-record failure is reported but the fold itself stands, so fall
		// through and emit the commit event: the context really is smaller and the
		// UI has to reflect that. Returning here would leave the display showing a
		// window that no longer exists.
		if !res.Committed {
			return
		}
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
		// Compaction folded the reminders' context away: every injector
		// re-injects once on the next turn, then resumes its quiet cadence.
		a.injector.OnCompacted()
		a.emit(ctx, events.CompactionCommittedEvent{
			Base:         events.Base{Type: events.CompactionCommitted, Turn: turn},
			Summary:      res.Summary,
			FirstKeptID:  res.FirstKeptID,
			TokensBefore: res.TokensBefore,
			// Stats() reflects the post-fold context, so its total is the after count.
			TokensAfter: a.mgr.Stats()["total"],
			Degraded:    res.Degraded,
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

// MicroCompactStatus reports the mechanical tool-result trimming status for
// /status echo ("" when disabled).
func (a *Agent) MicroCompactStatus() string {
	if a.mgr == nil {
		return ""
	}
	return a.mgr.MicroCompactStatus()
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

// applyMode returns the base system prompt and the tool definitions for the
// active mode. The mode text itself is injected by the modeInjector at the
// turn boundary (see refreshEphemeralReminders), not here.
//
// The mode text travels as an ephemeral reminder rather than being written
// into the system block, and the tool list is returned unfiltered. Both are
// deliberate: Anthropic's cache prefix is ordered tools -> system -> messages,
// so rewriting the system block on a switch_mode invalidated everything after
// it, and filtering the tool array invalidated the widest layer of all —
// re-billing the whole conversation as fresh input. Ephemeral reminders are
// appended after the last cache breakpoint is computed (see
// contextmgr.BuildTurnRequest), so the mode text costs only its own bytes and
// leaves the cached prefix untouched.
//
// Mode tool restrictions are enforced at execution time in executor.execute
// instead, matching how the skill layer already handles the same problem.
func (a *Agent) applyMode() (string, []models.ToolDefinition) {
	base := a.cfg.BaseSystemPrompt
	if base == "" && a.mgr != nil {
		if b, ok := a.mgr.GetBlock(contextmgr.BlockSystem, "system"); ok {
			base = b.Text()
		}
	}

	tools := a.executor.baseToolDefinitions()
	if a.mgr == nil {
		return base, tools
	}

	// Evict before returning, not after: a mode block restored from a
	// checkpoint written before mode text became an ephemeral reminder must go
	// even when no mode manager is configured, or it stays in the system prompt —
	// and so in the cache prefix — for the rest of the session.
	a.mgr.RemoveBlock(contextmgr.BlockMode, "mode")

	return base, tools
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
