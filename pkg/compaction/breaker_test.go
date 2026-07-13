package compaction

import (
	"context"
	"errors"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

func okSummarizer(string) SummarizeFunc {
	return func(context.Context, []models.AgentMessage) (string, error) { return "ok", nil }
}

func failSummarizer() SummarizeFunc {
	return func(context.Context, []models.AgentMessage) (string, error) { return "", errors.New("boom") }
}

func TestCircuitBreakerTripsAfterMaxFailures(t *testing.T) {
	cb := NewCircuitBreaker(3)
	var calls int
	inner := func(context.Context, []models.AgentMessage) (string, error) {
		calls++
		return "", errors.New("boom")
	}
	wrapped := cb.Wrap(inner)

	// First 3 calls reach inner and fail (CLOSED -> HALF_OPEN -> OPEN).
	for i := 0; i < 3; i++ {
		if _, err := wrapped(context.Background(), nil); err == nil {
			t.Fatalf("call %d: expected error", i)
		}
	}
	if calls != 3 {
		t.Fatalf("expected 3 inner calls before trip, got %d", calls)
	}

	// Now OPEN: inner must not be called, ErrCompactionSkipped returned.
	if _, err := wrapped(context.Background(), nil); !errors.Is(err, ErrCompactionSkipped) {
		t.Fatalf("expected ErrCompactionSkipped when open, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("inner should not be called when open, got %d calls", calls)
	}
}

func TestCircuitBreakerSuccessResets(t *testing.T) {
	cb := NewCircuitBreaker(3)
	fail := cb.Wrap(failSummarizer())
	ok := cb.Wrap(okSummarizer(""))

	// Two failures (HALF_OPEN), then a success resets to CLOSED.
	_, _ = fail(context.Background(), nil)
	_, _ = fail(context.Background(), nil)
	if _, err := ok(context.Background(), nil); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	// After reset, the breaker tolerates failures again without tripping early.
	if _, err := fail(context.Background(), nil); err == nil {
		t.Fatal("expected failure error, not a skip")
	}
	if _, err := fail(context.Background(), nil); err == nil {
		t.Fatal("expected failure error, not a skip")
	}
	// Still not open after 2 post-reset failures.
	if _, err := ok(context.Background(), nil); err != nil {
		t.Fatalf("expected success (not open), got %v", err)
	}
}

func TestCircuitBreakerReset(t *testing.T) {
	cb := NewCircuitBreaker(2)
	fail := cb.Wrap(failSummarizer())
	_, _ = fail(context.Background(), nil)
	_, _ = fail(context.Background(), nil)
	// Open now.
	if _, err := fail(context.Background(), nil); !errors.Is(err, ErrCompactionSkipped) {
		t.Fatalf("expected open, got %v", err)
	}
	cb.Reset()
	// Closed again: inner is reached and returns its own error.
	if _, err := fail(context.Background(), nil); errors.Is(err, ErrCompactionSkipped) {
		t.Fatal("expected inner error after reset, got skip")
	}
}

func TestCircuitBreakerDefaultMax(t *testing.T) {
	cb := NewCircuitBreaker(0)
	fail := cb.Wrap(failSummarizer())
	for i := 0; i < defaultMaxFailures; i++ {
		if _, err := fail(context.Background(), nil); errors.Is(err, ErrCompactionSkipped) {
			t.Fatalf("tripped too early at call %d", i)
		}
	}
	if _, err := fail(context.Background(), nil); !errors.Is(err, ErrCompactionSkipped) {
		t.Fatalf("expected trip after default max, got %v", err)
	}
}
