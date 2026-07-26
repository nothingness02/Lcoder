// pkg/llm/provider/adapter_test.go
package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

// captureRequestBody streams req through a and returns the decoded request body
// the adapter sent to the (stub) server.
func captureRequestBody(t *testing.T, a Adapter, req models.TurnRequest) map[string]any {
	t.Helper()
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(srv.Close)
	ch, err := a.Stream(context.Background(), Conn{BaseURL: srv.URL, APIKey: "k"}, req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch {
	}
	var body map[string]any
	if err := json.Unmarshal(captured, &body); err != nil {
		t.Fatalf("request body not JSON: %v (%s)", err, captured)
	}
	return body
}

func TestDefaultBaseURL(t *testing.T) {
	cases := map[string]string{
		"openai":     "https://api.openai.com/v1",
		"deepseek":   "https://api.deepseek.com/v1",
		"moonshot":   "https://api.moonshot.cn/v1",
		"kimi-code":  "https://api.kimi.com/coding/v1",
		"openrouter": "https://openrouter.ai/api/v1",
		"gemini":     "https://generativelanguage.googleapis.com/v1beta/openai",
		"anthropic":  "https://api.anthropic.com/v1",
	}
	for route, want := range cases {
		if got := DefaultBaseURL(route); got != want {
			t.Errorf("DefaultBaseURL(%q)=%q, want %q", route, got, want)
		}
	}
	if got := DefaultBaseURL("unknown"); got != "" {
		t.Errorf("DefaultBaseURL(unknown)=%q, want empty", got)
	}
}

func TestEventKindString(t *testing.T) {
	if KindTextDelta.String() != "text_delta" {
		t.Errorf("KindTextDelta=%q", KindTextDelta.String())
	}
}
