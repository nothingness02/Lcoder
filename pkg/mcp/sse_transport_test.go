package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestSSETransportInitializeAndCall verifies endpoint negotiation and a full
// JSON-RPC round-trip over the SSE stream.
func TestSSETransportInitializeAndCall(t *testing.T) {
	var mu sync.Mutex
	pending := make(map[int]chan Response)
	closed := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sse":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Error("response writer does not support flushing")
				return
			}
			fmt.Fprint(w, "event: endpoint\ndata: /messages?session=abc\n\n")
			flusher.Flush()

			// Keep the SSE connection open and forward pending responses.
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-r.Context().Done():
					close(closed)
					return
				case <-ticker.C:
					mu.Lock()
					for id, rw := range pending {
						resp := Response{JSONRPC: "2.0", ID: id, Result: mustMarshal(t, map[string]any{"ok": true})}
						fmt.Fprintf(w, "event: message\ndata: %s\n\n", mustMarshalString(resp))
						flusher.Flush()
						delete(pending, id)
						rw <- resp
					}
					mu.Unlock()
				}
			}
		case "/messages":
			var req Request
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			mu.Lock()
			pending[req.ID] = make(chan Response, 1)
			mu.Unlock()
			w.WriteHeader(http.StatusAccepted)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	tr, err := NewSSETransport(srv.URL, nil, 5*time.Second)
	if err != nil {
		t.Fatalf("new sse transport: %v", err)
	}
	defer tr.Close()

	if !strings.HasPrefix(tr.endpoint, srv.URL+"/messages") {
		t.Fatalf("unexpected endpoint %q", tr.endpoint)
	}

	var result map[string]any
	if err := tr.Call(context.Background(), "ping", map[string]any{}, &result); err != nil {
		t.Fatalf("call: %v", err)
	}
	if result["ok"] != true {
		t.Fatalf("unexpected result: %+v", result)
	}

	select {
	case <-closed:
		t.Fatal("sse reader closed unexpectedly")
	case <-time.After(50 * time.Millisecond):
	}
}

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func mustMarshalString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
