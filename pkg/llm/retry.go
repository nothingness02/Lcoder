package llm

import (
	"context"
	"errors"
	"io"
	"net"
	"time"

	"github.com/lcoder/lcoder/pkg/llm/provider"
	"github.com/lcoder/lcoder/pkg/models"
)

// retryableCode is the set of normalized engine error codes worth retrying:
// rate limits and transient internal/upstream failures. Client errors such as
// auth or bad_request will not be fixed by a retry.
var retryableCode = map[string]bool{
	"rate_limit": true,
	"internal":   true,
}

// IsRetryable reports whether a failed turn establishment is worth retrying.
// Context cancellation and deadline errors are never retryable; transient engine
// errors (by code) and network/EOF errors are.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var pe *provider.EventError
	if errors.As(err, &pe) {
		return retryableCode[pe.Code]
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

// RetryConfig controls turn-establishment retries.
type RetryConfig struct {
	MaxAttempts int
	BaseBackoff time.Duration
}

// DefaultRetryConfig retries up to 3 times with 1s/2s exponential backoff.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{MaxAttempts: 3, BaseBackoff: time.Second}
}

// StreamTurnRetry establishes a turn stream, retrying transient failures with
// exponential backoff. It only retries the establishment call (before any
// content has streamed), so a successful return yields a fresh, unread stream.
func (c *Client) StreamTurnRetry(ctx context.Context, req models.TurnRequest, rc RetryConfig) (<-chan provider.Event, error) {
	if rc.MaxAttempts < 1 {
		rc.MaxAttempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < rc.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, err
		}
		stream, err := c.StreamTurn(ctx, req)
		if err == nil {
			return stream, nil
		}
		lastErr = err
		if !IsRetryable(err) || attempt == rc.MaxAttempts-1 {
			return nil, err
		}
		backoff := rc.BaseBackoff << attempt
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, lastErr
		case <-timer.C:
		}
	}
	return nil, lastErr
}
