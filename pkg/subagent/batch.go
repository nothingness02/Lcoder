// batch.go implements the coordinated subagent batch scheduler, ported from
// kimi-code's AgentRunBatch (packages/agent-core-v2/src/session/swarm/
// agentRunBatch.ts). It runs a list of spawn/resume tasks with a
// burst-then-throttle launch ramp, and on provider rate limits switches to a
// coordinated recovery mode: exponential-backoff requeues (reusing each
// agent's journal), a dynamically shrinking capacity, and a global launch
// throttle so a batch of subagents cannot keep hammering the same provider.
//
// The scheduler is pure: it drives a Launcher (spawn/resume) and reports
// back ordered results. It owns no agent state beyond its own bookkeeping,
// so it is easy to unit-test with a fake launcher.
package subagent

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/lcoder/lcoder/pkg/llm"
)

// Batch scheduling constants (aligned with agentRunBatch.ts).
const (
	// initialLaunchLimit is how many tasks launch immediately on entry.
	initialLaunchLimit = 5
	// initialLaunchInterval is the gap between subsequent normal-mode
	// launches after the burst (burst-then-throttle ramp).
	initialLaunchInterval = 700 * time.Millisecond
	// rateLimitRetryBase is the exponential-backoff base for requeued tasks.
	rateLimitRetryBase = 3 * time.Second
	// rateLimitRetryFactor multiplies the backoff per retry.
	rateLimitRetryFactor = 2
	// capacityShrinkInterval shrinks the rate-limit concurrency cap by one
	// every interval (floor 1), backing the load off the provider.
	capacityShrinkInterval = 2 * time.Second
	// capacityRecoveryInterval grows the cap by one again, probing whether
	// the provider has recovered.
	capacityRecoveryInterval = 3 * time.Minute
	// rateLimitSuspendedReason is the requeue notification sent to Suspend.
	rateLimitSuspendedReason = "Provider rate limit; subagent requeued for retry."
)

// MaxConcurrencyEnv overrides the batch concurrency cap when set.
const MaxConcurrencyEnv = "LC_AGENT_SWARM_MAX_CONCURRENCY"

// BatchTask is one unit of work: a fresh spawn or a resume of an existing
// agent. ResumeID non-empty selects the resume path.
type BatchTask struct {
	Profile   Agent
	Prompt    string
	SwarmItem string        // resume relabeling / display; empty = not a swarm item
	ResumeID  string        // non-empty = resume this agent instead of spawning
	Timeout   time.Duration // per-attempt ceiling; 0 = none (Host applies profile timeout)
}

// BatchStatus is the terminal status of one task.
type BatchStatus string

const (
	StatusCompleted BatchStatus = "completed"
	StatusFailed    BatchStatus = "failed"
	StatusAborted   BatchStatus = "aborted"
)

// BatchState distinguishes aborted tasks that had started (consumed a turn)
// from those that never launched.
type BatchState string

const (
	StateStarted    BatchState = "started"
	StateNotStarted BatchState = "not_started"
)

// BatchResult is the settled outcome of one task.
type BatchResult struct {
	Index   int
	AgentID string
	Status  BatchStatus
	State   BatchState
	Summary string
	Error   string
}

// Launcher is the boundary between the batch scheduler and the agent host.
// Spawn runs a fresh subagent; Resume continues an existing one. A fake
// launcher drives scheduler tests without any LLM.
type Launcher interface {
	Spawn(ctx context.Context, task BatchTask) *Outcome
	Resume(ctx context.Context, agentID, prompt string) *Outcome
}

// Batch is a one-shot scheduler: call Run exactly once.
type Batch struct {
	Launcher Launcher
	// MaxConcurrency caps in-flight attempts; 0 = no hard cap (the launch
	// ramp alone governs the burst). Overridable via LC_AGENT_SWARM_MAX_CONCURRENCY.
	MaxConcurrency int
	// Suspend, when set, is notified with the agent id and reason each time
	// a task is requeued for a rate-limit retry.
	Suspend func(agentID, reason string)
}

// attemptDone carries a completed attempt back to the scheduler loop.
type attemptDone struct {
	state *attemptState
	out   *Outcome
}

// attemptState is the bookkeeping for one task through its (possibly
// multiple) attempts.
type attemptState struct {
	task         BatchTask
	index        int
	retryCount   int
	retryReadyAt time.Time
	retryAgentID string // journal agent id reused by rate-limit retries
	started      bool   // an attempt launched (approximated as "turn consumed")
}

// batch is the running state of one Run.
type batch struct {
	*Batch
	bctx    context.Context
	results []*BatchResult
	pending []*attemptState
	states  []*attemptState
	active  int

	normalLaunchCount int
	rateLimitMode     bool
	rateLimitCapacity int
	globalRetry       time.Duration
	nextRateLimitAt   time.Time
	lastRateLimitAt   time.Time
	lastShrinkAt      time.Time
	lastRecoveryAt    time.Time
	startedSuccess    int
}

// Run executes tasks to completion (or batch cancellation) and returns
// ordered results. ctx cancellation aborts in-flight attempts; tasks that had
// started are reported aborted/started, the rest aborted/not_started.
func (b *Batch) Run(ctx context.Context, tasks []BatchTask) []BatchResult {
	bctx, cancel := context.WithCancel(ctx)
	defer cancel()

	states := make([]*attemptState, len(tasks))
	pending := make([]*attemptState, 0, len(tasks))
	for i, t := range tasks {
		st := &attemptState{task: t, index: i}
		states[i] = st
		pending = append(pending, st)
	}
	bb := &batch{
		Batch:       b,
		bctx:        bctx,
		results:     make([]*BatchResult, len(tasks)),
		pending:     pending,
		states:      states,
		globalRetry: rateLimitRetryBase,
	}
	if bb.MaxConcurrency <= 0 {
		if v := envInt(MaxConcurrencyEnv); v > 0 {
			bb.MaxConcurrency = v
		}
	}

	resultCh := make(chan attemptDone, len(tasks))

	burstDone := false
	for {
		if !burstDone {
			bb.burstLaunch(resultCh)
			burstDone = true
		}
		if bb.settled() {
			break
		}
		if err := ctx.Err(); err != nil {
			bb.abortRemaining(err)
			break
		}

		// next is the earliest future event: a ramp gap, a rate-limit retry
		// point, or a capacity change. Zero means nothing is scheduled — wait
		// on results only (the long timer never fires meaningfully).
		next := bb.nextWake()
		wait := time.Hour
		if !next.IsZero() {
			if d := time.Until(next); d > 0 {
				wait = d
			}
		}
		timer := time.NewTimer(wait)
		select {
		case done := <-resultCh:
			bb.handleDone(done)
		case <-timer.C:
			bb.onTick(resultCh)
		case <-bctx.Done():
		}
		timer.Stop()
	}
	out := make([]BatchResult, len(bb.results))
	for i, r := range bb.results {
		out[i] = *r
	}
	return out
}

// burstLaunch fires the initial burst: up to initialLaunchLimit launches
// immediately (normal mode; a batch cannot be in rate-limit mode yet).
func (b *batch) burstLaunch(resultCh chan attemptDone) {
	for b.normalLaunchCount < initialLaunchLimit && len(b.pending) > 0 && !b.atCap() {
		st := b.pending[0]
		b.pending = b.pending[1:]
		b.start(st, resultCh)
		b.normalLaunchCount++
	}
}

// nextWake returns when the next launch (or capacity change) should happen.
func (b *batch) nextWake() time.Time {
	if b.rateLimitMode {
		return b.rateLimitWake()
	}
	if len(b.pending) == 0 || b.atCap() {
		return time.Time{}
	}
	return time.Now().Add(initialLaunchInterval)
}

// onTick runs on a timer fire: launch per the current mode.
func (b *batch) onTick(resultCh chan attemptDone) {
	if b.rateLimitMode {
		b.rateLimitLaunch(resultCh)
		return
	}
	if len(b.pending) > 0 && !b.atCap() {
		st := b.pending[0]
		b.pending = b.pending[1:]
		b.start(st, resultCh)
		b.normalLaunchCount++
	}
}

// rateLimitWake returns the next rate-limit-mode event: capacity recovery,
// a ready pending retry, or the global throttle.
func (b *batch) rateLimitWake() time.Time {
	now := time.Now()
	b.recoverCapacity(now)
	b.shrinkCapacity(now)

	nextCapacity := b.nextCapacityRecoveryAt()
	if b.active >= b.rateLimitCapacity {
		return nextCapacity
	}
	next := b.nextPendingReadyAt()
	if b.nextRateLimitAt.After(next) {
		next = b.nextRateLimitAt
	}
	if !nextCapacity.IsZero() && nextCapacity.Before(next) {
		next = nextCapacity
	}
	return next
}

// rateLimitLaunch launches every currently eligible pending task under the
// capacity and global-throttle constraints.
func (b *batch) rateLimitLaunch(resultCh chan attemptDone) {
	now := time.Now()
	b.recoverCapacity(now)
	b.shrinkCapacity(now)
	for {
		if b.active >= b.rateLimitCapacity || len(b.pending) == 0 {
			return
		}
		if b.nextRateLimitAt.After(now) {
			return
		}
		idx := -1
		for i, st := range b.pending {
			if !st.retryReadyAt.After(now) {
				idx = i
				break
			}
		}
		if idx < 0 {
			return
		}
		st := b.pending[idx]
		b.pending = append(b.pending[:idx], b.pending[idx+1:]...)
		b.start(st, resultCh)
		b.nextRateLimitAt = now.Add(b.globalRetry)
	}
}

// start launches one attempt in its own goroutine.
func (b *batch) start(st *attemptState, resultCh chan attemptDone) {
	b.active++
	st.started = true
	go func() {
		out := b.runAttempt(st)
		select {
		case resultCh <- attemptDone{state: st, out: out}:
		case <-b.bctx.Done():
		}
	}()
}

// runAttempt executes the task once: a rate-limit retry resumes the journal
// agent (reusing partial progress), an explicit resume targets ResumeID, and
// anything else spawns fresh. An optional task timeout wraps the attempt
// context.
func (b *batch) runAttempt(st *attemptState) *Outcome {
	attemptCtx := b.bctx
	cancel := func() {}
	if st.task.Timeout > 0 {
		attemptCtx, cancel = context.WithTimeout(b.bctx, st.task.Timeout)
	}
	defer cancel()
	switch {
	case st.retryAgentID != "":
		// Rate-limit retry: the failed turn produced no usable content, so
		// resuming the journal with a continuation prompt is safe. (An exact
		// prompt would duplicate the user message already persisted in the
		// journal.)
		return b.Launcher.Resume(attemptCtx, st.retryAgentID, "Continue and finish the original task: "+st.task.Prompt)
	case st.task.ResumeID != "":
		return b.Launcher.Resume(attemptCtx, st.task.ResumeID, st.task.Prompt)
	default:
		return b.Launcher.Spawn(attemptCtx, st.task)
	}
}

// handleDone settles a completed attempt: success → completed; rate limit →
// requeue (unless it is the only unfinished task, which then fails); any
// other failure → failed.
func (b *batch) handleDone(done attemptDone) {
	b.active--
	if b.settled() {
		return
	}
	out := done.out
	state := done.state
	switch {
	case out != nil && out.Err == nil && !out.TimedOut && !out.Canceled:
		b.startedSuccess++
		b.results[state.index] = &BatchResult{
			Index:   state.index,
			AgentID: out.AgentID,
			Status:  StatusCompleted,
			Summary: out.Summary,
		}
	case llm.IsRateLimited(errOf(out)) && !b.isOnlyUnfinished(state):
		b.requeueRateLimited(state, out)
	default:
		b.results[state.index] = failedResult(state, out)
	}
}

// requeueRateLimited re-queues a rate-limited task with exponential backoff,
// enters rate-limit mode, and throttles the global launch rate.
func (b *batch) requeueRateLimited(state *attemptState, out *Outcome) {
	state.retryAgentID = out.AgentID
	state.retryCount++
	delay := rateLimitRetryBase
	for i := 1; i < state.retryCount; i++ {
		delay *= rateLimitRetryFactor
	}
	state.retryReadyAt = time.Now().Add(delay)
	b.pending = append(b.pending, state)

	now := time.Now()
	b.lastRateLimitAt = now
	if !b.rateLimitMode {
		b.rateLimitMode = true
		b.rateLimitCapacity = max(1, b.startedSuccess)
		b.globalRetry = rateLimitRetryBase
		b.shrinkCapacity(now)
	}
	if !state.started {
		b.globalRetry *= 2
	}
	if b.nextRateLimitAt.Before(now.Add(rateLimitRetryBase)) {
		b.nextRateLimitAt = now.Add(rateLimitRetryBase)
	}
	if b.Suspend != nil {
		b.Suspend(out.AgentID, rateLimitSuspendedReason)
	}
}

// shrinkCapacity decrements the rate-limit capacity every
// capacityShrinkInterval (floor 1).
func (b *batch) shrinkCapacity(now time.Time) {
	if !b.lastShrinkAt.IsZero() && now.Sub(b.lastShrinkAt) < capacityShrinkInterval {
		return
	}
	b.rateLimitCapacity = max(1, b.rateLimitCapacity-1)
	b.lastShrinkAt = now
}

// recoverCapacity grows the capacity by one every capacityRecoveryInterval.
func (b *batch) recoverCapacity(now time.Time) {
	next := b.nextCapacityRecoveryAt()
	if next.IsZero() || next.After(now) {
		return
	}
	b.rateLimitCapacity++
	b.lastRecoveryAt = now
	if b.nextRateLimitAt.After(now) {
		b.nextRateLimitAt = now
	}
}

func (b *batch) nextCapacityRecoveryAt() time.Time {
	if len(b.pending) == 0 || b.lastRateLimitAt.IsZero() {
		return time.Time{}
	}
	latest := b.lastRateLimitAt
	if b.lastRecoveryAt.After(latest) {
		latest = b.lastRecoveryAt
	}
	return latest.Add(capacityRecoveryInterval)
}

func (b *batch) nextPendingReadyAt() time.Time {
	var next time.Time
	for _, st := range b.pending {
		if next.IsZero() || st.retryReadyAt.Before(next) {
			next = st.retryReadyAt
		}
	}
	return next
}

func (b *batch) atCap() bool {
	return b.MaxConcurrency > 0 && b.active >= b.MaxConcurrency
}

func (b *batch) settled() bool {
	for _, r := range b.results {
		if r == nil {
			return false
		}
	}
	return true
}

func (b *batch) isOnlyUnfinished(state *attemptState) bool {
	for i, r := range b.results {
		if i == state.index {
			continue
		}
		if r == nil {
			return false
		}
	}
	return true
}

// abortRemaining marks every unsettled task aborted, distinguishing tasks
// that had started from those that never launched.
func (b *batch) abortRemaining(err error) {
	for _, st := range b.states {
		if b.results[st.index] != nil {
			continue
		}
		res := &BatchResult{
			Index:  st.index,
			Status: StatusAborted,
			Error:  abortMessage(err),
		}
		if st.started {
			res.State = StateStarted
			res.AgentID = st.retryAgentID
		} else {
			res.State = StateNotStarted
		}
		b.results[st.index] = res
	}
}

func abortMessage(err error) string {
	if err == nil {
		return "batch cancelled"
	}
	return "batch cancelled: " + err.Error()
}

func failedResult(state *attemptState, out *Outcome) *BatchResult {
	res := &BatchResult{
		Index:  state.index,
		Status: StatusFailed,
		State:  StateStarted,
	}
	if out == nil {
		res.Error = "unknown failure"
		return res
	}
	res.AgentID = out.AgentID
	switch {
	case out.TimedOut:
		res.Status = StatusAborted
		res.Error = "Subagent timed out."
	case out.Canceled:
		res.Status = StatusAborted
		res.Error = "Subagent cancelled."
	case out.Err != nil:
		res.Error = out.Err.Error()
	}
	return res
}

func errOf(out *Outcome) error {
	if out == nil {
		return nil
	}
	return out.Err
}

// envInt parses a positive integer from an environment variable; 0 on
// absence or parse failure.
func envInt(key string) int {
	raw := os.Getenv(key)
	if raw == "" {
		return 0
	}
	var v int
	if _, err := fmt.Sscanf(raw, "%d", &v); err != nil {
		return 0
	}
	return v
}
