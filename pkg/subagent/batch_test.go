package subagent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/llm/provider"
)

// fakeLauncher records launch events and settles each attempt per its
// programmed behavior.
type fakeLauncher struct {
	mu      sync.Mutex
	started []string // prompt of each launched attempt, in order

	// rateLimited is the set of spawn prompts whose FIRST attempt fails with
	// a rate-limit error; the retry (via Resume) succeeds.
	rateLimited map[string]bool
	// fail is the set of spawn prompts that fail outright (non-rate-limit).
	fail map[string]bool
	// block spawns until release is closed (for cancellation tests).
	release chan struct{}
	// resumeOut is the outcome returned for every Resume call.
	resumeOut *Outcome
}

func (f *fakeLauncher) Spawn(ctx context.Context, task BatchTask) *Outcome {
	f.mu.Lock()
	f.started = append(f.started, task.Prompt)
	rateLimited := f.rateLimited[task.Prompt]
	fail := f.fail[task.Prompt]
	release := f.release
	f.mu.Unlock()

	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return &Outcome{
				AgentID:  "agent-" + task.Prompt,
				TimedOut: ctx.Err() == context.DeadlineExceeded,
				Canceled: ctx.Err() != context.DeadlineExceeded,
			}
		}
	}
	switch {
	case rateLimited:
		return &Outcome{AgentID: "agent-" + task.Prompt, Err: rateLimitErr()}
	case fail:
		return &Outcome{AgentID: "agent-" + task.Prompt, Err: errors.New("boom")}
	default:
		return &Outcome{AgentID: "agent-" + task.Prompt, Summary: "ok: " + task.Prompt}
	}
}

func (f *fakeLauncher) Resume(ctx context.Context, agentID, _ string) *Outcome {
	f.mu.Lock()
	f.started = append(f.started, "resume:"+agentID)
	release := f.release
	f.mu.Unlock()
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return &Outcome{
				AgentID:  agentID,
				TimedOut: ctx.Err() == context.DeadlineExceeded,
				Canceled: ctx.Err() != context.DeadlineExceeded,
			}
		}
	}
	if f.resumeOut != nil {
		return f.resumeOut
	}
	return &Outcome{AgentID: agentID, Summary: "resumed " + agentID}
}

func rateLimitErr() error {
	return &provider.EventError{Code: "rate_limit", Message: "429 too many requests"}
}

func task(prompt string) BatchTask {
	return BatchTask{Prompt: prompt}
}

func tasks(n int) []BatchTask {
	out := make([]BatchTask, n)
	for i := range out {
		out[i] = task(fmt.Sprintf("t%d", i))
	}
	return out
}

func statusOf(results []BatchResult) []BatchStatus {
	out := make([]BatchStatus, len(results))
	for i, r := range results {
		out[i] = r.Status
	}
	return out
}

func TestBatchNormalRampCompletesAll(t *testing.T) {
	l := &fakeLauncher{}
	b := &Batch{Launcher: l}

	results := b.Run(context.Background(), tasks(3))
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	for i, r := range results {
		if r.Status != StatusCompleted || r.Summary != "ok: t"+string(rune('0'+i)) {
			t.Fatalf("result[%d] = %+v, want completed", i, r)
		}
	}
	if len(l.started) != 3 {
		t.Fatalf("expected 3 launches, got %v", l.started)
	}
}

func TestBatchLaunchRampTiming(t *testing.T) {
	l := &fakeLauncher{}
	b := &Batch{Launcher: l}

	start := time.Now()
	_ = b.Run(context.Background(), tasks(8))
	// First 5 launch immediately; the remaining 3 are spaced by the ramp.
	if len(l.started) != 8 {
		t.Fatalf("expected 8 launches, got %d", len(l.started))
	}
	// At least one launch must have been delayed by ~700ms (the ramp gap),
	// so the whole batch cannot finish in the burst window.
	if time.Since(start) < initialLaunchInterval/2 {
		t.Fatalf("batch finished too fast (%v): ramp should throttle the tail", time.Since(start))
	}
}

func TestBatchRateLimitRequeuesAndResumes(t *testing.T) {
	l := &fakeLauncher{
		rateLimited: map[string]bool{"t1": true, "t2": true},
	}
	var suspended []string
	b := &Batch{
		Launcher: l,
		Suspend: func(agentID, reason string) {
			suspended = append(suspended, agentID)
		},
	}

	results := b.Run(context.Background(), tasks(3))
	if len(suspended) != 2 {
		t.Fatalf("expected 2 suspend notifications, got %v", suspended)
	}
	// t1 and t2 were requeued: their retries went through Resume (journal).
	resumed := 0
	for _, s := range l.started {
		if len(s) > 7 && s[:7] == "resume:" {
			resumed++
		}
	}
	if resumed != 2 {
		t.Fatalf("expected 2 rate-limit retries via resume, got launches %v", l.started)
	}
	for _, st := range statusOf(results) {
		if st != StatusCompleted {
			t.Fatalf("all tasks should complete after recovery, got %v", statusOf(results))
		}
	}
}

func TestBatchRateLimitSingleTaskFails(t *testing.T) {
	// A rate-limited task that is the only unfinished one fails instead of
	// requeueing forever.
	l := &fakeLauncher{rateLimited: map[string]bool{"t0": true}}
	b := &Batch{Launcher: l}

	results := b.Run(context.Background(), tasks(1))
	if results[0].Status != StatusFailed {
		t.Fatalf("solo rate-limited task should fail, got %+v", results[0])
	}
}

func TestBatchFailureIsolated(t *testing.T) {
	l := &fakeLauncher{fail: map[string]bool{"t1": true}}
	b := &Batch{Launcher: l}

	results := b.Run(context.Background(), tasks(3))
	if results[1].Status != StatusFailed || results[1].Error != "boom" {
		t.Fatalf("t1 should fail, got %+v", results[1])
	}
	if results[0].Status != StatusCompleted || results[2].Status != StatusCompleted {
		t.Fatalf("other tasks must still complete, got %v", statusOf(results))
	}
}

func TestBatchCancellationDistinguishesStarted(t *testing.T) {
	release := make(chan struct{})
	l := &fakeLauncher{release: release}
	b := &Batch{Launcher: l}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan []BatchResult, 1)
	go func() { done <- b.Run(ctx, tasks(6)) }()

	// Wait until the burst of 5 has launched and is blocked.
	deadline := time.Now().Add(2 * time.Second)
	for {
		l.mu.Lock()
		n := len(l.started)
		l.mu.Unlock()
		if n >= 5 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("burst did not launch in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	results := <-done

	started, notStarted := 0, 0
	for _, r := range results {
		if r.Status != StatusAborted {
			t.Fatalf("all should be aborted on cancel, got %+v", r)
		}
		switch r.State {
		case StateStarted:
			started++
		case StateNotStarted:
			notStarted++
		}
	}
	if started < 5 {
		t.Fatalf("expected >=5 started (burst launched), got %d", started)
	}
	if notStarted < 1 {
		t.Fatalf("expected at least one not_started (beyond the burst), got %d", notStarted)
	}
	close(release) // unblock launchers so the test exits cleanly
}

func TestBatchMaxConcurrency(t *testing.T) {
	release := make(chan struct{})
	l := &fakeLauncher{release: release}
	b := &Batch{Launcher: l, MaxConcurrency: 2}

	done := make(chan []BatchResult, 1)
	go func() { done <- b.Run(context.Background(), tasks(6)) }()

	// The cap keeps in-flight attempts at 2: wait, then count started while
	// blocked (all launched tasks are stuck on release).
	deadline := time.Now().Add(2 * time.Second)
	for {
		l.mu.Lock()
		n := len(l.started)
		l.mu.Unlock()
		if n >= 2 {
			// Give the scheduler a moment to (incorrectly) launch more.
			time.Sleep(300 * time.Millisecond)
			l.mu.Lock()
			n = len(l.started)
			l.mu.Unlock()
			if n != 2 {
				t.Fatalf("expected exactly 2 in-flight under the cap, got %d", n)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cap did not launch in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(release)
	<-done
}

func TestBatchMixedResumeAndSpawn(t *testing.T) {
	l := &fakeLauncher{resumeOut: &Outcome{AgentID: "agent-old", Summary: "resumed work"}}
	b := &Batch{Launcher: l}

	results := b.Run(context.Background(), []BatchTask{
		task("fresh"),
		{Prompt: "continue", ResumeID: "agent-old"},
		task("fresh2"),
	})
	if results[0].Status != StatusCompleted || results[2].Status != StatusCompleted {
		t.Fatalf("spawn tasks should complete, got %v", statusOf(results))
	}
	if results[1].Summary != "resumed work" || results[1].AgentID != "agent-old" {
		t.Fatalf("resume task malformed: %+v", results[1])
	}
}

func TestBatchTaskTimeout(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	l := &fakeLauncher{release: release}
	b := &Batch{Launcher: l}

	tks := []BatchTask{{Prompt: "slow", Timeout: 50 * time.Millisecond}}
	results := b.Run(context.Background(), tks)
	if results[0].Status != StatusAborted || !contains(results[0].Error, "timed out") {
		t.Fatalf("timed-out task should be aborted, got %+v", results[0])
	}
}

func TestBatchEnvConcurrencyOverride(t *testing.T) {
	t.Setenv(MaxConcurrencyEnv, "3")
	release := make(chan struct{})
	l := &fakeLauncher{release: release}
	b := &Batch{Launcher: l}

	done := make(chan []BatchResult, 1)
	go func() { done <- b.Run(context.Background(), tasks(6)) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		l.mu.Lock()
		n := len(l.started)
		l.mu.Unlock()
		if n >= 3 {
			time.Sleep(200 * time.Millisecond)
			l.mu.Lock()
			n = len(l.started)
			l.mu.Unlock()
			if n != 3 {
				t.Fatalf("env cap 3 should hold, got %d in-flight", n)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("env cap did not launch in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(release)
	<-done
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
