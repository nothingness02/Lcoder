package llm

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/llm/provider"
	"github.com/lcoder/lcoder/pkg/models"
)

func TestIsRetryable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"rate-limit", &provider.EventError{Code: "rate_limit"}, true},
		{"internal", &provider.EventError{Code: "internal"}, true},
		{"auth", &provider.EventError{Code: "auth"}, false},
		{"bad-request", &provider.EventError{Code: "bad_request"}, false},
		{"ctx-canceled", context.Canceled, false},
		{"ctx-deadline", context.DeadlineExceeded, false},
		{"eof", io.EOF, true},
		{"unexpected-eof", io.ErrUnexpectedEOF, true},
		{"generic", errors.New("boom"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsRetryable(tc.err); got != tc.want {
				t.Fatalf("IsRetryable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsRetryableNetError(t *testing.T) {
	var netErr net.Error = &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	if !IsRetryable(netErr) {
		t.Fatalf("network error should be retryable")
	}
}

func fastRetry() RetryConfig {
	return RetryConfig{MaxAttempts: 3, BaseBackoff: time.Millisecond}
}

func TestStreamTurnRetryRecoversAfterTransient(t *testing.T) {
	adapter := &fakeAdapter{
		errUntil: 2, errCode: "internal",
		events: []provider.Event{{Kind: provider.KindDone, Message: models.AssistantMessage("ok")}},
	}
	c := clientWithAdapter(adapter)
	stream, err := c.StreamTurnRetry(context.Background(), models.TurnRequest{}, fastRetry())
	if err != nil {
		t.Fatalf("expected success after transient failures, got %v", err)
	}
	// Drain the channel so the test doesn't leave it unread.
	for range stream {
	}
	if adapter.calls != 3 {
		t.Fatalf("expected 3 attempts, got %d", adapter.calls)
	}
}

func TestStreamTurnRetryGivesUpOnNonRetryable(t *testing.T) {
	adapter := &fakeAdapter{errUntil: 5, errCode: "bad_request"}
	c := clientWithAdapter(adapter)
	_, err := c.StreamTurnRetry(context.Background(), models.TurnRequest{}, fastRetry())
	if err == nil {
		t.Fatal("expected error for non-retryable code")
	}
	var pe *provider.EventError
	if !errors.As(err, &pe) || pe.Code != "bad_request" {
		t.Fatalf("expected provider.EventError bad_request, got %v", err)
	}
	if adapter.calls != 1 {
		t.Fatalf("expected 1 attempt (no retry), got %d", adapter.calls)
	}
}

func TestStreamTurnRetryExhaustsAttempts(t *testing.T) {
	adapter := &fakeAdapter{errUntil: 100, errCode: "internal"}
	c := clientWithAdapter(adapter)
	_, err := c.StreamTurnRetry(context.Background(), models.TurnRequest{}, fastRetry())
	if err == nil {
		t.Fatal("expected error after exhausting attempts")
	}
	if adapter.calls != 3 {
		t.Fatalf("expected 3 attempts, got %d", adapter.calls)
	}
}

func TestStreamTurnRetryRespectsContext(t *testing.T) {
	adapter := &fakeAdapter{errUntil: 100, errCode: "internal"}
	c := clientWithAdapter(adapter)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled

	_, err := c.StreamTurnRetry(ctx, models.TurnRequest{}, RetryConfig{MaxAttempts: 3, BaseBackoff: time.Hour})
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
}
