package agent

import (
	"context"
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/tools"
)

func writeAccess(p string) []tools.ToolAccess {
	return []tools.ToolAccess{{Op: tools.OpWrite, Path: p}}
}

func TestSchedulerIndependentCallDoesNotWait(t *testing.T) {
	s := newBatchScheduler([][]tools.ToolAccess{writeAccess("/w/a.go"), writeAccess("/w/b.go")})
	done := make(chan error, 1)
	go func() { done <- s.wait(context.Background(), 1) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("wait: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("call 1 must not wait for non-conflicting call 0")
	}
}

func TestSchedulerConflictingCallWaits(t *testing.T) {
	s := newBatchScheduler([][]tools.ToolAccess{writeAccess("/w/a.go"), writeAccess("/w/a.go")})
	done := make(chan error, 1)
	go func() { done <- s.wait(context.Background(), 1) }()
	select {
	case <-done:
		t.Fatal("conflicting call returned before finish(0)")
	case <-time.After(100 * time.Millisecond):
	}
	s.finish(0)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("wait: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("call 1 still blocked after finish(0)")
	}
}

// FIFO: [read x, write x, read x] —— call 2 与 call 0 不冲突,但与排在
// 前面的 call 1 冲突,因此必须等 call 1(不允许插队)。
func TestSchedulerFIFOChain(t *testing.T) {
	read := []tools.ToolAccess{{Op: tools.OpRead, Path: "/w/x"}}
	write := writeAccess("/w/x")
	s := newBatchScheduler([][]tools.ToolAccess{read, write, read})
	s.finish(0)
	done := make(chan error, 1)
	go func() { done <- s.wait(context.Background(), 2) }()
	select {
	case <-done:
		t.Fatal("call 2 must wait for earlier conflicting call 1 even after finish(0)")
	case <-time.After(100 * time.Millisecond):
	}
	s.finish(1)
	if err := <-done; err != nil {
		t.Fatalf("wait: %v", err)
	}
}

func TestSchedulerWaitContextCancel(t *testing.T) {
	s := newBatchScheduler([][]tools.ToolAccess{writeAccess("/w/a"), writeAccess("/w/a")})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.wait(ctx, 1); err == nil {
		t.Fatal("expected context error for canceled wait")
	}
}

func TestSchedulerAddWait(t *testing.T) {
	// 两个 read 不冲突,但 addWait 显式加边后 call 1 必须等 call 0。
	read := []tools.ToolAccess{{Op: tools.OpRead, Path: "/w/a"}}
	s := newBatchScheduler([][]tools.ToolAccess{read, read})
	s.addWait(1, 0)
	done := make(chan error, 1)
	go func() { done <- s.wait(context.Background(), 1) }()
	select {
	case <-done:
		t.Fatal("addWait edge not honored")
	case <-time.After(100 * time.Millisecond):
	}
	s.finish(0)
	if err := <-done; err != nil {
		t.Fatalf("wait: %v", err)
	}
}
