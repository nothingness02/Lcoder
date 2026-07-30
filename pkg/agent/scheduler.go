package agent

import (
	"context"

	"github.com/lcoder/lcoder/pkg/tools"
)

// batchScheduler orders one batch of tool calls by resource conflicts.
// Call i waits for every earlier call whose accesses conflict with its own;
// because conflicts chain through earlier indices, this reproduces the
// active+queued FIFO semantics of Kimi Code's ToolScheduler for a batch
// that is fully known upfront. One instance serves exactly one batch.
type batchScheduler struct {
	done  []chan struct{}
	waits [][]int // waits[i]: earlier indices call i must await
}

func newBatchScheduler(accesses [][]tools.ToolAccess) *batchScheduler {
	n := len(accesses)
	s := &batchScheduler{done: make([]chan struct{}, n), waits: make([][]int, n)}
	for i := 0; i < n; i++ {
		s.done[i] = make(chan struct{})
		for j := 0; j < i; j++ {
			if tools.AccessesConflict(accesses[i], accesses[j]) {
				s.waits[i] = append(s.waits[i], j)
			}
		}
	}
	return s
}

// addWait adds a non-resource ordering edge: call i must also await call j.
// Used for same-batch dedup of cacheable calls, where the duplicate must
// observe the original's cached result.
func (s *batchScheduler) addWait(i, j int) {
	s.waits[i] = append(s.waits[i], j)
}

// wait blocks until all calls that i depends on have finished, or ctx is done.
func (s *batchScheduler) wait(ctx context.Context, i int) error {
	for _, j := range s.waits[i] {
		select {
		case <-s.done[j]:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// finish marks call i complete, unblocking later calls that waited on it.
// Each index must be finished exactly once; the executor guarantees this via
// defer, including for short-circuited calls.
func (s *batchScheduler) finish(i int) {
	close(s.done[i])
}
