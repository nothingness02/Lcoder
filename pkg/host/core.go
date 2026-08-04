// Package host implements the host-side half of the UI/agent protocol
// boundary (see pkg/agentapi). Core composes an *agent.Agent with the session
// store, the event bus, and a goal driver goroutine to provide the full
// agentapi.CoreAPI surface: the run-control, goal, task, context, and
// checkpoint methods delegate to the current agent, while the session and
// mode write paths (SetMode/OpenSession/NewSession/TruncateAfter) and the
// goal pursuit loop live here.
//
// Two invariants are inherited from the TUI code this package replaces:
//
//   - Session persistence is a SYNCHRONOUS bus subscription on TurnEnd/
//     AgentEnd, so the session on disk is always at least as new as the
//     automatic checkpoint written at the turn boundary (a crash cannot
//     resurrect a checkpoint whose messages were never saved).
//   - Switching the active session atomically swaps the agent's messages,
//     session id, and task list, then re-points the persistence mirror and
//     notifies the compaction-sink wiring (the old onSessionChange hook).
//   - Runs single-flight through the Core: Prompt/Continue and the goal
//     driver hold a run slot for their whole duration (a pursuit counts as
//     busy between turns too), and the state-changing operations
//     (SetMode/OpenSession/NewSession/TruncateAfter/RestoreCheckpoint)
//     refuse to run while any run is in flight (ErrAgentBusy). The "idle
//     first" discipline is enforced here, not left to callers.
package host

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/agentapi"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/session"
	"github.com/lcoder/lcoder/pkg/task"
)

var _ agentapi.CoreAPI = (*Core)(nil)

var (
	// ErrAgentBusy is returned by run submissions and state-changing
	// operations while a run (ad-hoc or goal-driven) is in flight.
	ErrAgentBusy = errors.New("host: agent is running")
	// ErrCoreClosed is returned by run submissions and state-changing
	// operations after Close.
	ErrCoreClosed = errors.New("host: core is closed")
)

// Core is the stable agentapi.CoreAPI handle a UI holds. The underlying
// runner (*agent.Agent) may be swapped by SetMode; the Core itself never
// changes identity.
type Core struct {
	bus   *events.Bus
	store *session.Store
	cwd   string

	mirror *sessionMirror

	// onSessionChange absorbs the TUI's onSessionChange hook: cmd wiring uses
	// it to point the agentsetup.SessionCompactionSink (and the subagent
	// host's parent session) at the session actually in use.
	onSessionChange func(*session.Session)

	// mu guards runner and confirm: SetMode swaps the runner while runs,
	// the mirror, or the goal driver may be reading it.
	mu      sync.Mutex
	runner  *agent.Agent
	confirm agentapi.UserConfirmation

	// runMu is the single-flight run slot (TryLock only): ad-hoc
	// Prompt/Continue runs and state-change operations hold it for their
	// duration; the goal driver holds it for its whole pursuit.
	runMu sync.Mutex

	// Goal driver lifecycle. goalDone is non-nil exactly while a driver
	// goroutine is running; it is closed when that goroutine exits. closed
	// is set by Close and makes run submissions/state changes fail and goal
	// methods no-op.
	goalMu     sync.Mutex
	goalCancel context.CancelFunc
	goalDone   chan struct{}
	closed     bool

	unsubscribeMirror func()
}

// NewCore builds the host around an already-assembled agent and its opening
// session. bus must be the same bus the agent emits on. The checkpoint store
// is NOT taken here: it already lives in the agent's config (written by the
// run loop for automatic checkpoints and used by the agent-side
// Save/Restore/ListCheckpoint implementations Core delegates to).
//
// onSessionChange, when non-nil, is invoked immediately with sess (so the
// caller starts in sync) and then on every OpenSession/NewSession.
func NewCore(ag *agent.Agent, bus *events.Bus, store *session.Store, sess *session.Session, cwd string, onSessionChange func(*session.Session)) *Core {
	c := &Core{
		bus:             bus,
		store:           store,
		cwd:             cwd,
		mirror:          &sessionMirror{},
		onSessionChange: onSessionChange,
		runner:          ag,
	}
	c.mirror.setActive(sess)
	c.unsubscribeMirror = bus.Subscribe(c.persistFromEvent)
	if onSessionChange != nil {
		onSessionChange(sess)
	}
	return c
}

// Close marks the core closed, then stops the goal driver (cancel first, then
// wait for the exit with a bounded timeout) and only then detaches the
// persistence mirror, so a turn unwinding during the wait is still persisted.
// The agent itself is left usable. Close is idempotent.
func (c *Core) Close() {
	c.goalMu.Lock()
	if c.closed {
		c.goalMu.Unlock()
		return
	}
	c.closed = true
	cancel := c.goalCancel
	done := c.goalDone
	c.goalMu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	}
	if c.unsubscribeMirror != nil {
		c.unsubscribeMirror()
		c.unsubscribeMirror = nil
	}
}

func (c *Core) currentRunner() *agent.Agent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.runner
}

func (c *Core) isClosed() bool {
	c.goalMu.Lock()
	defer c.goalMu.Unlock()
	return c.closed
}

// Running reports whether any run is in flight — an ad-hoc Prompt/Continue or
// a goal pursuit (the driver holds the run slot between turns too). It is an
// extra Core method (not part of agentapi.CoreAPI) for callers that report
// busy state, e.g. rpcserver's get_state.
func (c *Core) Running() bool {
	c.goalMu.Lock()
	driving := c.goalDone != nil
	c.goalMu.Unlock()
	if driving {
		return true
	}
	if !c.runMu.TryLock() {
		return true
	}
	c.runMu.Unlock()
	return false
}

// tryAcquireIdle takes the run slot for a state-change operation
// (SetMode/OpenSession/NewSession/TruncateAfter/RestoreCheckpoint), refusing
// while any run or the goal driver is in flight. On success the caller owns
// the slot and must release it with c.runMu.Unlock().
func (c *Core) tryAcquireIdle() error {
	c.goalMu.Lock()
	defer c.goalMu.Unlock()
	if c.closed {
		return ErrCoreClosed
	}
	if c.goalDone != nil {
		return ErrAgentBusy
	}
	if !c.runMu.TryLock() {
		return ErrAgentBusy
	}
	return nil
}

// ---------------------------------------------------------------------------
// Session persistence mirror (moved from tui.Model.persistFromEvent)
// ---------------------------------------------------------------------------

// sessionMirror holds the session persistence currently writes go to. It is
// switched by OpenSession/NewSession; TruncateAfter keeps the same session
// (Fork re-points the branch inside it).
type sessionMirror struct {
	mu     sync.Mutex
	active *session.Session
}

func (m *sessionMirror) setActive(s *session.Session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active = s
}

func (m *sessionMirror) activeSession() *session.Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active
}

// persistFromEvent mirrors the agent's context window into the active session
// after each turn. It is a SYNCHRONOUS bus subscriber.
//
// Compactions are persisted by the context manager's CompactionSink
// (agentsetup.SessionCompactionSink), inside the same call that folds the
// context — not from here, where a missed event would silently leave the
// session claiming the folded messages are still active.
func (c *Core) persistFromEvent(_ context.Context, ev events.Event) error {
	switch ev.(type) {
	case events.TurnEndEvent, events.AgentEndEvent:
		sess := c.mirror.activeSession()
		if sess == nil {
			return nil
		}
		// Mirror the completed turn's assistant/tool messages into the session
		// now. This handler runs synchronously inside the agent's TurnEnd
		// emission, which precedes the automatic checkpoint written at the
		// turn boundary — so the session on disk is always at least as new as
		// any checkpoint, and a crash cannot resurrect a checkpoint whose
		// messages were never saved.
		_ = sess.AppendMissing(c.currentRunner().AllMessages())
	}
	return nil
}

// ---------------------------------------------------------------------------
// Run control
// ---------------------------------------------------------------------------

// Prompt appends the user message to the active session and starts a run.
// (The session append used to live in the TUI's runner queue; persistence is
// a host responsibility now.) Runs single-flight: while any run or goal
// pursuit is in flight, Prompt fails with ErrAgentBusy.
func (c *Core) Prompt(ctx context.Context, msg models.AgentMessage) error {
	if !c.runMu.TryLock() {
		return ErrAgentBusy
	}
	defer c.runMu.Unlock()
	if c.isClosed() {
		return ErrCoreClosed
	}
	return c.promptWithSession(ctx, c.currentRunner(), msg)
}

// promptWithSession is the shared submit path for Prompt and the goal
// driver's continuation prompts: persist the message first, then run. The
// caller must already hold the run slot.
func (c *Core) promptWithSession(ctx context.Context, r *agent.Agent, msg models.AgentMessage) error {
	if sess := c.mirror.activeSession(); sess != nil {
		if err := sess.Append(msg); err != nil {
			return err
		}
	}
	return r.Prompt(ctx, msg)
}

// Continue starts a run without a new user message. It single-flights with
// Prompt (ErrAgentBusy while a run is in flight).
func (c *Core) Continue(ctx context.Context) error {
	if !c.runMu.TryLock() {
		return ErrAgentBusy
	}
	defer c.runMu.Unlock()
	if c.isClosed() {
		return ErrCoreClosed
	}
	return c.currentRunner().Continue(ctx)
}

// Steer injects a user message during the next safe boundary. While a goal
// driver is pursuing, this is the way to talk to the agent — a mid-pursuit
// Prompt would race the driver's own run.
func (c *Core) Steer(msg models.AgentMessage) { c.currentRunner().Steer(msg) }

// Abort signals the current run to stop gracefully. When a goal driver is
// pursuing, the interrupted run makes the driver pause the goal and exit,
// mirroring the TUI's Esc-then-"goal driver stopped" behavior.
func (c *Core) Abort() { c.currentRunner().Abort() }

// AllMessages returns the full conversation (read-only query).
func (c *Core) AllMessages() []models.AgentMessage { return c.currentRunner().AllMessages() }

// SetUserConfirm wires the interactive permission approval callback. It is
// remembered and re-applied to the runner SetMode swaps in. (The subagent
// host's confirmation is wired separately by the caller with the same shared
// callback object, so a mode switch does not disturb it.)
func (c *Core) SetUserConfirm(uc agentapi.UserConfirmation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.confirm = uc
	c.runner.SetUserConfirm(uc)
}

// ---------------------------------------------------------------------------
// Context / mode / model
// ---------------------------------------------------------------------------

// ContextStats returns the structured context token accounting.
func (c *Core) ContextStats() agentapi.ContextStats { return c.currentRunner().ContextStats() }

// SetSkillsBlock writes (or, for an empty string, removes) the skills context
// block.
func (c *Core) SetSkillsBlock(content string) { c.currentRunner().SetSkillsBlock(content) }

// Mode returns the current permission mode.
func (c *Core) Mode() string { return c.currentRunner().Mode() }

// SetMode switches the permission mode by swapping in the agent's WithMode
// clone (fresh context manager for the mode's system prompt; goals, task
// manager, and injector state carry over). The Core handle and the bus stay
// the same, so subscribers and the mirror are unaffected. Swapping the runner
// mid-run would strand the in-flight loop, so SetMode refuses while busy
// (ErrAgentBusy).
func (c *Core) SetMode(mode string) error {
	if err := c.tryAcquireIdle(); err != nil {
		return err
	}
	defer c.runMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	next := c.runner.WithMode(mode)
	ag, ok := next.(*agent.Agent)
	if !ok {
		return fmt.Errorf("host: mode switch returned unexpected runner type %T", next)
	}
	if c.confirm != nil {
		ag.SetUserConfirm(c.confirm)
	}
	c.runner = ag
	return nil
}

// SwitchModel changes the model for subsequent turns, preserving history.
func (c *Core) SwitchModel(ref models.ModelRef, budget contextmgr.TokenBudget) {
	c.currentRunner().SwitchModel(ref, budget)
}

// SwitchThinking replaces the resolved thinking value for subsequent turns.
func (c *Core) SwitchThinking(thinking string) { c.currentRunner().SwitchThinking(thinking) }

// Thinking returns the current resolved thinking value.
func (c *Core) Thinking() string { return c.currentRunner().Thinking() }

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

// SessionID returns the session the agent is currently associated with.
func (c *Core) SessionID() string { return c.currentRunner().SessionID() }

// OpenSession loads an existing session and swaps it in atomically: agent
// messages + session id, task list rebuilt from the loaded history, the
// persistence mirror re-pointed, and the compaction-sink wiring notified.
// (Moved from tui.Model.loadSession.) Refuses while a run is in flight
// (ErrAgentBusy).
func (c *Core) OpenSession(sessionID string) error {
	if err := c.tryAcquireIdle(); err != nil {
		return err
	}
	defer c.runMu.Unlock()
	sess, err := c.store.LoadByID(c.cwd, sessionID)
	if err != nil {
		return fmt.Errorf("host: open session %q: %w", sessionID, err)
	}
	c.applySession(sess)
	return nil
}

// NewSession starts a fresh, empty session. (Moved from the TUI /new handler;
// Create defers the file write until the first message, so an untouched new
// session leaves no record.) Refuses while a run is in flight (ErrAgentBusy).
func (c *Core) NewSession() error {
	if err := c.tryAcquireIdle(); err != nil {
		return err
	}
	defer c.runMu.Unlock()
	sess, err := c.store.Create(c.cwd)
	if err != nil {
		return fmt.Errorf("host: new session: %w", err)
	}
	c.applySession(sess)
	return nil
}

// applySession is the shared session-swap path: load/create produced sess;
// here the agent state, mirror, and sink wiring follow.
func (c *Core) applySession(sess *session.Session) {
	r := c.currentRunner()
	r.SetSessionID(sess.ID)
	r.SetMessages(sess.EffectiveMessages())
	// Rebuild the task list from the latest todo_write call in the loaded
	// history (nil when none, which also clears a stale list).
	if tm := r.TaskManager(); tm != nil {
		_ = tm.Restore(task.ManagerState{Tasks: tasksFromMessages(sess.ActiveMessages())})
	}
	c.mirror.setActive(sess)
	if c.onSessionChange != nil {
		c.onSessionChange(sess)
	}
}

// TruncateAfter drops everything after the given message id (/retry). The
// rollback is pi-style: the session forks at messageID, so the retry forms a
// new branch while the abandoned tail stays reachable on the old one; the
// agent context is pruned to the same point. An empty messageID forks at the
// root and clears the context. The caller re-submits the prompt afterwards,
// so nothing is duplicated. (Moved from tui.Model.retryLast.) Refuses while a
// run is in flight (ErrAgentBusy).
//
// messageID is given in the runner's CURRENT (post-compaction) view. The old
// implementation forked the session and then rebuilt the context from
// sess.EffectiveMessages() — wrong after a fold: a fork point inside the
// folded span drops the compaction entry from the new branch, and
// EffectiveMessages then resurrects the full uncompressed history. Here the
// context cut is an exact slice of the runner's live (compacted) view, and
// the fork target is mapped onto raw-branch coordinates by mapForkTarget.
func (c *Core) TruncateAfter(messageID string) error {
	if err := c.tryAcquireIdle(); err != nil {
		return err
	}
	defer c.runMu.Unlock()

	sess := c.mirror.activeSession()
	if sess == nil {
		return fmt.Errorf("host: no active session")
	}
	r := c.currentRunner()
	msgs := r.AllMessages()

	cut := 0
	if messageID != "" {
		cut = -1
		for i := range msgs {
			if msgs[i].ID == messageID {
				cut = i + 1 // the fork point itself is kept
				break
			}
		}
		if cut < 0 {
			return fmt.Errorf("host: cannot truncate: message %q not found", messageID)
		}
	}

	forkAt := messageID
	if messageID != "" {
		var err error
		forkAt, err = mapForkTarget(sess, msgs[cut-1])
		if err != nil {
			return err
		}
	}
	if _, err := sess.Fork(forkAt); err != nil {
		return err
	}
	r.SetMessages(msgs[:cut])
	return nil
}

// mapForkTarget maps a fork point from the runner's compacted view onto the
// session's raw branch coordinates:
//
//   - A post-entry message forks directly; the compaction entry stays on the
//     new branch and its effective view matches the runner cut exactly.
//   - A target at or before the newest compaction entry (a kept message, or
//     the entry id that EffectiveMessages reuses for its summary) clamps to
//     the entry: forking inside the folded span would drop the entry from the
//     branch and resurrect the pre-compaction history. The branch's effective
//     view is then [summary, kept...] — kept messages beyond the cut may
//     reappear on reload, but the folded-away history never does.
//   - An id that is in the runner view but not on the branch at all is the
//     runtime-only summary message of a live fold (fresh id, marked
//     compacted); it maps to the newest compaction entry too.
func mapForkTarget(sess *session.Session, target models.AgentMessage) (string, error) {
	active := sess.ActiveMessages()
	entryIdx, targetIdx := -1, -1
	for i, m := range active {
		if session.IsCompactionEntry(m) {
			entryIdx = i // keep the newest entry
		}
		if m.ID == target.ID {
			targetIdx = i
		}
	}
	if targetIdx < 0 {
		if entryIdx >= 0 && target.Metadata["compacted"] == true {
			return active[entryIdx].ID, nil
		}
		return "", fmt.Errorf("host: cannot truncate: message %q not found in the active session", target.ID)
	}
	if entryIdx >= 0 && targetIdx <= entryIdx {
		return active[entryIdx].ID, nil
	}
	return target.ID, nil
}

// tasksFromMessages rebuilds the task list from history by finding the most
// recent todo_write tool call. Returns nil when none is present.
// (Same logic as the TUI's tasksidebar.go helper.)
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

// ---------------------------------------------------------------------------
// Tasks / skills
// ---------------------------------------------------------------------------

// Tasks returns a snapshot of the agent's declared task list.
func (c *Core) Tasks() []task.Task { return c.currentRunner().Tasks() }

// ClearSkillFilter lifts any active skill tool restriction.
func (c *Core) ClearSkillFilter() { c.currentRunner().ClearSkillFilter() }

// ---------------------------------------------------------------------------
// Goal pursuit
// ---------------------------------------------------------------------------

// Goal returns a copy of the current goal record, or nil.
func (c *Core) Goal() *agentapi.GoalState { return c.currentRunner().Goal() }

// StartGoal creates an active goal and launches the asynchronous goal driver
// that pursues it across runs (the loop previously driven by the TUI's
// continueGoalIfActive wiring). The first run uses objective as its prompt;
// continuations come from agent.NextGoalAction.
//
// Re-entrant StartGoal while a driver is already running replaces the goal
// record and steers the new objective into the in-flight run (the protocol's
// recommended channel for talking to a pursuit): no second goroutine is
// spawned and the objective is not dropped.
func (c *Core) StartGoal(objective string, turnBudget, tokenBudget int) {
	c.goalMu.Lock()
	closed := c.closed
	driverRunning := c.goalDone != nil
	c.goalMu.Unlock()
	if closed {
		return
	}
	r := c.currentRunner()
	r.StartGoal(objective, turnBudget, tokenBudget)
	if driverRunning {
		r.Steer(models.UserMessage(objective))
		return
	}
	c.ensureGoalDriver(objective)
}

// PauseGoal suspends an active goal; the driver exits at the next run
// boundary (the in-flight run finishes first, exactly as the TUI's /goal
// pause behaved).
func (c *Core) PauseGoal(reason string) { c.currentRunner().PauseGoal(reason) }

// ResumeGoal reactivates a paused/blocked goal and relaunches the driver when
// none is running. The first prompt of the relaunched driver is decided with
// EndReasonCompleted (a fresh pursuit turn), so a goal paused by an abort
// actually continues — unlike the old TUI wiring, which re-paused on a stale
// interrupted LastEndReason.
//
// A resume racing a driver exit waits for that driver to settle its terminal
// state first: the driver applies its pause/block and clears goalDone before
// closing the done channel, so the resume is never swallowed by the exit
// window and reliably relaunches a fresh driver.
func (c *Core) ResumeGoal() {
	c.goalMu.Lock()
	closed := c.closed
	done := c.goalDone
	c.goalMu.Unlock()
	if closed {
		return
	}
	if done != nil {
		<-done
	}
	r := c.currentRunner()
	r.ResumeGoal()
	g := r.Goal()
	if g == nil || g.Status != agentapi.GoalActive {
		return
	}
	prompt, done2 := agent.NextGoalAction(g, events.EndReasonCompleted)
	if done2 {
		r.BlockGoal("a configured budget was reached")
		return
	}
	c.ensureGoalDriver(prompt)
}

// CancelGoal clears the goal record; the driver exits at the next boundary.
func (c *Core) CancelGoal() { c.currentRunner().CancelGoal() }

// LastEndReason returns how the most recent run ended.
func (c *Core) LastEndReason() events.AgentEndReason { return c.currentRunner().LastEndReason() }

// MicroCompactStatus returns the mechanical tool-result trimming status.
func (c *Core) MicroCompactStatus() string { return c.currentRunner().MicroCompactStatus() }

// ensureGoalDriver spawns the pursuit goroutine unless one is already
// running or the core is closed; first is the prompt for the driver's first
// run.
func (c *Core) ensureGoalDriver(first string) {
	c.goalMu.Lock()
	defer c.goalMu.Unlock()
	if c.closed || c.goalDone != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	c.goalCancel = cancel
	c.goalDone = done
	go c.runGoalDriver(ctx, first, done)
}

// runGoalDriver is the asynchronous equivalent of agent.GoalDriver.Run: it
// pursues the active goal with ordinary Prompt runs until the goal settles.
// The driver holds the run slot for its whole lifetime — waits between turns
// included — so a pursuit single-flights with ad-hoc Prompt/Continue calls
// and the core never reports idle mid-goal. Like GoalDriver it resolves the
// current runner every iteration, so a SetMode between pursuits continues on
// the swapped-in agent.
func (c *Core) runGoalDriver(ctx context.Context, first string, doneCh chan struct{}) {
	// settle applies the terminal goal transition on exit. It runs before
	// goalDone is cleared and doneCh is closed, so a ResumeGoal observing the
	// exit always sees the settled state and reliably relaunches.
	acquired := false
	var settle func(r *agent.Agent)
	defer func() {
		if settle != nil {
			settle(c.currentRunner())
		}
		c.goalMu.Lock()
		c.goalDone = nil
		c.goalCancel = nil
		c.goalMu.Unlock()
		if acquired {
			c.runMu.Unlock()
		}
		close(doneCh)
	}()

	// Take the run slot, giving way to an ad-hoc run already in flight; a
	// cancelled driver gives up instead of queueing behind it.
	for !c.runMu.TryLock() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Millisecond):
		}
	}
	acquired = true

	next := first
	for {
		r := c.currentRunner()
		g := r.Goal()
		if g == nil || g.Status != agentapi.GoalActive {
			return // settled by the model (update_goal), cancelled, or paused
		}
		if g.OverBudget() {
			settle = func(r *agent.Agent) { r.BlockGoal("a configured budget was reached") }
			return
		}
		// A cancel/pause landing since the previous run ended must not buy an
		// extra pursuit turn: re-check immediately before submitting.
		if ctx.Err() != nil {
			return
		}
		if g := r.Goal(); g == nil || g.Status != agentapi.GoalActive {
			return
		}
		r.NoteGoalTurn()

		if err := c.promptWithSession(ctx, r, models.UserMessage(next)); err != nil {
			settle = func(r *agent.Agent) { r.PauseGoal(err.Error()) }
			return
		}
		reason := r.LastEndReason()
		if reason == events.EndReasonInterrupted || reason == events.EndReasonError {
			settle = func(r *agent.Agent) { r.PauseGoal(string(reason)) }
			return
		}

		prompt, done := agent.NextGoalAction(r.Goal(), reason)
		if done {
			settle = func(r *agent.Agent) {
				if g := r.Goal(); g != nil && g.Status == agentapi.GoalActive {
					r.BlockGoal("a configured budget was reached")
				}
			}
			return
		}
		next = prompt
	}
}

// ---------------------------------------------------------------------------
// Checkpoints (delegate to the agent, which owns the store via its config)
// ---------------------------------------------------------------------------

// SaveCheckpoint captures and persists the current state, returning the
// identifier the checkpoint can be restored under.
func (c *Core) SaveCheckpoint() (string, error) { return c.currentRunner().SaveCheckpoint() }

// RestoreCheckpoint loads the checkpoint stored under id and applies it.
// Applying state mid-run would strand the in-flight loop, so it refuses
// while busy (ErrAgentBusy).
func (c *Core) RestoreCheckpoint(id string) error {
	if err := c.tryAcquireIdle(); err != nil {
		return err
	}
	defer c.runMu.Unlock()
	return c.currentRunner().RestoreCheckpoint(id)
}

// ListCheckpoints lists the stored checkpoint entries.
func (c *Core) ListCheckpoints() ([]agentapi.CheckpointInfo, error) {
	return c.currentRunner().ListCheckpoints()
}

// ---------------------------------------------------------------------------
// Session listing (session picker)
// ---------------------------------------------------------------------------

// ListSessions returns the metadata of the current project's sessions for the
// session picker. Subagent journals are included but flagged so the picker can
// filter them (they are loadable but not user-facing).
func (c *Core) ListSessions() ([]agentapi.SessionInfo, error) {
	sessions, err := c.store.List(c.cwd)
	if err != nil {
		return nil, err
	}
	infos := make([]agentapi.SessionInfo, 0, len(sessions))
	for _, s := range sessions {
		infos = append(infos, agentapi.SessionInfo{
			ID:           s.ID,
			Title:        s.DisplayTitle(),
			MessageCount: len(s.Messages),
			CWD:          s.CWD,
			Subagent:     s.IsSubagentJournal(),
		})
	}
	return infos, nil
}

// RenameSession assigns an explicit title to a session.
func (c *Core) RenameSession(sessionID, title string) error {
	sess, err := c.store.LoadByID(c.cwd, sessionID)
	if err != nil {
		return fmt.Errorf("host: rename session %q: %w", sessionID, err)
	}
	return sess.SetTitle(title)
}
