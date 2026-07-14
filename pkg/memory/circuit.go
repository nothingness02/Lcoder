package memory

import (
	"sync"
	"time"
)

type circuitBreaker struct {
	mu               sync.Mutex
	consecutiveFails int
	threshold        int
	openSince        time.Time
	resetTimeout     time.Duration
}

func newCircuitBreaker(threshold int, resetTimeout time.Duration) *circuitBreaker {
	if threshold <= 0 {
		threshold = 3
	}
	if resetTimeout <= 0 {
		resetTimeout = 30 * time.Second
	}
	return &circuitBreaker{threshold: threshold, resetTimeout: resetTimeout}
}

func (c *circuitBreaker) allow() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.consecutiveFails < c.threshold {
		return true
	}
	if time.Since(c.openSince) >= c.resetTimeout {
		c.consecutiveFails = 0
		return true
	}
	return false
}

func (c *circuitBreaker) recordSuccess() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.consecutiveFails = 0
}

func (c *circuitBreaker) recordFailure() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.consecutiveFails++
	if c.consecutiveFails >= c.threshold {
		c.openSince = time.Now()
	}
}
