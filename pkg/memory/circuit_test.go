package memory

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCircuitAllowsInitially(t *testing.T) {
	c := newCircuitBreaker(3, 100*time.Millisecond)
	require.True(t, c.allow())
}

func TestCircuitOpensAfterFailures(t *testing.T) {
	c := newCircuitBreaker(2, time.Hour)
	c.recordFailure()
	c.recordFailure()
	require.False(t, c.allow())
}

func TestCircuitClosesAfterTimeout(t *testing.T) {
	c := newCircuitBreaker(2, 50*time.Millisecond)
	c.recordFailure()
	c.recordFailure()
	require.False(t, c.allow())
	time.Sleep(60 * time.Millisecond)
	require.True(t, c.allow())
}

func TestCircuitClosesOnSuccess(t *testing.T) {
	c := newCircuitBreaker(2, time.Hour)
	c.recordFailure()
	c.recordFailure()
	c.recordSuccess()
	require.True(t, c.allow())
}

func TestCircuitOpenSinceFixed(t *testing.T) {
	c := newCircuitBreaker(2, 100*time.Millisecond)
	c.recordFailure()
	c.recordFailure()
	require.False(t, c.allow())

	// Wait less than the reset timeout, then record another failure.
	// In the buggy implementation this would reset openSince and extend the timeout.
	time.Sleep(60 * time.Millisecond)
	c.recordFailure()

	// After the original timeout has elapsed from the first open, traffic should flow again.
	time.Sleep(50 * time.Millisecond)
	require.True(t, c.allow())
}
