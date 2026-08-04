package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/lcoder/lcoder/pkg/checkpoint"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/task"
)

// InjectContext carries the per-turn inputs every injector sees.
type InjectContext struct {
	Turn     int
	Messages []models.AgentMessage
}

// Injector is the dedup decision unit for one reminder source. It runs at
// every turn boundary, but returning an empty string means the turn gets no
// injection at all — silence is the default, injection is the exception
// (kimi-code's DynamicInjector contract, see docs/kimi-injector-mechanism.md).
type Injector interface {
	// Variant is the injector's stable identity, used as the snapshot key.
	Variant() string
	Inject(ctx InjectContext) string
	// OnCompacted resets the dedup bookkeeping so the next turn re-injects
	// the constraint once before the quiet cadence resumes.
	OnCompacted()
	Snapshot() checkpoint.InjectorState
	Restore(checkpoint.InjectorState)
}

// InjectionManager holds the ordered injector set and aggregates their
// per-turn output into the ephemeral reminder list.
type InjectionManager struct {
	injectors []Injector
}

// newInjectionManager assembles the built-in injectors: the todo reminder,
// the mode reminder, and one adapter per configured ReminderProducer. cfg is
// retained by pointer because the active mode is mutated in place on
// switch_mode and checkpoint restore. onSwitch receives the mode-switch
// release notice so the agent can persist it into the conversation; nil keeps
// the notice ephemeral (see modeInjector).
func newInjectionManager(taskMgr *task.Manager, cfg *Config, producers []ReminderProducer, onSwitch func(string)) *InjectionManager {
	if taskMgr == nil {
		taskMgr = task.NewManager()
	}
	injectors := []Injector{
		newTodoInjector(taskMgr),
		newModeInjector(cfg, onSwitch),
	}
	for i, p := range producers {
		injectors = append(injectors, producerInjector{
			variant:  fmt.Sprintf("reminder_producer_%d", i),
			producer: p,
		})
	}
	return &InjectionManager{injectors: injectors}
}

// Collect returns the non-empty injections for the upcoming turn, in
// injector registration order. A nil manager (agents constructed directly
// rather than via New) collects nothing.
func (m *InjectionManager) Collect(ctx InjectContext) []string {
	if m == nil {
		return nil
	}
	var out []string
	for _, inj := range m.injectors {
		if s := inj.Inject(ctx); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// OnCompacted broadcasts the compaction signal: every injector re-injects
// once on the next turn, then resumes its quiet cadence.
func (m *InjectionManager) OnCompacted() {
	if m == nil {
		return
	}
	for _, inj := range m.injectors {
		inj.OnCompacted()
	}
}

// Snapshot captures every injector's bookkeeping, keyed by variant. Used by
// WithMode (carry state across the agent clone) and by checkpoint capture.
func (m *InjectionManager) Snapshot() map[string]checkpoint.InjectorState {
	if m == nil {
		return nil
	}
	out := make(map[string]checkpoint.InjectorState, len(m.injectors))
	for _, inj := range m.injectors {
		out[inj.Variant()] = inj.Snapshot()
	}
	return out
}

// Restore reinstates injector bookkeeping from a snapshot. Variants missing
// from the map keep their zero state, so old checkpoints stay compatible.
func (m *InjectionManager) Restore(states map[string]checkpoint.InjectorState) {
	if m == nil {
		return
	}
	for _, inj := range m.injectors {
		if st, ok := states[inj.Variant()]; ok {
			inj.Restore(st)
		}
	}
}

// todoReminderQuietTurns is how many turns must pass since the last task-list
// write AND since the last reminder before the todo reminder fires again.
const todoReminderQuietTurns = 10

// todoInjector reminds about unfinished tasks at most once per quiet window.
// A changed task list counts as a "write" (the model just saw the tool
// result, so a reminder would only repeat it); the reminder stays silent
// while either the write or the previous reminder is still fresh.
type todoInjector struct {
	mgr *task.Manager

	lastFingerprint  string
	hasWrite         bool
	lastWriteTurn    int
	hasReminder      bool
	lastReminderTurn int
	forceNext        bool
}

func newTodoInjector(mgr *task.Manager) *todoInjector {
	return &todoInjector{mgr: mgr}
}

func (t *todoInjector) Variant() string { return "todo_list_reminder" }

func (t *todoInjector) Inject(ctx InjectContext) string {
	fp := fingerprintTasks(t.mgr.List())
	if !t.hasWrite || fp != t.lastFingerprint {
		t.lastFingerprint = fp
		t.hasWrite = true
		t.lastWriteTurn = ctx.Turn
	}

	text := t.mgr.FormatReminder()
	if text == "" {
		// No tasks at all, or everything is done: nothing to remind about.
		return ""
	}
	if !t.forceNext {
		if t.hasWrite && ctx.Turn-t.lastWriteTurn < todoReminderQuietTurns {
			return ""
		}
		if t.hasReminder && ctx.Turn-t.lastReminderTurn < todoReminderQuietTurns {
			return ""
		}
	}
	t.forceNext = false
	t.hasReminder = true
	t.lastReminderTurn = ctx.Turn
	return text
}

func (t *todoInjector) OnCompacted() {
	// Force exactly one re-injection; the write window then re-applies, so
	// the quiet cadence resumes right after it.
	t.forceNext = true
}

func (t *todoInjector) Snapshot() checkpoint.InjectorState {
	return checkpoint.InjectorState{
		LastFingerprint: t.lastFingerprint,
		HasWrite:        t.hasWrite,
		LastWriteTurn:   t.lastWriteTurn,
		HasInject:       t.hasReminder,
		LastInjectTurn:  t.lastReminderTurn,
		ForceNext:       t.forceNext,
	}
}

func (t *todoInjector) Restore(st checkpoint.InjectorState) {
	t.lastFingerprint = st.LastFingerprint
	t.hasWrite = st.HasWrite
	t.lastWriteTurn = st.LastWriteTurn
	t.hasReminder = st.HasInject
	t.lastReminderTurn = st.LastInjectTurn
	t.forceNext = st.ForceNext
}

// fingerprintTasks hashes the task list (text + status, in order) so a write
// is detected without threading turn numbers through the todo tool path.
func fingerprintTasks(tasks []task.Task) string {
	var b strings.Builder
	for _, t := range tasks {
		b.WriteString(string(t.Status))
		b.WriteByte(':')
		b.WriteString(t.Text)
		b.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

const (
	// modeReminderSparseTurns is the minimum distance from the last mode
	// injection before the abbreviated reminder may fire again.
	modeReminderSparseTurns = 2
	// modeReminderFullRefreshTurns is how many turns may pass since the last
	// full mode prompt before it is re-sent in full.
	modeReminderFullRefreshTurns = 5
)

// modeInjector injects the active mode's prompt on a quiet cadence: the full
// prompt on mode entry/switch and every modeReminderFullRefreshTurns turns,
// the sparse prompt when at least modeReminderSparseTurns turns have passed
// since the last injection, and nothing at all in between. Without a
// SparsePrompt the sparse tier stays silent instead of falling back to the
// full text.
//
// The mode-switch release notice is treated differently from the standing
// reminders: a switch is a one-shot EVENT, not re-derivable state, so it is
// handed to onSwitch for persistence into the conversation history. The model
// can then find the transition point when it looks back, and the notice never
// goes stale the way a repeated standing reminder would. A nil onSwitch keeps
// the notice ephemeral so it is never dropped silently.
type modeInjector struct {
	cfg      *Config
	onSwitch func(string)

	lastMode       string
	hasFull        bool
	lastFullTurn   int
	lastInjectTurn int
	forceNext      bool
}

func newModeInjector(cfg *Config, onSwitch func(string)) *modeInjector {
	return &modeInjector{cfg: cfg, onSwitch: onSwitch}
}

func (m *modeInjector) Variant() string { return "mode_reminder" }

func (m *modeInjector) Inject(ctx InjectContext) string {
	if m.cfg == nil || m.cfg.ModeManager == nil {
		return ""
	}
	mode := m.cfg.ModeManager.Get(m.cfg.Mode)
	prev := m.lastMode
	switched := prev != mode.Name
	m.lastMode = mode.Name

	var parts []string
	if switched && prev != "" {
		// The previous reminder is gone from context, so the model would
		// otherwise still be acting under the old mode's restrictions.
		notice := "You have switched from " + prev + " mode to " + mode.Name +
			" mode. Any tool restrictions from " + prev + " mode no longer apply."
		if m.onSwitch != nil {
			m.onSwitch(notice) // persisted event; see the type comment
		} else {
			parts = append(parts, notice)
		}
	}
	if mode.SystemPrompt == "" {
		return strings.Join(parts, "\n\n")
	}

	sinceInject := ctx.Turn - m.lastInjectTurn
	sinceFull := ctx.Turn - m.lastFullTurn
	switch {
	case switched || m.forceNext || !m.hasFull || sinceFull >= modeReminderFullRefreshTurns:
		parts = append(parts, "# Mode: "+mode.Name+"\n\n"+mode.SystemPrompt)
		m.hasFull = true
		m.lastFullTurn = ctx.Turn
		m.lastInjectTurn = ctx.Turn
	case sinceInject >= modeReminderSparseTurns && mode.SparsePrompt != "":
		parts = append(parts, "# Mode: "+mode.Name+"\n\n"+mode.SparsePrompt)
		m.lastInjectTurn = ctx.Turn
	default:
		// Silent: the previous reminder is still fresh in context.
	}
	m.forceNext = false
	return strings.Join(parts, "\n\n")
}

func (m *modeInjector) OnCompacted() {
	m.forceNext = true
}

func (m *modeInjector) Snapshot() checkpoint.InjectorState {
	return checkpoint.InjectorState{
		LastMode:       m.lastMode,
		HasFull:        m.hasFull,
		LastFullTurn:   m.lastFullTurn,
		LastInjectTurn: m.lastInjectTurn,
		ForceNext:      m.forceNext,
	}
}

func (m *modeInjector) Restore(st checkpoint.InjectorState) {
	m.lastMode = st.LastMode
	m.hasFull = st.HasFull
	m.lastFullTurn = st.LastFullTurn
	m.lastInjectTurn = st.LastInjectTurn
	m.forceNext = st.ForceNext
}

// producerInjector adapts a legacy ReminderProducer func to the Injector
// interface. Producers are turn-scoped and self-deduping by contract, so the
// adapter keeps no bookkeeping.
type producerInjector struct {
	variant  string
	producer ReminderProducer
}

func (p producerInjector) Variant() string { return p.variant }

func (p producerInjector) Inject(ctx InjectContext) string {
	return strings.Join(p.producer(ctx.Messages), "\n\n")
}

func (p producerInjector) OnCompacted() {}

func (p producerInjector) Snapshot() checkpoint.InjectorState { return checkpoint.InjectorState{} }

func (p producerInjector) Restore(checkpoint.InjectorState) {}
