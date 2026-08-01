// pkg/llm/catalog/refresh_test.go
package catalog

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const fakeModelsDev = `{
  "openai": {"models": {
    "gpt-4o": {"name":"GPT-4o","limit":{"context":111111},
      "cost":{"input":2.5,"output":10,"cache_read":1.25,"cache_write":2.5},
      "tool_call":true}
  }}
}`

func TestRefreshMergesModelsDev(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(fakeModelsDev))
	}))
	defer ts.Close()

	cache := filepath.Join(t.TempDir(), "models.json")
	c := New(Options{Refresh: false, SourceURL: ts.URL})
	c.refresh(cache) // synchronous in test
	if w := c.Window("openai", "gpt-4o"); w != 111111 {
		t.Fatalf("refresh did not override window: got %d", w)
	}
}

func TestRefreshFailureKeepsSnapshot(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "models.json")
	c := New(Options{Refresh: false, SourceURL: "http://127.0.0.1:1"}) // nothing listening
	c.refresh(cache)
	if w := c.Window("openai", "gpt-4o"); w != 128000 {
		t.Fatalf("snapshot window lost after failed refresh: got %d", w)
	}
}

func TestRefreshPreservesOverrides(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(fakeModelsDev))
	}))
	defer ts.Close()
	cache := filepath.Join(t.TempDir(), "models.json")
	c := New(Options{Refresh: false, SourceURL: ts.URL, Overrides: []Entry{
		{ID: "gpt-4o", Provider: "openai", ContextWindow: 999},
	}})
	c.refresh(cache)
	if w := c.Window("openai", "gpt-4o"); w != 999 {
		t.Fatalf("override lost after refresh: got %d", w)
	}
}

func waitForCond(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s not met within %v", what, d)
}

func TestPeriodicRefreshPicksUpChanges(t *testing.T) {
	var mu sync.Mutex
	body := `{"openai":{"models":{"gpt-4o":{"name":"GPT-4o","limit":{"context":111111},"tool_call":true}}}}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_, _ = w.Write([]byte(body))
	}))
	defer ts.Close()

	c := New(Options{Refresh: true, RefreshInterval: 20 * time.Millisecond, SourceURL: ts.URL})
	defer func() { _ = c.Close() }()

	waitForCond(t, 2*time.Second, "initial refresh", func() bool {
		return c.Window("openai", "gpt-4o") == 111111
	})

	mu.Lock()
	body = `{"openai":{"models":{"gpt-4o":{"name":"GPT-4o","limit":{"context":222222},"tool_call":true}}}}`
	mu.Unlock()
	waitForCond(t, 2*time.Second, "periodic refresh", func() bool {
		return c.Window("openai", "gpt-4o") == 222222
	})
}

func TestRefreshStatusReportsError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()
	c := New(Options{Refresh: true, RefreshInterval: 10 * time.Millisecond, SourceURL: ts.URL})
	defer func() { _ = c.Close() }()

	waitForCond(t, 2*time.Second, "status error", func() bool {
		_, err := c.Status()
		return err != nil
	})
}
