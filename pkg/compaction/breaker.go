package compaction

import (
	"context"
	"errors"
	"sync"

	"github.com/lcoder/lcoder/pkg/models"
)

// ErrCompactionSkipped is returned by a breaker-wrapped summarizer when the
// circuit is OPEN. Callers should treat it as a signal to fall back (e.g.
// truncate) rather than a fatal error.
var ErrCompactionSkipped = errors.New("compaction skipped: circuit breaker open")

// defaultMaxFailures is the number of consecutive failures that trips the breaker.
const defaultMaxFailures = 3

// CircuitBreaker guards the summarizer against repeated failures. It models
// three states via a consecutive-failure counter:
//
//	failures == 0            -> CLOSED   (healthy)
//	1 <= failures < max      -> HALF_OPEN (degraded, still attempting)
//	failures >= max          -> OPEN     (tripped, skip the inner call)
//
// A successful call resets the counter to CLOSED.
type CircuitBreaker struct {
	maxFailures int
	mu          sync.Mutex
	failures    int
}

// NewCircuitBreaker creates a breaker that trips after max consecutive failures.
// A non-positive max falls back to the default of 3.
func NewCircuitBreaker(max int) *CircuitBreaker {
	if max <= 0 {
		max = defaultMaxFailures
	}
	return &CircuitBreaker{maxFailures: max}
}

// Wrap returns a SummarizeFunc that short-circuits when the breaker is OPEN and
// otherwise delegates to inner, updating the failure counter from the outcome.
func (cb *CircuitBreaker) Wrap(inner SummarizeFunc) SummarizeFunc {
	return func(ctx context.Context, messages []models.AgentMessage, prior string) (string, error) {
		cb.mu.Lock()
		open := cb.failures >= cb.maxFailures
		cb.mu.Unlock()
		if open {
			return "", ErrCompactionSkipped
		}

		summary, err := inner(ctx, messages, prior)

		cb.mu.Lock()
		if err != nil {
			cb.failures++
		} else {
			cb.failures = 0
		}
		cb.mu.Unlock()
		return summary, err
	}
}

// Reset returns the breaker to the CLOSED state.
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	cb.failures = 0
	cb.mu.Unlock()
}
