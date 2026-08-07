package llm

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
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

// IsRateLimited reports whether err is a provider rate-limit rejection
// (normalized engine code "rate_limit", e.g. HTTP 429 or an SSE
// overloaded_error / rate_limit_error event). It is the trigger for the
// subagent batch scheduler's coordinated recovery: by the time a rate-limit
// error escapes the per-turn gateway retries (StreamTurnRetry + the
// streamer's whole-turn retry), it represents a persistent or aggregate limit
// that needs capacity management, not another immediate retry.
func IsRateLimited(err error) bool {
	if err == nil {
		return false
	}
	var pe *provider.EventError
	return errors.As(err, &pe) && pe.Code == "rate_limit"
}

// RateLimitRetryAfter returns the provider-requested wait carried by a
// rate-limit error, or 0 when absent/none.
func RateLimitRetryAfter(err error) time.Duration {
	var pe *provider.EventError
	if errors.As(err, &pe) && pe.Code == "rate_limit" {
		return pe.RetryAfter
	}
	return 0
}

// maxRetryAfterHonor caps how long a provider-requested Retry-After wait is
// honored; computed backoff is capped at RetryConfig.MaxBackoff instead.
const maxRetryAfterHonor = 2 * time.Minute

// RetryConfig controls turn-establishment retries.
type RetryConfig struct {
	MaxAttempts int
	BaseBackoff time.Duration
	// MaxBackoff caps the computed exponential backoff (jitter included).
	// Zero means 32s.
	MaxBackoff time.Duration
}

// DefaultRetryConfig retries up to 3 times with 1s/2s exponential backoff.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{MaxAttempts: 3, BaseBackoff: time.Second, MaxBackoff: 32 * time.Second}
}

// Backoff computes the wait before the next attempt. A provider-supplied
// RetryAfter always wins (capped at maxRetryAfterHonor); otherwise exponential
// backoff with ±25% jitter, capped at MaxBackoff.
func Backoff(rc RetryConfig, attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return min(retryAfter, maxRetryAfterHonor)
	}
	capDur := rc.MaxBackoff
	if capDur <= 0 {
		capDur = 32 * time.Second
	}
	d := rc.BaseBackoff << attempt
	if d <= 0 || d > capDur {
		d = capDur
	}
	// ±25% jitter around the (capped) value; never above the cap.
	jitter := time.Duration(float64(d) * (0.75 + 0.5*rand.Float64()))
	return min(jitter, capDur)
}

// retryAfterOf extracts the provider-requested wait from an error, if any.
func retryAfterOf(err error) time.Duration {
	var pe *provider.EventError
	if errors.As(err, &pe) {
		return pe.RetryAfter
	}
	return 0
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
		backoff := Backoff(rc, attempt, retryAfterOf(lastErr))
		if c.OnRetry != nil {
			c.OnRetry("establish", attempt+1, backoff, lastErr)
		}
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
