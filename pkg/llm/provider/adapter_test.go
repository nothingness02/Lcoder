// pkg/llm/provider/adapter_test.go
package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

func TestClassifyHTTPRetryAfter(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		want    time.Duration
	}{
		{"seconds", map[string]string{"Retry-After": "2"}, 2 * time.Second},
		{"millis", map[string]string{"Retry-After-Ms": "150"}, 150 * time.Millisecond},
		{"http-date", map[string]string{"Retry-After": time.Now().Add(3 * time.Second).UTC().Format(http.TimeFormat)}, 0}, // 只校验 >0
		{"absent", nil, 0},
		{"garbage", map[string]string{"Retry-After": "soon"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			for k, v := range tc.headers {
				h.Set(k, v)
			}
			err := classifyHTTP(429, []byte(`{}`), h)
			if tc.name == "http-date" {
				if err.RetryAfter <= 0 {
					t.Fatalf("http-date Retry-After not parsed: %+v", err)
				}
				return
			}
			if err.RetryAfter != tc.want {
				t.Fatalf("RetryAfter = %v, want %v", err.RetryAfter, tc.want)
			}
		})
	}
}

func TestProtocolParseAndDerive(t *testing.T) {
	for _, s := range []string{"openai-chat", "openai-responses", "anthropic"} {
		if _, err := ParseProtocol(s); err != nil {
			t.Fatalf("ParseProtocol(%q): %v", s, err)
		}
	}
	if _, err := ParseProtocol("gpt"); err == nil {
		t.Fatal("unknown protocol must error")
	}
	if p := ProtocolForRoute("anthropic"); p != ProtocolAnthropic {
		t.Fatalf("route anthropic → %q", p)
	}
	if p := ProtocolForRoute("openai-responses"); p != ProtocolOpenAIResponses {
		t.Fatalf("route openai-responses → %q", p)
	}
	for _, r := range []string{"deepseek", "openai", "gemini", "xai", ""} {
		if p := ProtocolForRoute(r); p != ProtocolOpenAIChat {
			t.Fatalf("route %q → %q, want openai-chat", r, p)
		}
	}
}
