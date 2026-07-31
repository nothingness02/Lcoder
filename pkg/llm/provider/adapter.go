// pkg/llm/provider/adapter.go
package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lcoder/lcoder/pkg/models"
)

// Adapter performs one provider call and streams normalized Events.
// Implementations must close the returned channel when the stream ends and
// must not block on a full channel after ctx is cancelled.
type Adapter interface {
	Stream(ctx context.Context, conn Conn, req models.TurnRequest) (<-chan Event, error)
}

// Conn holds the resolved connection settings for a single provider call.
type Conn struct {
	BaseURL string            // falls back to DefaultBaseURL(Route) when empty
	APIKey  string            //
	Route   string            // protocol family: openai | anthropic | gemini | ...
	Headers map[string]string // extra headers (merged last)
}

// defaultBaseURLs maps a protocol route to its canonical base URL.
var defaultBaseURLs = map[string]string{
	"openai":     "https://api.openai.com/v1",
	"anthropic":  "https://api.anthropic.com/v1",
	"deepseek":   "https://api.deepseek.com/v1",
	"gemini":     "https://generativelanguage.googleapis.com/v1beta/openai",
	"moonshot":   "https://api.moonshot.cn/v1",
	"kimi-code":  "https://api.kimi.com/coding/v1",
	"xai":        "https://api.x.ai/v1",
	"alibaba":    "https://dashscope.aliyuncs.com/compatible-mode/v1",
	"zhipu":      "https://open.bigmodel.cn/api/paas/v4",
	"xiaomi":     "https://api.xiaomimimo.com/v1",
	"openrouter": "https://openrouter.ai/api/v1",
}

// DefaultBaseURL returns the canonical base URL for a route, or "" if unknown.
func DefaultBaseURL(route string) string {
	return defaultBaseURLs[route]
}

// ResolveBaseURL returns conn.BaseURL when set, else the route default.
func ResolveBaseURL(conn Conn) string {
	if conn.BaseURL != "" {
		return conn.BaseURL
	}
	return DefaultBaseURL(conn.Route)
}

// maxErrorBodyBytes bounds how much of a failed response body is read into the
// classified error (a proxy may answer with a large HTML page).
const maxErrorBodyBytes = 64 * 1024

// doStreamRequest sends req and returns the response only on 200 OK. Any other
// status is classified and returned synchronously — before the event channel
// exists — so StreamTurnRetry can see rate_limit/internal failures and retry
// them. The body is drained (bounded) and closed on failure.
func doStreamRequest(client *http.Client, req *http.Request) (*http.Response, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		_ = resp.Body.Close()
		return nil, classifyHTTP(resp.StatusCode, data, resp.Header)
	}
	return resp, nil
}

// emit sends ev unless ctx is already cancelled (prevents goroutine leak on a
// stalled consumer that has gone away).
func emit(ctx context.Context, ch chan<- Event, ev Event) {
	select {
	case ch <- ev:
	case <-ctx.Done():
	}
}

// classifyHTTP maps a provider HTTP status + body to a normalized EventError.
// 400 responses whose body names a context-length limit are classified as
// context_overflow (never retryable; the agent layer routes it to compaction).
func classifyHTTP(status int, body []byte, headers http.Header) *EventError {
	code := "internal"
	switch {
	case status == 429:
		code = "rate_limit"
	case status == 401 || status == 403:
		code = "auth"
	case status == 400:
		code = "bad_request"
		if isContextOverflowBody(body) {
			code = "context_overflow"
		}
	}
	pe := map[string]any{}
	_ = json.Unmarshal(body, &pe)
	return &EventError{Code: code, Message: string(body), ProviderError: pe, RetryAfter: parseRetryAfter(headers)}
}

// parseRetryAfter reads Retry-After (seconds or HTTP-date) and Retry-After-Ms.
func parseRetryAfter(h http.Header) time.Duration {
	if ms := h.Get("Retry-After-Ms"); ms != "" {
		if n, err := strconv.Atoi(ms); err == nil && n > 0 {
			return time.Duration(n) * time.Millisecond
		}
	}
	ra := h.Get("Retry-After")
	if ra == "" {
		return 0
	}
	if n, err := strconv.Atoi(ra); err == nil {
		if n < 0 {
			return 0
		}
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(ra); err == nil {
		return max(time.Until(t), 0)
	}
	return 0
}

// isContextOverflowBody reports whether a 400 body says the prompt exceeded
// the model's context window (openai "context_length_exceeded", anthropic
// "prompt is too long", generic "maximum context length").
func isContextOverflowBody(body []byte) bool {
	s := strings.ToLower(string(body))
	return strings.Contains(s, "context_length_exceeded") ||
		strings.Contains(s, "prompt is too long") ||
		strings.Contains(s, "maximum context length")
}
