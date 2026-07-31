package llm

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/llm/catalog"
	"github.com/lcoder/lcoder/pkg/llm/engine"
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
		{"context-overflow", &provider.EventError{Code: "context_overflow"}, false},
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

func TestStreamTurnRetryRetriesOn429(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"slow down"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\ndata: [DONE]\n\n"))
	}))
	t.Cleanup(srv.Close)

	eng := engine.New(catalog.New(catalog.Options{Refresh: false}))
	eng.RegisterProvider("openai", provider.Conn{BaseURL: srv.URL, Route: "openai"})
	c := NewClient(eng)
	stream, err := c.StreamTurnRetry(context.Background(), models.TurnRequest{
		Model: models.ModelRef{Provider: "openai", ID: "gpt-4o"},
	}, RetryConfig{MaxAttempts: 3, BaseBackoff: time.Millisecond})
	if err != nil {
		t.Fatalf("retry should recover from 429: %v", err)
	}
	for range stream {
	}
	if calls.Load() != 2 {
		t.Fatalf("want 2 attempts, got %d", calls.Load())
	}
}

func TestBackoffHonorsRetryAfter(t *testing.T) {
	rc := RetryConfig{MaxAttempts: 3, BaseBackoff: time.Second, MaxBackoff: 32 * time.Second}
	if d := Backoff(rc, 0, 150*time.Millisecond); d != 150*time.Millisecond {
		t.Fatalf("Retry-After must win over computed backoff, got %v", d)
	}
}

func TestBackoffCapped(t *testing.T) {
	rc := RetryConfig{MaxAttempts: 10, BaseBackoff: time.Second, MaxBackoff: 5 * time.Second}
	for attempt := 0; attempt < 10; attempt++ {
		if d := Backoff(rc, attempt, 0); d > rc.MaxBackoff {
			t.Fatalf("attempt %d backoff %v exceeds cap %v", attempt, d, rc.MaxBackoff)
		}
	}
	// Retry-After 也封顶,但放宽到硬上限(供应商可能要求分钟级等待)。
	if d := Backoff(rc, 0, 10*time.Minute); d != maxRetryAfterHonor {
		t.Fatalf("Retry-After hard cap = %v, want %v", d, maxRetryAfterHonor)
	}
}

func TestStreamTurnRetryWaitsForRetryAfter(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After-Ms", "200")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"slow down"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	t.Cleanup(srv.Close)

	eng := engine.New(catalog.New(catalog.Options{Refresh: false}))
	eng.RegisterProvider("openai", provider.Conn{BaseURL: srv.URL, Route: "openai"})
	c := NewClient(eng)
	start := time.Now()
	stream, err := c.StreamTurnRetry(context.Background(), models.TurnRequest{
		Model: models.ModelRef{Provider: "openai", ID: "gpt-4o"},
	}, RetryConfig{MaxAttempts: 3, BaseBackoff: time.Millisecond, MaxBackoff: 32 * time.Second})
	if err != nil {
		t.Fatalf("retry should recover: %v", err)
	}
	for range stream {
	}
	if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
		t.Fatalf("Retry-After-Ms 200 not honored, elapsed %v", elapsed)
	}
}
