package memory

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPProviderPrefetch(t *testing.T) {
	var gotMethod, gotPath, gotBody string
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		gotBody = string(b)
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"memories":["use go modules"]}`))
	}))
	defer srv.Close()

	p := NewHTTPProvider(HTTPProviderConfig{
		Endpoint: srv.URL,
		APIKey:   "secret",
	})
	mems, err := p.Prefetch(context.Background(), "go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMethod != "POST" {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/search" {
		t.Errorf("path = %q, want /search", gotPath)
	}
	if gotBody != `{"query":"go"}` {
		t.Errorf("body = %q, want {\"query\":\"go\"}", gotBody)
	}
	if gotAuth != "Bearer secret" {
		t.Errorf("Authorization = %q, want Bearer secret", gotAuth)
	}
	if len(mems) != 1 || mems[0] != "use go modules" {
		t.Errorf("memories = %v, want [use go modules]", mems)
	}
}

func TestHTTPProviderSyncTurn(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := NewHTTPProvider(HTTPProviderConfig{Endpoint: srv.URL})
	if err := p.SyncTurn(context.Background(), "hi", "hello"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/observe" {
		t.Errorf("path = %q, want /observe", gotPath)
	}
	if gotBody != `{"user":"hi","assistant":"hello"}` {
		t.Errorf("body = %q, want {\"user\":\"hi\",\"assistant\":\"hello\"}", gotBody)
	}
}

func TestHTTPProviderOnSessionEnd(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := NewHTTPProvider(HTTPProviderConfig{Endpoint: srv.URL})
	if err := p.OnSessionEnd(context.Background(), SessionSummary{SessionID: "s1", TurnCount: 5}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/session/end" {
		t.Errorf("path = %q, want /session/end", gotPath)
	}
	if gotBody != `{"session_id":"s1","turn_count":5}` {
		t.Errorf("body = %q, want {\"session_id\":\"s1\",\"turn_count\":5}", gotBody)
	}
}

func TestHTTPProviderTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Outlast the client context deadline so the request is canceled.
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := NewHTTPProvider(HTTPProviderConfig{Endpoint: srv.URL})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := p.Prefetch(ctx, "go")
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Errorf("expected deadline exceeded error, got %v", err)
	}
}

func TestHTTPProviderContextCancellationDoesNotTripBreaker(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Intentionally never respond; the request is canceled before the timeout.
	}))
	defer srv.Close()

	p := NewHTTPProvider(HTTPProviderConfig{
		Endpoint: srv.URL,
		Timeout:  10,
	}).WithBreaker(newCircuitBreaker(1, 30*time.Second))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := p.Prefetch(ctx, "go")
	if err == nil {
		t.Fatal("expected error from canceled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if !p.Healthy(context.Background()) {
		t.Fatal("expected provider to remain healthy after context cancellation")
	}
}

func TestHTTPProviderCircuitBreaker(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()

	p := NewHTTPProvider(HTTPProviderConfig{Endpoint: srv.URL}).WithBreaker(newCircuitBreaker(2, 30*time.Second))

	if !p.Healthy(context.Background()) {
		t.Fatal("expected provider to be healthy initially")
	}

	_, _ = p.Prefetch(context.Background(), "go")
	if !p.Healthy(context.Background()) {
		t.Fatal("expected provider to be healthy after one failure")
	}

	_, _ = p.Prefetch(context.Background(), "go")
	if p.Healthy(context.Background()) {
		t.Fatal("expected provider to be unhealthy after two consecutive failures")
	}

	if callCount != 2 {
		t.Errorf("server called %d times, want 2", callCount)
	}
}

func TestHTTPProviderCustomHeaders(t *testing.T) {
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"memories":[]}`))
	}))
	defer srv.Close()

	p := NewHTTPProvider(HTTPProviderConfig{
		Endpoint: srv.URL,
		APIKey:   "secret",
		Headers: map[string]string{
			"X-Custom": "value",
		},
	})
	if _, err := p.Prefetch(context.Background(), "go"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotHeaders.Get("Authorization") != "Bearer secret" {
		t.Errorf("Authorization = %q, want Bearer secret", gotHeaders.Get("Authorization"))
	}
	if gotHeaders.Get("X-Custom") != "value" {
		t.Errorf("X-Custom = %q, want value", gotHeaders.Get("X-Custom"))
	}
}

func TestHTTPProviderDefaultPaths(t *testing.T) {
	paths := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths[r.URL.Path] = true
		w.WriteHeader(http.StatusOK)
		if r.URL.Path == "/search" {
			_, _ = w.Write([]byte(`{"memories":[]}`))
		}
	}))
	defer srv.Close()

	p := NewHTTPProvider(HTTPProviderConfig{Endpoint: srv.URL})
	_, _ = p.Prefetch(context.Background(), "go")
	_ = p.SyncTurn(context.Background(), "u", "a")
	_ = p.OnSessionEnd(context.Background(), SessionSummary{SessionID: "s1", TurnCount: 1})

	for _, path := range []string{"/search", "/observe", "/session/end"} {
		if !paths[path] {
			t.Errorf("expected request to %s", path)
		}
	}
}

func TestHTTPProviderCustomPaths(t *testing.T) {
	paths := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths[r.URL.Path] = true
		w.WriteHeader(http.StatusOK)
		if r.URL.Path == "/custom/search" {
			_, _ = w.Write([]byte(`{"memories":[]}`))
		}
	}))
	defer srv.Close()

	p := NewHTTPProvider(HTTPProviderConfig{
		Endpoint:       srv.URL,
		SearchPath:     "/custom/search",
		ObservePath:    "/custom/observe",
		SessionEndPath: "/custom/end",
	})
	_, _ = p.Prefetch(context.Background(), "go")
	_ = p.SyncTurn(context.Background(), "u", "a")
	_ = p.OnSessionEnd(context.Background(), SessionSummary{SessionID: "s1", TurnCount: 1})

	for _, path := range []string{"/custom/search", "/custom/observe", "/custom/end"} {
		if !paths[path] {
			t.Errorf("expected request to %s", path)
		}
	}
}

func TestHTTPProviderRecordsSuccess(t *testing.T) {
	breaker := newCircuitBreaker(2, 30*time.Second)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"memories":[]}`))
	}))
	defer srv.Close()

	p := NewHTTPProvider(HTTPProviderConfig{Endpoint: srv.URL}).WithBreaker(breaker)
	_, _ = p.Prefetch(context.Background(), "go")
	_, _ = p.Prefetch(context.Background(), "go")
	if !p.Healthy(context.Background()) {
		t.Fatal("expected provider to remain healthy after successes")
	}
}

func TestHTTPProviderPrefetchInvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	p := NewHTTPProvider(HTTPProviderConfig{Endpoint: srv.URL})
	_, err := p.Prefetch(context.Background(), "go")
	if err == nil {
		t.Fatal("expected JSON decode error, got nil")
	}
}
