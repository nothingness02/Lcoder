# Go LLM Gateway Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the Python FastAPI+litellm gateway with an in-process Go engine so Lcoder ships as a single binary with no Python/subprocess dependency.

**Architecture:** Keep `llm.Client`'s 5-method surface unchanged; swap its internals from HTTP/SSE-to-subprocess over to an in-process `engine` that routes a `TurnRequest` to one of two hand-written provider adapters (OpenAI-compatible, Anthropic), parses provider SSE into a normalized `provider.Event` channel, and maps those events back into the existing `GatewayEvent` payload shapes the agent loop already consumes. A `catalog` package serves model metadata from an embedded models.dev snapshot with non-blocking background refresh.

**Tech Stack:** Go standard library only (`net/http`, `bufio`, `encoding/json`, `embed`), no provider SDKs. Existing types in `pkg/models` and `pkg/config` are reused verbatim.

---

## Design Reference

Full spec: `docs/superpowers/specs/2026-06-28-go-llm-gateway-design.md`. Read it before starting.

## Critical Contracts (do not break these)

The agent loop (`pkg/agent/loop.go:400-483`) consumes `GatewayEvent` via `ev.Type()` and reads specific payload keys. The new engine MUST emit `GatewayEvent`s with these exact shapes (client.go maps `provider.Event` → `GatewayEvent`):

| `ev.Type()` | Payload keys the loop reads |
|---|---|
| `start` | (none read; loop resets `partial`) |
| `text_delta` | `delta` (string) |
| `thinking_delta` | `delta` (string) |
| `toolcall_delta` | `tool_call` (map; loop calls `updateToolCall`) — *current Python sends `tool_call_index`/`arguments_json`; loop reads `tool_call` which is nil today and tool calls finalize in `done`. The Go engine MUST keep finalizing tool calls in `done.FinalMessage()`; emitting `tool_call` is optional.* |
| `done` | `message` (AgentMessage via `ev.FinalMessage()`), `usage` (LLMUsage via `ev.Usage()`) |
| `error` | `error` (GatewayError via `ev.Error()`) |

`TurnStream.Next(ctx)` signature and `GatewayEvent`/`GatewayError`/`GatewayHTTPError` types stay identical. Only `TurnStream`'s backing store changes from `io.ReadCloser` to `<-chan GatewayEvent`.

## File Structure

```
pkg/llm/
  client.go            [modify] StreamTurn/ListModels/ModelWindow/RegisterProvider/Health delegate to engine
  turnstream.go         [new]   channel-backed TurnStream (moved out of client.go)
  gateway.go            [delete] StartGateway/GatewayManager
  engine/
    engine.go           [new]   Engine: provider registry, StreamTurn orchestration, cost, error classify
    engine_test.go      [new]
  provider/
    event.go            [new]   Event, EventKind, EventError
    adapter.go          [new]   Adapter interface, Conn, DefaultBaseURL table, emit/classifyHTTP
    openai.go           [new]   OpenAI-compatible adapter
    openai_test.go      [new]
    anthropic.go        [new]   Anthropic Messages adapter
    anthropic_test.go   [new]
    convert.go          [new]   AgentMessage -> provider wire format (shared helpers)
    cachepolicy.go      [new]   ComputeCacheMarks (Anthropic only, ported)
    cachepolicy_test.go [new]
  pricing/
    pricing.go          [new]   ModelPrice, EstimateCost, DefaultPricing (leaf, ported from pricing.py)
    pricing_test.go     [new]
  catalog/
    catalog.go          [new]   snapshot load + models.dev refresh + models.yaml overlay
    catalog_test.go     [new]
    snapshot.json       [new]   go:embed bundled models.dev subset
```

Package dependency direction (acyclic): `pricing` (leaf) ← `catalog`; `provider` (leaf, holds `Event`); `engine` imports `catalog`+`provider`+`pricing`; `llm` (client) imports `engine`. Nothing the engine imports imports `llm` back.

After all adapters work and smoke-tested, delete `gateway/` and rewire `cmd/lcoder/main.go`.

---

## Task 1: Event type, Adapter interface, Conn, base URL table

**Files:**
- Create: `pkg/llm/provider/event.go`
- Create: `pkg/llm/provider/adapter.go`
- Test: `pkg/llm/provider/adapter_test.go`

- [ ] **Step 1: Write the failing test**

```go
// pkg/llm/provider/adapter_test.go
package provider

import "testing"

func TestDefaultBaseURL(t *testing.T) {
	cases := map[string]string{
		"openai":     "https://api.openai.com/v1",
		"deepseek":   "https://api.deepseek.com/v1",
		"moonshot":   "https://api.moonshot.cn/v1",
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/llm/provider/ -run TestDefaultBaseURL -v`
Expected: FAIL — package does not compile (`DefaultBaseURL` undefined).

- [ ] **Step 3: Write event.go**

```go
// pkg/llm/provider/event.go
package provider

import "github.com/lcoder/lcoder/pkg/models"

// EventKind enumerates the normalized streaming events every adapter emits,
// independent of the provider's native wire format.
type EventKind int

const (
	KindStart EventKind = iota
	KindTextDelta
	KindThinkingDelta
	KindToolCallDelta
	KindDone
	KindError
)

func (k EventKind) String() string {
	switch k {
	case KindStart:
		return "start"
	case KindTextDelta:
		return "text_delta"
	case KindThinkingDelta:
		return "thinking_delta"
	case KindToolCallDelta:
		return "toolcall_delta"
	case KindDone:
		return "done"
	case KindError:
		return "error"
	default:
		return "unknown"
	}
}

// Event is one normalized streaming event from an adapter.
type Event struct {
	Kind EventKind

	// Delta carries incremental text for KindTextDelta / KindThinkingDelta.
	Delta string

	// ToolCallIndex / ArgumentsJSON describe a streamed tool-call fragment
	// (KindToolCallDelta). Adapters accumulate these and emit the finished
	// tool calls inside Message on KindDone.
	ToolCallIndex int
	ArgumentsJSON string

	// Message is the finalized assistant message on KindDone.
	Message models.AgentMessage

	// Usage is the token/cost usage on KindDone (nil if the provider omitted it).
	Usage *models.LLMUsage

	// Err is set only on KindError.
	Err *EventError
}

// EventError is a classified provider failure carried on KindError.
type EventError struct {
	Code          string         // bad_request | auth | rate_limit | internal
	Message       string         //
	ProviderError map[string]any //
}
```

- [ ] **Step 4: Write adapter.go**

```go
// pkg/llm/provider/adapter.go
package provider

import (
	"context"

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
	"deepseek":   "https://api.deepseek.com/v1",
	"moonshot":   "https://api.moonshot.cn/v1",
	"openrouter": "https://openrouter.ai/api/v1",
	"gemini":     "https://generativelanguage.googleapis.com/v1beta/openai",
	"anthropic":  "https://api.anthropic.com/v1",
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
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./pkg/llm/provider/ -run 'TestDefaultBaseURL|TestEventKindString' -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add pkg/llm/provider/event.go pkg/llm/provider/adapter.go pkg/llm/provider/adapter_test.go
git commit -m "feat(llm): add provider Event type, Adapter interface, base URL table"
```

---

## Task 2: Message conversion helpers (AgentMessage -> OpenAI wire)

**Files:**
- Create: `pkg/llm/provider/convert.go`
- Test: `pkg/llm/provider/convert_test.go`

This ports `message_to_litellm` / `content_to_litellm` / `tools_to_litellm` from `server.py` to Go. It produces `map[string]any` bodies marshalled to JSON.

- [ ] **Step 1: Write the failing test**

```go
// pkg/llm/provider/convert_test.go
package provider

import (
	"reflect"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

func TestOpenAIMessagesUserText(t *testing.T) {
	msgs := []models.AgentMessage{
		models.NewAgentMessage(models.RoleUser, models.TextContent{Text: "hi"}),
	}
	got := openAIMessages(msgs)
	want := []map[string]any{{"role": "user", "content": "hi"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestOpenAIMessagesAssistantToolCall(t *testing.T) {
	msgs := []models.AgentMessage{
		models.NewAgentMessage(models.RoleAssistant,
			models.ToolCallContent{ID: "c1", Name: "read", Arguments: map[string]any{"path": "x"}}),
	}
	got := openAIMessages(msgs)
	if got[0]["role"] != "assistant" {
		t.Fatalf("role=%v", got[0]["role"])
	}
	tcs, ok := got[0]["tool_calls"].([]map[string]any)
	if !ok || len(tcs) != 1 {
		t.Fatalf("tool_calls=%v", got[0]["tool_calls"])
	}
	fn := tcs[0]["function"].(map[string]any)
	if fn["name"] != "read" || fn["arguments"] != `{"path":"x"}` {
		t.Errorf("function=%v", fn)
	}
}

func TestOpenAIMessagesToolResult(t *testing.T) {
	msgs := []models.AgentMessage{
		models.NewAgentMessage(models.RoleToolResult,
			models.ToolResultContent{ToolCallID: "c1", Content: []models.ContentPart{models.TextContent{Text: "ok"}}}),
	}
	got := openAIMessages(msgs)
	want := []map[string]any{{"role": "tool", "tool_call_id": "c1", "content": "ok"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestOpenAITools(t *testing.T) {
	tools := []models.ToolDefinition{{Name: "read", Description: "read a file", Parameters: map[string]any{"type": "object"}}}
	got := openAITools(tools)
	if len(got) != 1 || got[0]["type"] != "function" {
		t.Fatalf("got %v", got)
	}
	fn := got[0]["function"].(map[string]any)
	if fn["name"] != "read" || fn["description"] != "read a file" {
		t.Errorf("function=%v", fn)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/llm/provider/ -run TestOpenAI -v`
Expected: FAIL — `openAIMessages` undefined.

- [ ] **Step 3: Write convert.go**

```go
// pkg/llm/provider/convert.go
package provider

import (
	"encoding/json"

	"github.com/lcoder/lcoder/pkg/models"
)

// openAIContent converts content parts to OpenAI message content: a bare string
// for a single text part, else a typed parts array (text / image_url).
func openAIContent(parts []models.ContentPart) any {
	if len(parts) == 1 {
		if t, ok := parts[0].(models.TextContent); ok {
			return t.Text
		}
	}
	out := []map[string]any{}
	for _, p := range parts {
		switch c := p.(type) {
		case models.TextContent:
			if c.Text != "" {
				out = append(out, map[string]any{"type": "text", "text": c.Text})
			}
		case models.ImageContent:
			if c.Data != "" {
				mime := c.MimeType
				if mime == "" {
					mime = "image/jpeg"
				}
				out = append(out, map[string]any{
					"type":      "image_url",
					"image_url": map[string]any{"url": "data:" + mime + ";base64," + c.Data},
				})
			}
		}
	}
	return out
}

// openAIMessages converts agent messages to OpenAI chat messages, dropping any
// message that produces no representable content (mirrors message_to_litellm).
func openAIMessages(msgs []models.AgentMessage) []map[string]any {
	out := []map[string]any{}
	for _, m := range msgs {
		switch m.Role {
		case models.RoleSystem:
			out = append(out, map[string]any{"role": "system", "content": openAIContent(m.Content)})
		case models.RoleUser:
			out = append(out, map[string]any{"role": "user", "content": openAIContent(m.Content)})
		case models.RoleAssistant:
			var assistantContent []map[string]any
			var toolCalls []map[string]any
			for _, p := range m.Content {
				switch c := p.(type) {
				case models.TextContent:
					if c.Text != "" {
						assistantContent = append(assistantContent, map[string]any{"type": "text", "text": c.Text})
					}
				case models.ToolCallContent:
					args, _ := json.Marshal(c.Arguments)
					if c.Arguments == nil {
						args = []byte("{}")
					}
					toolCalls = append(toolCalls, map[string]any{
						"id":   c.ID,
						"type": "function",
						"function": map[string]any{
							"name":      c.Name,
							"arguments": string(args),
						},
					})
				}
			}
			msg := map[string]any{"role": "assistant"}
			if len(assistantContent) > 0 {
				msg["content"] = assistantContent
			}
			if len(toolCalls) > 0 {
				msg["tool_calls"] = toolCalls
			}
			out = append(out, msg)
		case models.RoleToolResult:
			var result *models.ToolResultContent
			for _, p := range m.Content {
				if r, ok := p.(models.ToolResultContent); ok {
					result = &r
					break
				}
			}
			if result == nil {
				continue
			}
			text := ""
			for _, child := range result.Content {
				if t, ok := child.(models.TextContent); ok {
					text += t.Text
				}
			}
			out = append(out, map[string]any{
				"role":         "tool",
				"tool_call_id": result.ToolCallID,
				"content":      text,
			})
		}
	}
	return out
}

// openAITools converts tool definitions to OpenAI function tools.
func openAITools(tools []models.ToolDefinition) []map[string]any {
	if len(tools) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.Parameters,
			},
		})
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/llm/provider/ -run TestOpenAI -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/llm/provider/convert.go pkg/llm/provider/convert_test.go
git commit -m "feat(llm): port message/tool conversion to Go (OpenAI wire format)"
```

---

## Task 3: OpenAI-compatible adapter (request + SSE parsing)

**Files:**
- Create: `pkg/llm/provider/openai.go`
- Test: `pkg/llm/provider/openai_test.go`

Covers openai/deepseek/moonshot/openrouter/gemini. Reads `data: {json}` SSE lines, `data: [DONE]` ends. Maps `delta.content`→text, `delta.reasoning_content`→thinking, `delta.tool_calls[]`→accumulated tool calls, final `usage`→tokens. Tool calls are finalized into the `KindDone` Message (this is the bug the spec calls out: streamed tool-call fragments must accumulate by index).

- [ ] **Step 1: Write the failing test (canned SSE via httptest)**

```go
// pkg/llm/provider/openai_test.go
package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

// sseServer returns an httptest server that replays the given raw SSE body.
func sseServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func collect(t *testing.T, ch <-chan Event) []Event {
	t.Helper()
	var out []Event
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

func TestOpenAIStreamTextAndUsage(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{}}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2,\"total_tokens\":7}}\n\n" +
		"data: [DONE]\n\n"
	srv := sseServer(t, body)

	ad := OpenAICompat{}
	ch, err := ad.Stream(context.Background(),
		Conn{BaseURL: srv.URL, APIKey: "k", Route: "openai"},
		models.TurnRequest{Model: models.ModelRef{Provider: "openai", ID: "gpt-4o"},
			Messages: []models.AgentMessage{models.UserMessage("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)

	if evs[0].Kind != KindStart {
		t.Fatalf("first event %v", evs[0].Kind)
	}
	var text string
	var done *Event
	for i := range evs {
		switch evs[i].Kind {
		case KindTextDelta:
			text += evs[i].Delta
		case KindDone:
			done = &evs[i]
		}
	}
	if text != "Hello" {
		t.Errorf("text=%q", text)
	}
	if done == nil || done.Usage == nil || done.Usage.PromptTokens != 5 || done.Usage.CompletionTokens != 2 {
		t.Fatalf("done/usage wrong: %+v", done)
	}
	if done.Message.Text() != "Hello" {
		t.Errorf("done message text=%q", done.Message.Text())
	}
}

func TestOpenAIStreamToolCallFragments(t *testing.T) {
	// Tool call arguments arrive split across chunks; they must accumulate by index.
	body := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"function\":{\"name\":\"read\",\"arguments\":\"{\\\"pa\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"th\\\":\\\"x\\\"}\"}}]}}]}\n\n" +
		"data: [DONE]\n\n"
	srv := sseServer(t, body)

	ad := OpenAICompat{}
	ch, _ := ad.Stream(context.Background(),
		Conn{BaseURL: srv.URL, APIKey: "k", Route: "openai"},
		models.TurnRequest{Model: models.ModelRef{Provider: "openai", ID: "gpt-4o"}})
	evs := collect(t, ch)

	var done *Event
	for i := range evs {
		if evs[i].Kind == KindDone {
			done = &evs[i]
		}
	}
	if done == nil {
		t.Fatal("no done event")
	}
	calls := done.Message.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("want 1 tool call, got %d", len(calls))
	}
	if calls[0].Name != "read" || calls[0].ID != "c1" {
		t.Errorf("tool call meta wrong: %+v", calls[0])
	}
	if calls[0].Arguments["path"] != "x" {
		t.Errorf("accumulated args wrong: %+v", calls[0].Arguments)
	}
}

func TestOpenAIStreamThinkingDelta(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"ponder\"}}]}\n\n" +
		"data: [DONE]\n\n"
	srv := sseServer(t, body)
	ad := OpenAICompat{}
	ch, _ := ad.Stream(context.Background(), Conn{BaseURL: srv.URL, Route: "deepseek"},
		models.TurnRequest{Model: models.ModelRef{Provider: "deepseek", ID: "deepseek-reasoner"}})
	evs := collect(t, ch)
	var thinking string
	for _, e := range evs {
		if e.Kind == KindThinkingDelta {
			thinking += e.Delta
		}
	}
	if thinking != "ponder" {
		t.Errorf("thinking=%q", thinking)
	}
}

func TestOpenAIStreamHTTPErrorClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"slow down"}}`))
	}))
	t.Cleanup(srv.Close)
	ad := OpenAICompat{}
	ch, err := ad.Stream(context.Background(), Conn{BaseURL: srv.URL, Route: "openai"},
		models.TurnRequest{Model: models.ModelRef{Provider: "openai", ID: "gpt-4o"}})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)
	last := evs[len(evs)-1]
	if last.Kind != KindError || last.Err == nil || last.Err.Code != "rate_limit" {
		t.Fatalf("want rate_limit error, got %+v", last)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/llm/provider/ -run TestOpenAIStream -v`
Expected: FAIL — `OpenAICompat` undefined.

- [ ] **Step 3: Write openai.go**

```go
// pkg/llm/provider/openai.go
package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/lcoder/lcoder/pkg/models"
)

// OpenAICompat is the adapter for OpenAI and all OpenAI-compatible providers
// (deepseek, moonshot, openrouter, gemini via its OpenAI endpoint).
type OpenAICompat struct{}

// toolBuffer accumulates a streamed tool call across chunks.
type toolBuffer struct {
	id   string
	name string
	args strings.Builder
}

func (OpenAICompat) Stream(ctx context.Context, conn Conn, req models.TurnRequest) (<-chan Event, error) {
	body := map[string]any{
		"model":    req.Model.ID,
		"messages": withSystem(req.SystemPrompt, openAIMessages(req.Messages)),
		"stream":   true,
	}
	if tools := openAITools(req.Tools); tools != nil {
		body["tools"] = tools
	}
	if req.Generation.Temperature != 0 {
		body["temperature"] = req.Generation.Temperature
	}
	if req.Generation.MaxTokens != 0 {
		body["max_tokens"] = req.Generation.MaxTokens
	}
	if req.Generation.TopP != 0 {
		body["top_p"] = req.Generation.TopP
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	url := ResolveBaseURL(conn) + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if conn.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+conn.APIKey)
	}
	for k, v := range conn.Headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, err
	}

	out := make(chan Event, 16)
	go func() {
		defer close(out)
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			data, _ := io.ReadAll(resp.Body)
			emit(ctx, out, Event{Kind: KindError, Err: classifyHTTP(resp.StatusCode, data)})
			return
		}

		emit(ctx, out, Event{Kind: KindStart})

		var textBuf, thinkBuf strings.Builder
		tools := map[int]*toolBuffer{}
		var usage *models.LLMUsage

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "[DONE]" {
				break
			}
			var chunk openAIChunk
			if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
				continue
			}
			if chunk.Usage != nil {
				usage = chunk.Usage.toLLMUsage()
			}
			if len(chunk.Choices) == 0 {
				continue
			}
			d := chunk.Choices[0].Delta
			if d.Content != "" {
				textBuf.WriteString(d.Content)
				emit(ctx, out, Event{Kind: KindTextDelta, Delta: d.Content})
			}
			if d.ReasoningContent != "" {
				thinkBuf.WriteString(d.ReasoningContent)
				emit(ctx, out, Event{Kind: KindThinkingDelta, Delta: d.ReasoningContent})
			}
			for _, tc := range d.ToolCalls {
				buf := tools[tc.Index]
				if buf == nil {
					buf = &toolBuffer{}
					tools[tc.Index] = buf
				}
				if tc.ID != "" {
					buf.id = tc.ID
				}
				if tc.Function.Name != "" {
					buf.name = tc.Function.Name
				}
				if tc.Function.Arguments != "" {
					buf.args.WriteString(tc.Function.Arguments)
				}
				emit(ctx, out, Event{Kind: KindToolCallDelta, ToolCallIndex: tc.Index, ArgumentsJSON: tc.Function.Arguments})
			}
		}
		if err := scanner.Err(); err != nil {
			emit(ctx, out, Event{Kind: KindError, Err: &EventError{Code: "internal", Message: err.Error()}})
			return
		}

		emit(ctx, out, Event{Kind: KindDone,
			Message: finalizeMessage(thinkBuf.String(), textBuf.String(), tools),
			Usage:   usage})
	}()
	return out, nil
}

// withSystem prepends a system message when systemPrompt is non-empty.
func withSystem(systemPrompt string, msgs []map[string]any) []map[string]any {
	if systemPrompt == "" {
		return msgs
	}
	return append([]map[string]any{{"role": "system", "content": systemPrompt}}, msgs...)
}

// finalizeMessage assembles the finished assistant message from accumulated buffers.
func finalizeMessage(thinking, text string, tools map[int]*toolBuffer) models.AgentMessage {
	msg := models.NewAgentMessage(models.RoleAssistant)
	var parts []models.ContentPart
	if thinking != "" {
		parts = append(parts, models.ThinkingContent{Text: thinking})
	}
	if text != "" {
		parts = append(parts, models.TextContent{Text: text})
	}
	idxs := make([]int, 0, len(tools))
	for i := range tools {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	for _, i := range idxs {
		buf := tools[i]
		args := map[string]any{}
		raw := buf.args.String()
		if raw != "" {
			if err := json.Unmarshal([]byte(raw), &args); err != nil {
				args = map[string]any{"__error__": raw}
			}
		}
		id := buf.id
		if id == "" {
			id = fmt.Sprintf("call_%d", i)
		}
		parts = append(parts, models.ToolCallContent{ID: id, Name: buf.name, Arguments: args})
	}
	msg.Content = parts
	return msg
}

// --- chunk decoding ---

type openAIChunk struct {
	Choices []struct {
		Delta openAIDelta `json:"delta"`
	} `json:"choices"`
	Usage *openAIUsage `json:"usage"`
}

type openAIDelta struct {
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content"`
	ToolCalls        []struct {
		Index    int    `json:"index"`
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

type openAIUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	PromptCacheReadTok  int `json:"prompt_cache_read_tokens"`
	PromptCacheWriteTok int `json:"prompt_cache_write_tokens"`
}

func (u openAIUsage) toLLMUsage() *models.LLMUsage {
	return &models.LLMUsage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
		CacheReadTokens:  u.PromptCacheReadTok,
		CacheWriteTokens: u.PromptCacheWriteTok,
	}
}
```

- [ ] **Step 4: Write the shared `emit` + `classifyHTTP` helpers**

Add to `pkg/llm/provider/adapter.go`:

```go
// emit sends ev unless ctx is already cancelled (prevents goroutine leak on a
// stalled consumer that has gone away).
func emit(ctx context.Context, ch chan<- Event, ev Event) {
	select {
	case ch <- ev:
	case <-ctx.Done():
	}
}

// classifyHTTP maps a provider HTTP status + body to a normalized EventError.
func classifyHTTP(status int, body []byte) *EventError {
	code := "internal"
	switch {
	case status == 429:
		code = "rate_limit"
	case status == 401 || status == 403:
		code = "auth"
	case status == 400:
		code = "bad_request"
	}
	pe := map[string]any{}
	_ = json.Unmarshal(body, &pe)
	return &EventError{Code: code, Message: string(body), ProviderError: pe}
}
```

Add `"context"` and `"encoding/json"` to the adapter.go import block.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./pkg/llm/provider/ -run TestOpenAIStream -v`
Expected: PASS (all four subtests, including the tool-fragment accumulation).

- [ ] **Step 6: Commit**

```bash
git add pkg/llm/provider/openai.go pkg/llm/provider/openai_test.go pkg/llm/provider/adapter.go
git commit -m "feat(llm): hand-written OpenAI-compatible streaming adapter"
```

---

## Task 4: Anthropic adapter (request + native SSE parsing)

**Files:**
- Create: `pkg/llm/provider/anthropic.go`
- Test: `pkg/llm/provider/anthropic_test.go`

Anthropic's native events differ from OpenAI's. Reference `reference/higress/.../ai-proxy/provider/claude.go` (Apache-2.0; write fresh, don't copy). Events: `message_start` (initial usage), `content_block_start` (tool_use → new tool call id/name), `content_block_delta` (`text_delta`→text, `thinking_delta`→thinking, `input_json_delta`→tool args fragment), `message_delta` (final usage), `message_stop`→done. SSE lines come as `event: <name>\ndata: {json}\n\n`.

- [ ] **Step 1: Write the failing test**

```go
// pkg/llm/provider/anthropic_test.go
package provider

import (
	"context"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

func TestAnthropicStreamTextThinkingUsage(t *testing.T) {
	body := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"hmm\"}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hi\"}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":3}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	srv := sseServer(t, body)

	ad := Anthropic{}
	ch, err := ad.Stream(context.Background(),
		Conn{BaseURL: srv.URL, APIKey: "k", Route: "anthropic"},
		models.TurnRequest{Model: models.ModelRef{Provider: "anthropic", ID: "claude-sonnet-4"},
			Messages: []models.AgentMessage{models.UserMessage("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)

	var text, thinking string
	var done *Event
	for i := range evs {
		switch evs[i].Kind {
		case KindTextDelta:
			text += evs[i].Delta
		case KindThinkingDelta:
			thinking += evs[i].Delta
		case KindDone:
			done = &evs[i]
		}
	}
	if text != "Hi" || thinking != "hmm" {
		t.Errorf("text=%q thinking=%q", text, thinking)
	}
	if done == nil || done.Usage == nil || done.Usage.PromptTokens != 10 || done.Usage.CompletionTokens != 3 {
		t.Fatalf("usage wrong: %+v", done)
	}
	if done.Message.Text() != "Hi" || done.Message.Thinking() != "hmm" {
		t.Errorf("final message wrong: %+v", done.Message)
	}
}

func TestAnthropicStreamToolUse(t *testing.T) {
	body := "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tu1\",\"name\":\"read\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"\\\"x\\\"}\"}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	srv := sseServer(t, body)

	ad := Anthropic{}
	ch, _ := ad.Stream(context.Background(),
		Conn{BaseURL: srv.URL, APIKey: "k", Route: "anthropic"},
		models.TurnRequest{Model: models.ModelRef{Provider: "anthropic", ID: "claude-sonnet-4"}})
	evs := collect(t, ch)

	var done *Event
	for i := range evs {
		if evs[i].Kind == KindDone {
			done = &evs[i]
		}
	}
	calls := done.Message.ToolCalls()
	if len(calls) != 1 || calls[0].ID != "tu1" || calls[0].Name != "read" {
		t.Fatalf("tool call meta wrong: %+v", calls)
	}
	if calls[0].Arguments["path"] != "x" {
		t.Errorf("args=%+v", calls[0].Arguments)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/llm/provider/ -run TestAnthropicStream -v`
Expected: FAIL — `Anthropic` undefined.

- [ ] **Step 3: Write anthropic.go**

```go
// pkg/llm/provider/anthropic.go
package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/lcoder/lcoder/pkg/models"
)

// Anthropic is the adapter for the Anthropic Messages API.
type Anthropic struct{}

// anthropicBlock tracks one content block (text / thinking / tool_use) by index.
type anthropicBlock struct {
	kind string // text | thinking | tool_use
	id   string
	name string
	text strings.Builder // text or thinking content
	json strings.Builder // tool_use input json
}

func (Anthropic) Stream(ctx context.Context, conn Conn, req models.TurnRequest) (<-chan Event, error) {
	body := map[string]any{
		"model":      req.Model.ID,
		"messages":   anthropicMessages(req.Messages),
		"stream":     true,
		"max_tokens": anthropicMaxTokens(req.Generation.MaxTokens),
	}
	if req.SystemPrompt != "" {
		body["system"] = anthropicSystem(req)
	}
	if tools := anthropicTools(req.Tools); tools != nil {
		body["tools"] = tools
	}
	if req.Generation.Temperature != 0 {
		body["temperature"] = req.Generation.Temperature
	}
	if req.Generation.TopP != 0 {
		body["top_p"] = req.Generation.TopP
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	url := ResolveBaseURL(conn) + "/messages"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	if conn.APIKey != "" {
		httpReq.Header.Set("x-api-key", conn.APIKey)
	}
	for k, v := range conn.Headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, err
	}

	out := make(chan Event, 16)
	go func() {
		defer close(out)
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			data, _ := io.ReadAll(resp.Body)
			emit(ctx, out, Event{Kind: KindError, Err: classifyHTTP(resp.StatusCode, data)})
			return
		}

		emit(ctx, out, Event{Kind: KindStart})

		blocks := map[int]*anthropicBlock{}
		usage := &models.LLMUsage{}

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			var ev anthropicEvent
			if err := json.Unmarshal([]byte(payload), &ev); err != nil {
				continue
			}
			switch ev.Type {
			case "message_start":
				if ev.Message != nil {
					applyAnthropicUsage(usage, ev.Message.Usage)
				}
			case "content_block_start":
				b := &anthropicBlock{kind: ev.ContentBlock.Type, id: ev.ContentBlock.ID, name: ev.ContentBlock.Name}
				blocks[ev.Index] = b
			case "content_block_delta":
				b := blocks[ev.Index]
				if b == nil {
					b = &anthropicBlock{}
					blocks[ev.Index] = b
				}
				switch ev.Delta.Type {
				case "text_delta":
					b.text.WriteString(ev.Delta.Text)
					emit(ctx, out, Event{Kind: KindTextDelta, Delta: ev.Delta.Text})
				case "thinking_delta":
					b.text.WriteString(ev.Delta.Thinking)
					emit(ctx, out, Event{Kind: KindThinkingDelta, Delta: ev.Delta.Thinking})
				case "input_json_delta":
					b.json.WriteString(ev.Delta.PartialJSON)
					emit(ctx, out, Event{Kind: KindToolCallDelta, ToolCallIndex: ev.Index, ArgumentsJSON: ev.Delta.PartialJSON})
				}
			case "message_delta":
				applyAnthropicUsage(usage, ev.Usage)
			case "message_stop":
				// handled after loop
			case "error":
				emit(ctx, out, Event{Kind: KindError, Err: &EventError{Code: "internal", Message: payload}})
				return
			}
		}
		if err := scanner.Err(); err != nil {
			emit(ctx, out, Event{Kind: KindError, Err: &EventError{Code: "internal", Message: err.Error()}})
			return
		}

		emit(ctx, out, Event{Kind: KindDone, Message: finalizeAnthropic(blocks), Usage: usage})
	}()
	return out, nil
}

func anthropicMaxTokens(n int) int {
	if n <= 0 {
		return 4096
	}
	return n
}

// finalizeAnthropic assembles the finished message in block-index order.
func finalizeAnthropic(blocks map[int]*anthropicBlock) models.AgentMessage {
	msg := models.NewAgentMessage(models.RoleAssistant)
	idxs := make([]int, 0, len(blocks))
	for i := range blocks {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	var parts []models.ContentPart
	for _, i := range idxs {
		b := blocks[i]
		switch b.kind {
		case "thinking":
			if b.text.Len() > 0 {
				parts = append(parts, models.ThinkingContent{Text: b.text.String()})
			}
		case "text":
			if b.text.Len() > 0 {
				parts = append(parts, models.TextContent{Text: b.text.String()})
			}
		case "tool_use":
			args := map[string]any{}
			if raw := b.json.String(); raw != "" {
				if err := json.Unmarshal([]byte(raw), &args); err != nil {
					args = map[string]any{"__error__": raw}
				}
			}
			id := b.id
			if id == "" {
				id = fmt.Sprintf("call_%d", i)
			}
			parts = append(parts, models.ToolCallContent{ID: id, Name: b.name, Arguments: args})
		}
	}
	msg.Content = parts
	return msg
}

func applyAnthropicUsage(u *models.LLMUsage, src *anthropicUsage) {
	if src == nil {
		return
	}
	if src.InputTokens != 0 {
		u.PromptTokens = src.InputTokens
	}
	if src.OutputTokens != 0 {
		u.CompletionTokens = src.OutputTokens
	}
	if src.CacheReadInputTokens != 0 {
		u.CacheReadTokens = src.CacheReadInputTokens
	}
	if src.CacheCreationInputTokens != 0 {
		u.CacheWriteTokens = src.CacheCreationInputTokens
	}
	u.TotalTokens = u.PromptTokens + u.CompletionTokens
}

// --- event decoding ---

type anthropicEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		Usage *anthropicUsage `json:"usage"`
	} `json:"message"`
	ContentBlock struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		PartialJSON string `json:"partial_json"`
	} `json:"delta"`
	Usage *anthropicUsage `json:"usage"`
}

type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}
```

- [ ] **Step 4: Write anthropic request-body helpers (in anthropic.go)**

Append to `anthropic.go`. `anthropicMessages` mirrors `openAIMessages` but uses Anthropic's content shape: user/assistant with `content` arrays; tool results become a user message carrying a `tool_result` block; tool calls become `tool_use` blocks. `anthropicSystem`/`anthropicTools` build the system + tools fields. Cache control (Task 5) is applied by mutating these structures before marshalling.

```go
// anthropicMessages converts agent messages to Anthropic message blocks.
func anthropicMessages(msgs []models.AgentMessage) []map[string]any {
	out := []map[string]any{}
	for _, m := range msgs {
		switch m.Role {
		case models.RoleUser:
			out = append(out, map[string]any{"role": "user", "content": anthropicUserContent(m.Content)})
		case models.RoleAssistant:
			out = append(out, map[string]any{"role": "assistant", "content": anthropicAssistantContent(m.Content)})
		case models.RoleToolResult:
			for _, p := range m.Content {
				r, ok := p.(models.ToolResultContent)
				if !ok {
					continue
				}
				text := ""
				for _, child := range r.Content {
					if t, ok := child.(models.TextContent); ok {
						text += t.Text
					}
				}
				out = append(out, map[string]any{
					"role": "user",
					"content": []map[string]any{{
						"type":        "tool_result",
						"tool_use_id": r.ToolCallID,
						"content":     text,
					}},
				})
			}
		}
	}
	return out
}

func anthropicUserContent(parts []models.ContentPart) []map[string]any {
	out := []map[string]any{}
	for _, p := range parts {
		switch c := p.(type) {
		case models.TextContent:
			if c.Text != "" {
				out = append(out, map[string]any{"type": "text", "text": c.Text})
			}
		case models.ImageContent:
			if c.Data != "" {
				mime := c.MimeType
				if mime == "" {
					mime = "image/jpeg"
				}
				out = append(out, map[string]any{
					"type":   "image",
					"source": map[string]any{"type": "base64", "media_type": mime, "data": c.Data},
				})
			}
		}
	}
	return out
}

func anthropicAssistantContent(parts []models.ContentPart) []map[string]any {
	out := []map[string]any{}
	for _, p := range parts {
		switch c := p.(type) {
		case models.TextContent:
			if c.Text != "" {
				out = append(out, map[string]any{"type": "text", "text": c.Text})
			}
		case models.ToolCallContent:
			args := c.Arguments
			if args == nil {
				args = map[string]any{}
			}
			out = append(out, map[string]any{"type": "tool_use", "id": c.ID, "name": c.Name, "input": args})
		}
	}
	return out
}

// anthropicSystem returns the system field: a plain string, or a blocks array
// when cache control marks it (set by applyCachePolicy via req metadata).
func anthropicSystem(req models.TurnRequest) any {
	return req.SystemPrompt
}

func anthropicTools(tools []models.ToolDefinition) []map[string]any {
	if len(tools) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{
			"name":         t.Name,
			"description":  t.Description,
			"input_schema": t.Parameters,
		})
	}
	return out
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./pkg/llm/provider/ -run TestAnthropicStream -v`
Expected: PASS (text/thinking/usage + tool_use accumulation).

- [ ] **Step 6: Commit**

```bash
git add pkg/llm/provider/anthropic.go pkg/llm/provider/anthropic_test.go
git commit -m "feat(llm): hand-written Anthropic Messages streaming adapter"
```

---

## Task 5: Cache policy (Anthropic ephemeral cache_control)

**Files:**
- Create: `pkg/llm/provider/cachepolicy.go`
- Test: `pkg/llm/provider/cachepolicy_test.go`

Ports `apply_cache_policy`. Lives in the `provider` package (leaf) because the Anthropic adapter is its only consumer — this avoids any import cycle. The engine computes marks via `provider.ComputeCacheMarks` and hands them to the Anthropic adapter.

- [ ] **Step 1: Write the failing test**

```go
// pkg/llm/provider/cachepolicy_test.go
package provider

import "testing"

func TestCacheMarksDisabledWhenNone(t *testing.T) {
	marks := ComputeCacheMarks("none", []int{1}, 3, true)
	if marks.System || len(marks.MessageIdx) != 0 || marks.LastTool {
		t.Fatalf("expected no marks when cache=none, got %+v", marks)
	}
}

func TestCacheMarksDisabledWhenNotAnthropic(t *testing.T) {
	marks := ComputeCacheMarks("auto", []int{1}, 3, false)
	if marks.System || len(marks.MessageIdx) != 0 {
		t.Fatalf("expected no marks for non-anthropic, got %+v", marks)
	}
}

func TestCacheMarksUsesBreakpoints(t *testing.T) {
	marks := ComputeCacheMarks("auto", []int{0, 2}, 3, true)
	if !marks.System || !marks.LastTool {
		t.Fatalf("expected system+lastTool marks, got %+v", marks)
	}
	if len(marks.MessageIdx) != 2 || marks.MessageIdx[0] != 0 || marks.MessageIdx[1] != 2 {
		t.Fatalf("breakpoints wrong: %+v", marks.MessageIdx)
	}
}

func TestCacheMarksFallbackLastMsg(t *testing.T) {
	marks := ComputeCacheMarks("auto", nil, 4, true)
	if len(marks.MessageIdx) != 1 || marks.MessageIdx[0] != 3 {
		t.Fatalf("expected fallback to last index 3, got %+v", marks.MessageIdx)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/llm/provider/ -run TestCacheMarks -v`
Expected: FAIL — `ComputeCacheMarks` undefined.

- [ ] **Step 3: Write cachepolicy.go**

```go
// pkg/llm/provider/cachepolicy.go
package provider

// CacheMarks describes where Anthropic ephemeral cache_control should be applied
// for a turn. Computed by the engine, consumed by the Anthropic adapter.
type CacheMarks struct {
	System     bool  // mark the system block cacheable
	LastTool   bool  // mark the last tool definition cacheable
	MessageIdx []int // message indices whose first text block is cacheable
}

// ComputeCacheMarks ports apply_cache_policy: Anthropic-only ephemeral caching.
// cache=="none" or a non-Anthropic provider disables it. Explicit breakpoints
// are used as-is; otherwise it falls back to the last message so at least one
// breakpoint exists.
func ComputeCacheMarks(cache string, breakpoints []int, msgCount int, anthropic bool) CacheMarks {
	if cache == "none" || !anthropic {
		return CacheMarks{}
	}
	m := CacheMarks{System: true, LastTool: true}
	if len(breakpoints) > 0 {
		for _, b := range breakpoints {
			if b >= 0 && b < msgCount {
				m.MessageIdx = append(m.MessageIdx, b)
			}
		}
	} else if msgCount > 0 {
		m.MessageIdx = []int{msgCount - 1}
	}
	return m
}
```

> **Adapter wiring (apply during Task 9):** give `Anthropic` an exported field `Marks CacheMarks` (struct literal, no constructor needed). In `Stream`, when `a.Marks.System`, build `system` as `[]map[string]any{{"type":"text","text":req.SystemPrompt,"cache_control":map[string]any{"type":"ephemeral"}}}`; when `a.Marks.LastTool` and tools exist, add `"cache_control":{"type":"ephemeral"}` to the last tool map; for each `i` in `a.Marks.MessageIdx`, add `cache_control` to the first text block of message `i`'s content. The engine sets `Anthropic{Marks: provider.ComputeCacheMarks(...)}` per turn.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/llm/provider/ -run TestCacheMarks -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/llm/provider/cachepolicy.go pkg/llm/provider/cachepolicy_test.go
git commit -m "feat(llm): port Anthropic cache policy to provider package"
```

---

## Task 6: Pricing (leaf package)

**Files:**
- Create: `pkg/llm/pricing/pricing.go`
- Test: `pkg/llm/pricing/pricing_test.go`

Ports `estimate_cost` into a leaf package `pricing` (imports nothing but stdlib) so both `catalog` and `engine` can use it without a cycle.

- [ ] **Step 1: Write the failing test**

```go
// pkg/llm/pricing/pricing_test.go
package pricing

import (
	"math"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestEstimateCostKnownModel(t *testing.T) {
	c := EstimateCost(map[string]ModelPrice{
		"openai/gpt-4o": {Prompt: 2.50, Completion: 10.00, CacheRead: 1.25, CacheWrite: 2.50},
	}, "openai", "gpt-4o", 1_000_000, 500_000, 0, 0)
	if !approx(c.PromptCost, 2.50) || !approx(c.CompletionCost, 5.00) {
		t.Fatalf("costs wrong: %+v", c)
	}
	if !approx(c.TotalCost, 7.50) {
		t.Fatalf("total wrong: %v", c.TotalCost)
	}
}

func TestEstimateCostUnknownModelZero(t *testing.T) {
	c := EstimateCost(nil, "x", "y", 1000, 1000, 0, 0)
	if c.TotalCost != 0 {
		t.Fatalf("unknown model should cost 0, got %v", c.TotalCost)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/llm/pricing/ -run TestEstimateCost -v`
Expected: FAIL — package does not compile.

- [ ] **Step 3: Write pricing.go**

```go
// pkg/llm/pricing/pricing.go
package pricing

// ModelPrice holds per-1M-token USD prices for one model.
type ModelPrice struct {
	Prompt     float64
	Completion float64
	CacheRead  float64
	CacheWrite float64
}

// CostBreakdown is the per-turn cost split.
type CostBreakdown struct {
	PromptCost     float64
	CompletionCost float64
	CacheReadCost  float64
	CacheWriteCost float64
	TotalCost      float64
}

// EstimateCost ports estimate_cost: tokens * price_per_1M / 1e6 across four
// tiers. Unknown models (no table entry) cost 0, matching current behavior.
func EstimateCost(table map[string]ModelPrice, provider, modelID string,
	promptTokens, completionTokens, cacheReadTokens, cacheWriteTokens int) CostBreakdown {
	key := provider + "/" + modelID
	p, ok := table[key]
	if !ok {
		return CostBreakdown{}
	}
	cost := func(tokens int, per1M float64) float64 {
		return float64(tokens) * per1M / 1_000_000
	}
	b := CostBreakdown{
		PromptCost:     cost(promptTokens, p.Prompt),
		CompletionCost: cost(completionTokens, p.Completion),
		CacheReadCost:  cost(cacheReadTokens, p.CacheRead),
		CacheWriteCost: cost(cacheWriteTokens, p.CacheWrite),
	}
	b.TotalCost = b.PromptCost + b.CompletionCost + b.CacheReadCost + b.CacheWriteCost
	return b
}

// DefaultPricing mirrors the built-in PRICING table from pricing.py. The catalog
// overlay can add or override entries.
func DefaultPricing() map[string]ModelPrice {
	return map[string]ModelPrice{
		"openai/gpt-4o":                      {Prompt: 2.50, Completion: 10.00, CacheRead: 1.25, CacheWrite: 2.50},
		"openai/gpt-4o-mini":                 {Prompt: 0.15, Completion: 0.60, CacheRead: 0.075, CacheWrite: 0.15},
		"anthropic/claude-sonnet-4-20250514": {Prompt: 3.00, Completion: 15.00, CacheRead: 0.30, CacheWrite: 3.75},
		"deepseek/deepseek-chat":             {Prompt: 0.27, Completion: 1.10, CacheRead: 0.10, CacheWrite: 0.27},
		"deepseek/deepseek-reasoner":         {Prompt: 0.55, Completion: 2.19, CacheRead: 0.14, CacheWrite: 0.55},
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/llm/pricing/ -run TestEstimateCost -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/llm/pricing/pricing.go pkg/llm/pricing/pricing_test.go
git commit -m "feat(llm): cost estimation in leaf pricing package"
```

---

## Task 7: Catalog — embedded snapshot load + lookup + models.yaml overlay

**Files:**
- Create: `pkg/llm/catalog/catalog.go`
- Create: `pkg/llm/catalog/snapshot.json`
- Test: `pkg/llm/catalog/catalog_test.go`

The catalog serves `[]models.ModelInfo` plus per-model context window and pricing. Three layers, priority **models.yaml > models.dev > snapshot**. This task covers snapshot load + lookup + overlay; the network refresh is Task 8. Imports `pricing` (leaf) and `models` only.

`snapshot.json` is a JSON array of entries; bundle a minimal real subset (the 6 providers' headline models) so offline mode works. Schema:

```json
[
  {"id":"gpt-4o","name":"GPT-4o","provider":"openai","context_window":128000,
   "capabilities":["tools","vision"],
   "cost":{"prompt":2.50,"completion":10.00,"cache_read":1.25,"cache_write":2.50}},
  {"id":"claude-sonnet-4-20250514","name":"Claude Sonnet 4","provider":"anthropic","context_window":200000,
   "capabilities":["tools","vision"],
   "cost":{"prompt":3.00,"completion":15.00,"cache_read":0.30,"cache_write":3.75}},
  {"id":"deepseek-chat","name":"DeepSeek Chat","provider":"deepseek","context_window":64000,
   "capabilities":["tools"],"cost":{"prompt":0.27,"completion":1.10,"cache_read":0.10,"cache_write":0.27}},
  {"id":"deepseek-reasoner","name":"DeepSeek Reasoner","provider":"deepseek","context_window":64000,
   "capabilities":["tools","reasoning"],"cost":{"prompt":0.55,"completion":2.19,"cache_read":0.14,"cache_write":0.55}},
  {"id":"moonshot-v1-128k","name":"Moonshot v1 128k","provider":"moonshot","context_window":128000,
   "capabilities":["tools"],"cost":{"prompt":0,"completion":0,"cache_read":0,"cache_write":0}},
  {"id":"gemini-2.5-pro","name":"Gemini 2.5 Pro","provider":"gemini","context_window":1000000,
   "capabilities":["tools","vision"],"cost":{"prompt":0,"completion":0,"cache_read":0,"cache_write":0}}
]
```

- [ ] **Step 1: Write the failing test**

```go
// pkg/llm/catalog/catalog_test.go
package catalog

import "testing"

func TestSnapshotLoads(t *testing.T) {
	c := New(Options{Refresh: false})
	list := c.List()
	if len(list) < 6 {
		t.Fatalf("expected >=6 snapshot models, got %d", len(list))
	}
}

func TestWindowExactAndPrefix(t *testing.T) {
	c := New(Options{Refresh: false})
	if w := c.Window("openai", "gpt-4o"); w != 128000 {
		t.Errorf("gpt-4o window=%d", w)
	}
	// Dated Anthropic id resolves by prefix to the base catalog entry.
	if w := c.Window("anthropic", "claude-sonnet-4-20250514"); w != 200000 {
		t.Errorf("sonnet window=%d", w)
	}
}

func TestPriceTable(t *testing.T) {
	c := New(Options{Refresh: false})
	pt := c.PriceTable()
	if p, ok := pt["openai/gpt-4o"]; !ok || p.Prompt != 2.50 {
		t.Fatalf("price table missing gpt-4o: %+v", pt["openai/gpt-4o"])
	}
}

func TestOverrideWins(t *testing.T) {
	c := New(Options{Refresh: false, Overrides: []Entry{
		{ID: "gpt-4o", Provider: "openai", ContextWindow: 999},
	}})
	if w := c.Window("openai", "gpt-4o"); w != 999 {
		t.Errorf("override window=%d, want 999", w)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/llm/catalog/ -run TestSnapshot -v`
Expected: FAIL — package does not compile (`New` undefined).

- [ ] **Step 3: Write catalog.go (snapshot + lookup; refresh stubbed)**

```go
// pkg/llm/catalog/catalog.go
package catalog

import (
	_ "embed"
	"encoding/json"
	"strings"
	"sync"

	"github.com/lcoder/lcoder/pkg/llm/pricing"
	"github.com/lcoder/lcoder/pkg/models"
)

//go:embed snapshot.json
var snapshotJSON []byte

// Entry is one catalog model record (snapshot/models.dev shape).
type Entry struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Provider      string   `json:"provider"`
	ContextWindow int      `json:"context_window"`
	Capabilities  []string `json:"capabilities"`
	Cost          struct {
		Prompt     float64 `json:"prompt"`
		Completion float64 `json:"completion"`
		CacheRead  float64 `json:"cache_read"`
		CacheWrite float64 `json:"cache_write"`
	} `json:"cost"`
}

// Options configures catalog construction.
type Options struct {
	Refresh   bool    // enable models.dev background refresh (Task 8)
	CachePath string  // ~/.lcoder/cache/models.json (Task 8)
	Overrides []Entry // from models.yaml (highest priority)
}

// Catalog holds merged model entries keyed by "provider/id".
type Catalog struct {
	mu      sync.RWMutex
	entries map[string]Entry
	order   []string
}

// New builds a catalog from the embedded snapshot, applies overrides, and (if
// Options.Refresh) kicks off a non-blocking background refresh.
func New(opts Options) *Catalog {
	c := &Catalog{entries: map[string]Entry{}}
	var snap []Entry
	_ = json.Unmarshal(snapshotJSON, &snap)
	c.merge(snap)
	c.merge(opts.Overrides)
	if opts.Refresh {
		go c.refresh(opts.CachePath) // defined in Task 8
	}
	return c
}

func (c *Catalog) merge(entries []Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range entries {
		key := e.Provider + "/" + e.ID
		if _, exists := c.entries[key]; !exists {
			c.order = append(c.order, key)
		}
		c.entries[key] = e
	}
}

// List returns all models as ModelInfo in stable insertion order.
func (c *Catalog) List() []models.ModelInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]models.ModelInfo, 0, len(c.order))
	for _, key := range c.order {
		e := c.entries[key]
		out = append(out, models.ModelInfo{
			ID:            e.ID,
			Provider:      e.Provider,
			Capabilities:  e.Capabilities,
			ContextWindow: e.ContextWindow,
		})
		// Name is populated once models.ModelInfo gains the field (Task 8).
	}
	return out
}

// Window returns the context window for provider/model: exact match first, then
// a prefix match (either direction) so dated variants resolve. 0 if unknown.
func (c *Catalog) Window(provider, model string) int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if e, ok := c.entries[provider+"/"+model]; ok && e.ContextWindow > 0 {
		return e.ContextWindow
	}
	for _, key := range c.order {
		e := c.entries[key]
		if e.Provider != provider || e.ContextWindow <= 0 {
			continue
		}
		if strings.HasPrefix(e.ID, model) || strings.HasPrefix(model, e.ID) {
			return e.ContextWindow
		}
	}
	return 0
}

// PriceTable returns a pricing table for pricing.EstimateCost, catalog entries
// overlaid on the built-in defaults.
func (c *Catalog) PriceTable() map[string]pricing.ModelPrice {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := pricing.DefaultPricing()
	for key, e := range c.entries {
		if e.Cost.Prompt == 0 && e.Cost.Completion == 0 {
			continue
		}
		out[key] = pricing.ModelPrice{
			Prompt: e.Cost.Prompt, Completion: e.Cost.Completion,
			CacheRead: e.Cost.CacheRead, CacheWrite: e.Cost.CacheWrite,
		}
	}
	return out
}
```

- [ ] **Step 4: Create snapshot.json**

Write the JSON array shown above to `pkg/llm/catalog/snapshot.json`.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./pkg/llm/catalog/ -run 'TestSnapshot|TestWindow|TestPrice|TestOverride' -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add pkg/llm/catalog/catalog.go pkg/llm/catalog/snapshot.json pkg/llm/catalog/catalog_test.go
git commit -m "feat(llm): catalog snapshot load, window lookup, price table"
```

---

## Task 8: Add `Name` to `models.ModelInfo` and populate it in the catalog

**Files:**
- Modify: `pkg/models/message.go:317-324`
- Modify: `pkg/llm/catalog/catalog.go` (`List()`)
- Test: `pkg/models/message_test.go` (add one test)

`ModelInfo` needs a human display name (`name` from models.dev) for the model picker. This is additive; existing JSON stays compatible.

- [ ] **Step 1: Write the failing test**

```go
// append to pkg/models/message_test.go
func TestModelInfoNameRoundTrips(t *testing.T) {
	in := ModelInfo{ID: "gpt-4o", Name: "GPT-4o", Provider: "openai", ContextWindow: 128000}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out ModelInfo
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Name != "GPT-4o" {
		t.Fatalf("Name did not round-trip: %q", out.Name)
	}
}
```

> If `pkg/models/message_test.go` does not yet import `encoding/json`/`testing`, add them. If the file does not exist, create it with `package models` and those imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/models/ -run TestModelInfoNameRoundTrips -v`
Expected: FAIL — `unknown field Name in struct literal`.

- [ ] **Step 3: Add the field**

In `pkg/models/message.go`, change the `ModelInfo` struct to:

```go
// ModelInfo describes a model available via the Gateway.
type ModelInfo struct {
	ID            string   `json:"id"`
	Name          string   `json:"name,omitempty"`
	Provider      string   `json:"provider"`
	Aliases       []string `json:"aliases,omitempty"`
	Capabilities  []string `json:"capabilities"`
	ContextWindow int      `json:"context_window"`
}
```

- [ ] **Step 4: Populate it in catalog.List**

In `pkg/llm/catalog/catalog.go`, change the `List()` append block to set `Name` and remove the placeholder comment:

```go
		out = append(out, models.ModelInfo{
			ID:            e.ID,
			Name:          e.Name,
			Provider:      e.Provider,
			Capabilities:  e.Capabilities,
			ContextWindow: e.ContextWindow,
		})
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./pkg/models/ ./pkg/llm/catalog/ -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add pkg/models/message.go pkg/models/message_test.go pkg/llm/catalog/catalog.go
git commit -m "feat(models): add display Name to ModelInfo, populate from catalog"
```

---

## Task 9: Catalog models.dev background refresh

**Files:**
- Modify: `pkg/llm/catalog/catalog.go` (add fields to `Catalog`/`Options`, store overrides+sourceURL in `New`, implement `refresh`)
- Test: `pkg/llm/catalog/refresh_test.go`

Implements the `refresh(cachePath)` stub referenced in Task 7. Fetches `https://models.dev/api.json`, caches to `~/.lcoder/cache/models.json` with a 5-minute TTL, fully non-blocking (already launched in a goroutine by `New`), snapshot fallback on any failure. Refresh re-asserts `models.yaml` overrides on top so priority stays **overrides > models.dev > snapshot**.

- [ ] **Step 1: Write the failing test**

```go
// pkg/llm/catalog/refresh_test.go
package catalog

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/llm/catalog/ -run TestRefresh -v`
Expected: FAIL — `Options has no field SourceURL` / `c.sourceURL` undefined.

- [ ] **Step 3: Extend Catalog/Options and New**

In `catalog.go`, add two fields to `Options`:

```go
type Options struct {
	Refresh   bool    // enable models.dev background refresh
	CachePath string  // ~/.lcoder/cache/models.json
	SourceURL string  // models.dev endpoint (default https://models.dev/api.json)
	Overrides []Entry // from models.yaml (highest priority)
}
```

Add two fields to `Catalog`:

```go
type Catalog struct {
	mu        sync.RWMutex
	entries   map[string]Entry
	order     []string
	overrides []Entry
	sourceURL string
}
```

Change `New` to store them:

```go
func New(opts Options) *Catalog {
	src := opts.SourceURL
	if src == "" {
		src = modelsDevURL
	}
	c := &Catalog{entries: map[string]Entry{}, overrides: opts.Overrides, sourceURL: src}
	var snap []Entry
	_ = json.Unmarshal(snapshotJSON, &snap)
	c.merge(snap)
	c.merge(opts.Overrides)
	if opts.Refresh {
		go c.refresh(opts.CachePath)
	}
	return c
}
```

- [ ] **Step 4: Implement refresh + fetch + cache**

Add to `catalog.go` (and extend the import block with `fmt`, `net/http`, `os`, `path/filepath`, `time`):

```go
const (
	modelsDevURL = "https://models.dev/api.json"
	cacheTTL     = 5 * time.Minute
)

// refresh loads models.dev data (from a fresh cache if within TTL, else over the
// network), merges it under the user overrides, and rewrites the cache. Any
// failure is swallowed: the embedded snapshot remains in effect.
func (c *Catalog) refresh(cachePath string) {
	if cachePath != "" {
		if info, err := os.Stat(cachePath); err == nil && time.Since(info.ModTime()) < cacheTTL {
			if data, err := os.ReadFile(cachePath); err == nil {
				var ents []Entry
				if json.Unmarshal(data, &ents) == nil && len(ents) > 0 {
					c.applyRefresh(ents)
					return
				}
			}
		}
	}
	ents, err := fetchModelsDev(c.sourceURL)
	if err != nil || len(ents) == 0 {
		return
	}
	if cachePath != "" {
		if data, err := json.Marshal(ents); err == nil {
			_ = os.MkdirAll(filepath.Dir(cachePath), 0o755)
			_ = os.WriteFile(cachePath, data, 0o644)
		}
	}
	c.applyRefresh(ents)
}

// applyRefresh merges models.dev entries, then re-asserts user overrides on top.
func (c *Catalog) applyRefresh(ents []Entry) {
	c.merge(ents)
	c.merge(c.overrides)
}

// fetchModelsDev fetches and flattens the models.dev api.json into []Entry.
func fetchModelsDev(url string) ([]Entry, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models.dev returned %d", resp.StatusCode)
	}
	var raw map[string]struct {
		Models map[string]struct {
			Name  string `json:"name"`
			Limit struct {
				Context int `json:"context"`
			} `json:"limit"`
			Cost struct {
				Input      float64 `json:"input"`
				Output     float64 `json:"output"`
				CacheRead  float64 `json:"cache_read"`
				CacheWrite float64 `json:"cache_write"`
			} `json:"cost"`
			ToolCall  bool `json:"tool_call"`
			Reasoning bool `json:"reasoning"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	var out []Entry
	for provID, p := range raw {
		for modelID, m := range p.Models {
			e := Entry{ID: modelID, Name: m.Name, Provider: provID, ContextWindow: m.Limit.Context}
			if m.ToolCall {
				e.Capabilities = append(e.Capabilities, "tools")
			}
			if m.Reasoning {
				e.Capabilities = append(e.Capabilities, "reasoning")
			}
			e.Cost.Prompt = m.Cost.Input
			e.Cost.Completion = m.Cost.Output
			e.Cost.CacheRead = m.Cost.CacheRead
			e.Cost.CacheWrite = m.Cost.CacheWrite
			out = append(out, e)
		}
	}
	return out, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./pkg/llm/catalog/ -v`
Expected: PASS (all catalog tests, including Task 7's).

- [ ] **Step 6: Commit**

```bash
git add pkg/llm/catalog/catalog.go pkg/llm/catalog/refresh_test.go
git commit -m "feat(llm): non-blocking models.dev catalog refresh with snapshot fallback"
```

---

## Task 10: Engine orchestration

**Files:**
- Create: `pkg/llm/engine/engine.go`
- Test: `pkg/llm/engine/engine_test.go`

The engine owns the provider registry, selects an adapter per turn, computes Anthropic cache marks, and fills `LLMUsage` cost fields on the `done` event. It exposes a `SetAdapterFactory` seam so tests (and `llmtest`, Task 12) can inject a fake adapter. Imports `catalog`+`provider`+`pricing`+`models`; nothing imports `engine` except package `llm`.

- [ ] **Step 1: Write the failing test**

```go
// pkg/llm/engine/engine_test.go
package engine

import (
	"context"
	"testing"

	"github.com/lcoder/lcoder/pkg/llm/catalog"
	"github.com/lcoder/lcoder/pkg/llm/provider"
	"github.com/lcoder/lcoder/pkg/models"
)

// fakeAdapter emits a fixed event script.
type fakeAdapter struct{ events []provider.Event }

func (f fakeAdapter) Stream(ctx context.Context, conn provider.Conn, req models.TurnRequest) (<-chan provider.Event, error) {
	ch := make(chan provider.Event, len(f.events))
	for _, e := range f.events {
		ch <- e
	}
	close(ch)
	return ch, nil
}

func TestEngineFillsCostOnDone(t *testing.T) {
	cat := catalog.New(catalog.Options{Refresh: false}) // gpt-4o priced 2.5/10 in snapshot
	eng := New(cat)
	eng.SetAdapterFactory(func(route string, marks provider.CacheMarks) provider.Adapter {
		return fakeAdapter{events: []provider.Event{
			{Kind: provider.KindTextDelta, Delta: "hi"},
			{Kind: provider.KindDone,
				Message: models.AgentMessage{Role: models.RoleAssistant},
				Usage:   &models.LLMUsage{PromptTokens: 1_000_000, CompletionTokens: 500_000}},
		}}
	})
	eng.RegisterProvider("openai", provider.Conn{Route: "openai"})

	ch, err := eng.StreamTurn(context.Background(), models.TurnRequest{
		Model: models.ModelRef{Provider: "openai", ID: "gpt-4o"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got *models.LLMUsage
	for ev := range ch {
		if ev.Kind == provider.KindDone {
			got = ev.Usage
		}
	}
	if got == nil {
		t.Fatal("no done event")
	}
	if got.TotalCost != 7.5 {
		t.Fatalf("cost not computed: got %v, want 7.5", got.TotalCost)
	}
	if got.Provider != "openai" || got.Model != "gpt-4o" {
		t.Fatalf("usage provider/model not stamped: %+v", got)
	}
}

func TestEngineRoutesAnthropicCacheMarks(t *testing.T) {
	cat := catalog.New(catalog.Options{Refresh: false})
	eng := New(cat)
	var gotMarks provider.CacheMarks
	eng.SetAdapterFactory(func(route string, marks provider.CacheMarks) provider.Adapter {
		gotMarks = marks
		return fakeAdapter{events: []provider.Event{{Kind: provider.KindDone,
			Message: models.AgentMessage{Role: models.RoleAssistant}}}}
	})
	eng.RegisterProvider("anthropic", provider.Conn{Route: "anthropic"})
	ch, _ := eng.StreamTurn(context.Background(), models.TurnRequest{
		Model:    models.ModelRef{Provider: "anthropic", ID: "claude-sonnet-4-20250514"},
		Messages: []models.AgentMessage{models.UserMessage("hi")},
	})
	for range ch {
	}
	if !gotMarks.System || len(gotMarks.MessageIdx) != 1 {
		t.Fatalf("anthropic cache marks not computed: %+v", gotMarks)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/llm/engine/ -v`
Expected: FAIL — package does not compile (`New` undefined).

- [ ] **Step 3: Write engine.go**

```go
// pkg/llm/engine/engine.go
package engine

import (
	"context"

	"github.com/lcoder/lcoder/pkg/llm/catalog"
	"github.com/lcoder/lcoder/pkg/llm/pricing"
	"github.com/lcoder/lcoder/pkg/llm/provider"
	"github.com/lcoder/lcoder/pkg/models"
)

// AdapterFactory builds an adapter for a route, given precomputed cache marks.
type AdapterFactory func(route string, marks provider.CacheMarks) provider.Adapter

// Engine routes turns to provider adapters in-process.
type Engine struct {
	providers  map[string]provider.Conn
	catalog    *catalog.Catalog
	newAdapter AdapterFactory
}

// New builds an engine over a catalog with the default adapter factory.
func New(cat *catalog.Catalog) *Engine {
	return &Engine{
		providers:  map[string]provider.Conn{},
		catalog:    cat,
		newAdapter: defaultAdapterFactory,
	}
}

func defaultAdapterFactory(route string, marks provider.CacheMarks) provider.Adapter {
	if route == "anthropic" {
		return provider.Anthropic{Marks: marks}
	}
	return provider.OpenAICompat{}
}

// SetAdapterFactory overrides adapter construction (used by tests / llmtest).
func (e *Engine) SetAdapterFactory(f AdapterFactory) { e.newAdapter = f }

// RegisterProvider stores or replaces an in-memory provider connection.
func (e *Engine) RegisterProvider(name string, conn provider.Conn) {
	e.providers[name] = conn
}

// ListModels returns the catalog's model list.
func (e *Engine) ListModels() []models.ModelInfo { return e.catalog.List() }

// ModelWindow returns the catalog context window for provider/model (0 if unknown).
func (e *Engine) ModelWindow(prov, model string) int { return e.catalog.Window(prov, model) }

func (e *Engine) resolveProvider(ref models.ModelRef) string {
	if ref.Provider != "" {
		return ref.Provider
	}
	for _, m := range e.catalog.List() {
		if m.ID == ref.ID {
			return m.Provider
		}
	}
	return ""
}

// StreamTurn selects an adapter, starts the provider stream, and returns a
// channel of normalized events with cost filled in on the done event.
func (e *Engine) StreamTurn(ctx context.Context, req models.TurnRequest) (<-chan provider.Event, error) {
	prov := e.resolveProvider(req.Model)
	conn := e.providers[prov]
	if conn.Route == "" {
		conn.Route = prov
	}
	anthropic := conn.Route == "anthropic"
	marks := provider.ComputeCacheMarks(req.Cache, req.CacheBreakpoints, len(req.Messages), anthropic)
	conn.BaseURL = provider.ResolveBaseURL(conn)

	adapter := e.newAdapter(conn.Route, marks)
	src, err := adapter.Stream(ctx, conn, req)
	if err != nil {
		return nil, err
	}
	out := make(chan provider.Event)
	go e.forward(prov, req.Model.ID, src, out)
	return out, nil
}

// forward copies events through, computing cost on the done event.
func (e *Engine) forward(prov, model string, src <-chan provider.Event, out chan<- provider.Event) {
	defer close(out)
	table := e.catalog.PriceTable()
	for ev := range src {
		if ev.Kind == provider.KindDone && ev.Usage != nil {
			u := ev.Usage
			u.Provider = prov
			u.Model = model
			cb := pricing.EstimateCost(table, prov, model,
				u.PromptTokens, u.CompletionTokens, u.CacheReadTokens, u.CacheWriteTokens)
			u.PromptCost = cb.PromptCost
			u.CompletionCost = cb.CompletionCost
			u.CacheReadCost = cb.CacheReadCost
			u.CacheWriteCost = cb.CacheWriteCost
			u.TotalCost = cb.TotalCost
		}
		out <- ev
	}
}
```

> **Depends on Tasks 1–7:** `provider.Conn{Route}`, `provider.ResolveBaseURL`, `provider.ComputeCacheMarks`, `provider.Anthropic{Marks}`, `provider.OpenAICompat`, `provider.Event{Kind,Usage,Message,...}`, `provider.KindDone/KindTextDelta`. If `provider.Anthropic`/`OpenAICompat` are not yet implemented when running this task in isolation, they exist from Tasks 3–4.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/llm/engine/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/llm/engine/engine.go pkg/llm/engine/engine_test.go
git commit -m "feat(llm): in-process engine routing, cache marks, cost"
```

---

## Task 11: Rewire `llm.Client` over the engine + channel-backed `TurnStream`

**Files:**
- Create: `pkg/llm/turnstream.go`
- Modify: `pkg/llm/client.go` (replace HTTP transport with engine delegation)
- Test: `pkg/llm/client_engine_test.go`

`llm.Client` keeps its method surface but is now constructed over an `*engine.Engine`. `TurnStream` becomes channel-backed. `client.go` maps `provider.Event` → `GatewayEvent` preserving the exact payload shapes from the Critical Contracts table.

> **Signature change:** `NewClient(baseURL string)` becomes `NewClient(eng *engine.Engine)`. This breaks ~25 test call sites across packages; they are migrated in Task 12. After this task, `go build ./...` succeeds but `go test ./...` does not compile yet — that is expected and resolved in Task 12. The old HTTP `StreamTurn`/SSE `TurnStream`/`ModelWindow`/`ListModels`/`RegisterProvider`/`Health` bodies in `client.go` are replaced (not appended).

- [ ] **Step 1: Write the failing test**

```go
// pkg/llm/client_engine_test.go
package llm_test

import (
	"context"
	"testing"

	"github.com/lcoder/lcoder/pkg/llm"
	"github.com/lcoder/lcoder/pkg/llm/catalog"
	"github.com/lcoder/lcoder/pkg/llm/engine"
	"github.com/lcoder/lcoder/pkg/llm/provider"
	"github.com/lcoder/lcoder/pkg/models"
)

func TestClientStreamMapsEvents(t *testing.T) {
	cat := catalog.New(catalog.Options{Refresh: false})
	eng := engine.New(cat)
	eng.SetAdapterFactory(func(route string, marks provider.CacheMarks) provider.Adapter {
		return scriptAdapter{[]provider.Event{
			{Kind: provider.KindStart},
			{Kind: provider.KindTextDelta, Delta: "hello"},
			{Kind: provider.KindDone, Message: models.AgentMessage{
				Role: models.RoleAssistant, Content: []models.ContentPart{models.TextContent{Text: "hello"}}}},
		}}
	})
	eng.RegisterProvider("openai", provider.Conn{Route: "openai"})
	c := llm.NewClient(eng)

	stream, err := c.StreamTurn(context.Background(), models.TurnRequest{
		Model: models.ModelRef{Provider: "openai", ID: "gpt-4o"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	var gotText string
	var sawDone bool
	for {
		ev, ok, err := stream.Next(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		switch ev.Type() {
		case "text_delta":
			gotText += ev.Payload["delta"].(string)
		case "done":
			sawDone = true
			msg, err := ev.FinalMessage()
			if err != nil || msg.Text() != "hello" {
				t.Fatalf("final message wrong: %v / %q", err, msg.Text())
			}
		}
	}
	if gotText != "hello" || !sawDone {
		t.Fatalf("stream mapping wrong: text=%q done=%v", gotText, sawDone)
	}
}

// scriptAdapter is a local fake. (Mirrors engine.fakeAdapter; kept local to avoid exporting.)
type scriptAdapter struct{ events []provider.Event }

func (s scriptAdapter) Stream(ctx context.Context, conn provider.Conn, req models.TurnRequest) (<-chan provider.Event, error) {
	ch := make(chan provider.Event, len(s.events))
	for _, e := range s.events {
		ch <- e
	}
	close(ch)
	return ch, nil
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/llm/ -run TestClientStreamMapsEvents -v`
Expected: FAIL — `NewClient` still takes a string / compile error.

- [ ] **Step 3: Write turnstream.go**

```go
// pkg/llm/turnstream.go
package llm

import "context"

// TurnStream yields normalized gateway events from an in-process engine stream.
type TurnStream struct {
	ch   <-chan GatewayEvent
	done bool
}

// Next returns the next event, or ok=false when the stream is exhausted.
func (s *TurnStream) Next(ctx context.Context) (GatewayEvent, bool, error) {
	if s.done {
		return GatewayEvent{}, false, nil
	}
	select {
	case <-ctx.Done():
		return GatewayEvent{}, false, ctx.Err()
	case ev, ok := <-s.ch:
		if !ok {
			s.done = true
			return GatewayEvent{}, false, nil
		}
		if ev.Name == "done" || ev.Name == "error" {
			s.done = true
		}
		return ev, true, nil
	}
}

// Close is a no-op for channel-backed streams (kept for interface compatibility).
func (s *TurnStream) Close() error { return nil }
```

- [ ] **Step 4: Rewrite client.go**

Replace the entire contents of `pkg/llm/client.go` with the version below. It keeps `GatewayEvent`/`GatewayError` and their methods unchanged, drops `GatewayHTTPError`/SSE/`bufio`/`net/http`, removes the old `TurnStream` (now in turnstream.go), and delegates to the engine. `mapEvent` is the contract-preserving translator.

```go
// pkg/llm/client.go
package llm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/llm/engine"
	"github.com/lcoder/lcoder/pkg/llm/provider"
	"github.com/lcoder/lcoder/pkg/models"
)

// Client is the in-process LLM client. It keeps the method surface the agent
// loop and TUI depend on, delegating to an in-process engine.
type Client struct {
	engine *engine.Engine
}

// NewClient creates a client over an in-process engine.
func NewClient(eng *engine.Engine) *Client {
	return &Client{engine: eng}
}

// StreamTurn starts a provider turn and returns a channel-backed event stream.
func (c *Client) StreamTurn(ctx context.Context, req models.TurnRequest) (*TurnStream, error) {
	src, err := c.engine.StreamTurn(ctx, req)
	if err != nil {
		return nil, err
	}
	out := make(chan GatewayEvent)
	go func() {
		defer close(out)
		for ev := range src {
			out <- mapEvent(ev)
		}
	}()
	return &TurnStream{ch: out}, nil
}

// mapEvent translates a provider.Event into a GatewayEvent with the exact
// payload shape the agent loop consumes (see Critical Contracts).
func mapEvent(ev provider.Event) GatewayEvent {
	switch ev.Kind {
	case provider.KindStart:
		return GatewayEvent{Name: "start", Payload: map[string]any{"type": "start"}}
	case provider.KindTextDelta:
		return GatewayEvent{Name: "text_delta", Payload: map[string]any{"type": "text_delta", "delta": ev.Delta}}
	case provider.KindThinkingDelta:
		return GatewayEvent{Name: "thinking_delta", Payload: map[string]any{"type": "thinking_delta", "delta": ev.Delta}}
	case provider.KindToolCallDelta:
		return GatewayEvent{Name: "toolcall_delta", Payload: map[string]any{
			"type":             "toolcall_delta",
			"tool_call_index":  ev.ToolCallIndex,
			"arguments_json":   ev.ArgumentsJSON,
		}}
	case provider.KindDone:
		payload := map[string]any{"type": "done"}
		// Message is a value type; on done it is always the finalized assistant
		// message, so emit it unconditionally.
		payload["message"] = jsonRoundTrip(ev.Message)
		if ev.Usage != nil {
			payload["usage"] = jsonRoundTrip(ev.Usage)
		}
		return GatewayEvent{Name: "done", Payload: payload}
	case provider.KindError:
		ge := GatewayError{Code: "internal", Message: "unknown error"}
		if ev.Err != nil {
			ge = GatewayError{Code: ev.Err.Code, Message: ev.Err.Message, ProviderError: ev.Err.ProviderError}
		}
		return GatewayEvent{Name: "error", Payload: map[string]any{"type": "error", "error": jsonRoundTrip(ge)}}
	default:
		return GatewayEvent{Name: "", Payload: map[string]any{}}
	}
}

// jsonRoundTrip converts a typed value into the map/any shape that
// GatewayEvent.FinalMessage/Usage/Error re-decode, so AgentMessage's custom
// MarshalJSON is honored.
func jsonRoundTrip(v any) any {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	_ = json.Unmarshal(data, &out)
	return out
}

// RegisterProvider stores a provider connection on the engine (in-process).
func (c *Client) RegisterProvider(ctx context.Context, name string, conn config.ProviderConn) error {
	c.engine.RegisterProvider(name, provider.Conn{
		BaseURL: conn.BaseURL,
		APIKey:  conn.APIKey,
		Route:   conn.Route,
		Headers: conn.Headers,
	})
	return nil
}

// ListModels returns the available models from the catalog.
func (c *Client) ListModels(ctx context.Context) ([]models.ModelInfo, error) {
	return c.engine.ListModels(), nil
}

// ModelWindow returns the catalog context window for provider/model (0 if unknown).
func (c *Client) ModelWindow(ctx context.Context, prov, model string) (int, error) {
	return c.engine.ModelWindow(prov, model), nil
}

// Health reports in-process readiness.
func (c *Client) Health(ctx context.Context) (map[string]string, error) {
	return map[string]string{"status": "ok"}, nil
}

// GatewayEvent is a normalized event from the engine.
type GatewayEvent struct {
	Name    string
	Raw     string
	Payload map[string]any
}

// Type returns the payload type field if present.
func (e GatewayEvent) Type() string {
	if t, ok := e.Payload["type"].(string); ok {
		return t
	}
	return ""
}

// Usage extracts LLM usage from a "done" event if present.
func (e GatewayEvent) Usage() (models.LLMUsage, bool) {
	usageAny, ok := e.Payload["usage"]
	if !ok {
		return models.LLMUsage{}, false
	}
	data, err := json.Marshal(usageAny)
	if err != nil {
		return models.LLMUsage{}, false
	}
	var usage models.LLMUsage
	if err := json.Unmarshal(data, &usage); err != nil {
		return models.LLMUsage{}, false
	}
	return usage, true
}

// FinalMessage extracts the final assistant message from a "done" event.
func (e GatewayEvent) FinalMessage() (models.AgentMessage, error) {
	msgAny, ok := e.Payload["message"]
	if !ok {
		return models.AgentMessage{}, fmt.Errorf("done event missing message")
	}
	data, err := json.Marshal(msgAny)
	if err != nil {
		return models.AgentMessage{}, err
	}
	var msg models.AgentMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return models.AgentMessage{}, err
	}
	return msg, nil
}

// Error extracts a GatewayError from an "error" event.
func (e GatewayEvent) Error() (GatewayError, bool) {
	errAny, ok := e.Payload["error"]
	if !ok {
		return GatewayError{}, false
	}
	data, err := json.Marshal(errAny)
	if err != nil {
		return GatewayError{}, false
	}
	var ge GatewayError
	if err := json.Unmarshal(data, &ge); err != nil {
		return GatewayError{}, false
	}
	return ge, true
}

// GatewayError is returned by the engine on failure.
type GatewayError struct {
	Code          string         `json:"code"`
	Message       string         `json:"message"`
	ProviderError map[string]any `json:"provider_error,omitempty"`
}

func (e GatewayError) Error() string {
	return fmt.Sprintf("gateway error %s: %s", e.Code, e.Message)
}
```

> **`GatewayHTTPError` removal:** `retry.go` (`StreamTurnRetry`/`DefaultRetryConfig`) may reference `GatewayHTTPError` for status-code classification. Since transport errors now surface as `GatewayError` (via the `error` event) or as the engine's returned `error`, grep `retry.go` for `GatewayHTTPError`; if present, switch its classification to inspect `GatewayError.Code` (`rate_limit`/`auth`/`internal`) and treat a non-nil returned `error` from `StreamTurn` as retryable `internal`. Keep `retry.go`'s public API (`StreamTurnRetry`, `DefaultRetryConfig`) unchanged. Run `go build ./pkg/llm/` and fix any remaining `GatewayHTTPError` reference before moving on.

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./pkg/llm/ -run TestClientStreamMapsEvents -v`
Expected: PASS. (Other `pkg/llm` tests still fail to compile until Task 12 — run this single test by name.)

- [ ] **Step 6: Verify the build**

Run: `go build ./...`
Expected: success (production code compiles; test files are not built by `go build`).

- [ ] **Step 7: Commit**

```bash
git add pkg/llm/client.go pkg/llm/turnstream.go pkg/llm/client_engine_test.go
git commit -m "refactor(llm): Client delegates to in-process engine, channel TurnStream"
```

---

## Task 12: Migrate all `NewClient(url)` call sites + delete SSE test scaffolding

**Files:**
- Create: `pkg/llm/llmtest/llmtest.go` (shared fake-engine client builder)
- Modify (tests): `pkg/llm/client_test.go`, `pkg/llm/register_test.go`, `pkg/llm/retry_test.go`, `pkg/llm/window_test.go`, `pkg/agent/loop_test.go`, `pkg/agent/builder_test.go`, `pkg/compaction/summarizer_test.go`, `pkg/tui/providerpanel_test.go`, `cmd/lcoder/lookup_test.go`

Every `llm.NewClient(<url>)` site currently spins up an httptest server emitting gateway SSE. Replace each with a client built over a fake-adapter engine. The `llmtest` helper centralizes construction so each site changes by a few lines.

- [ ] **Step 1: Write the llmtest helper**

```go
// pkg/llm/llmtest/llmtest.go
package llmtest

import (
	"context"

	"github.com/lcoder/lcoder/pkg/llm"
	"github.com/lcoder/lcoder/pkg/llm/catalog"
	"github.com/lcoder/lcoder/pkg/llm/engine"
	"github.com/lcoder/lcoder/pkg/llm/provider"
	"github.com/lcoder/lcoder/pkg/models"
)

// ScriptAdapter replays one event script per turn. Each StreamTurn call pops the
// next script; once exhausted it repeats the last one.
type ScriptAdapter struct {
	turns [][]provider.Event
	calls int
}

func (s *ScriptAdapter) Stream(ctx context.Context, conn provider.Conn, req models.TurnRequest) (<-chan provider.Event, error) {
	script := s.turns[len(s.turns)-1]
	if s.calls < len(s.turns) {
		script = s.turns[s.calls]
	}
	s.calls++
	ch := make(chan provider.Event, len(script))
	for _, e := range script {
		ch <- e
	}
	close(ch)
	return ch, nil
}

// Client builds an *llm.Client whose every turn is served by the given event
// scripts (one slice per turn). Provider "openai"/"anthropic"/"deepseek" are
// pre-registered so any test ModelRef resolves.
func Client(turns ...[]provider.Event) *llm.Client {
	cat := catalog.New(catalog.Options{Refresh: false})
	eng := engine.New(cat)
	adapter := &ScriptAdapter{turns: turns}
	eng.SetAdapterFactory(func(route string, marks provider.CacheMarks) provider.Adapter { return adapter })
	for _, p := range []string{"openai", "anthropic", "deepseek", "moonshot", "openrouter", "gemini"} {
		eng.RegisterProvider(p, provider.Conn{Route: p})
	}
	return llm.NewClient(eng)
}

// Helpers to build common events.
func Start() provider.Event  { return provider.Event{Kind: provider.KindStart} }
func Text(s string) provider.Event {
	return provider.Event{Kind: provider.KindTextDelta, Delta: s}
}
func ToolCall(index int, args string) provider.Event {
	return provider.Event{Kind: provider.KindToolCallDelta, ToolCallIndex: index, ArgumentsJSON: args}
}
func Done(msg models.AgentMessage, usage *models.LLMUsage) provider.Event {
	return provider.Event{Kind: provider.KindDone, Message: msg, Usage: usage}
}
func ErrorEvent(code, message string) provider.Event {
	return provider.Event{Kind: provider.KindError, Err: &provider.EventError{Code: code, Message: message}}
}
```

- [ ] **Step 2: Migrate `pkg/llm` in-package tests**

These previously used `NewClient(srv.URL)` with an httptest server. Rewrite each to build the client over a scripted engine. Example transformation for `client_test.go` (apply the same pattern to `register_test.go`, `retry_test.go`, `window_test.go`):

Before:
```go
client := NewClient(ts.URL) // ts is an httptest server emitting SSE
```
After (in-package, so construct the engine directly):
```go
cat := catalog.New(catalog.Options{Refresh: false})
eng := engine.New(cat)
eng.SetAdapterFactory(func(route string, marks provider.CacheMarks) provider.Adapter {
	return scriptAdapter{events: []provider.Event{ /* the events this test asserts on */ }}
})
eng.RegisterProvider("openai", provider.Conn{Route: "openai"})
client := NewClient(eng)
```

Concretely:
- `register_test.go`: `RegisterProvider` no longer makes an HTTP POST. Assert it stores the conn — change the test to register then drive a turn whose adapter reads `conn`, or assert via a turn. Simplest: assert `RegisterProvider` returns nil and a subsequent `StreamTurn` succeeds. Delete the httptest server and the POST-body assertions.
- `retry_test.go`: previously returned `GatewayHTTPError`/500 from the server to exercise retry. Now drive retries via a `ScriptAdapter` that errors on the first call and succeeds on the second, or returns `ErrorEvent("rate_limit", ...)`. Assert `StreamTurnRetry` retries per `DefaultRetryConfig()`. Use the multi-turn `turns` feature.
- `window_test.go`: `ModelWindow` now reads the catalog, not HTTP. Rewrite to assert exact/prefix match against `catalog.New(Options{Refresh:false})` entries (e.g. `gpt-4o` → 128000, `claude-sonnet-4-...` → 200000, unknown → 0). Delete the httptest server.
- `client_test.go`: rewrite the streaming assertions using a scripted adapter (text deltas + done).

Delete every now-unused `httptest`, `net/http`, SSE-builder helper, and `srv` variable in these files.

- [ ] **Step 3: Migrate cross-package tests via llmtest**

For each external call site, replace the httptest+`llm.NewClient(url)` setup with `llmtest.Client(...)`:

- `pkg/agent/loop_test.go` (7 sites): each test asserts agent behavior for a turn (or multi-turn) script. Replace the SSE server with `llmtest.Client(turn1, turn2, ...)` where each `turnN` is a `[]provider.Event` built from `llmtest.Start/Text/ToolCall/Done/ErrorEvent`. Map the old canned SSE per test to the equivalent event slice (a tool-call test emits `ToolCall` deltas + a `Done` whose `AgentMessage` carries the finalized `ToolCallContent`).
- `pkg/agent/builder_test.go` (1 site): `llm.NewClient("http://localhost:8787")` is a placeholder client never streamed; replace with `llmtest.Client(nil)` (a single empty turn) or `llmtest.Client([]provider.Event{llmtest.Done(models.AssistantMessage(""), nil)})`.
- `pkg/compaction/summarizer_test.go` (3 sites): the summarizer calls one turn and reads the final text. Replace with `llmtest.Client([]provider.Event{llmtest.Done(models.AssistantMessage("summary"), nil)})`; the error-path test (`http://127.0.0.1:0`) becomes `llmtest.Client([]provider.Event{llmtest.ErrorEvent("internal", "boom")})`.
- `pkg/tui/providerpanel_test.go` (2 sites): the panel lists models / registers providers. Replace with `llmtest.Client(...)`; `ListModels` now returns the snapshot catalog, so adjust assertions to expect snapshot models (or seed `Overrides`).
- `cmd/lcoder/lookup_test.go` (1 site): this tests `lookupModelWindow` against an httptest `/v1/models`. Since `ModelWindow` is now catalog-backed and `lookupModelWindow` is being removed in Task 13, **delete this test** (its behavior is covered by `pkg/llm/catalog` window tests). Note the deletion in the commit message.

> **Per-test event mapping is mechanical but not blind:** open each test, read what the old SSE asserted, and build the matching `[]provider.Event`. Do not delete assertions — translate them. If a test asserted on `tool_call` payload fields mid-stream, assert on the finalized `ToolCallContent` in `Done`'s message instead (the loop finalizes tool calls in `done`).

- [ ] **Step 4: Run the full test suite**

Run: `go test ./pkg/... ./cmd/...`
Expected: PASS across all packages. Fix any per-test translation gaps until green.

- [ ] **Step 5: Format + vet**

Run: `gofmt -l pkg/llm cmd/lcoder pkg/agent pkg/compaction pkg/tui` then `go vet ./pkg/llm/... ./pkg/agent/... ./pkg/compaction/... ./pkg/tui/... ./cmd/...`
Expected: no output from gofmt; vet clean.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "test(llm): migrate all clients to in-process fake-engine, drop SSE scaffolding"
```

---

## Task 13: Rewire `cmd/lcoder/main.go` to construct the engine in-process

**Files:**
- Modify: `cmd/lcoder/main.go` (build engine, seed providers, drop gateway lifecycle)
- Modify: `cmd/lcoder/main.go` (remove `ensureGateway`/`reachable`/`waitForGateway`/`lookupModelWindow`; switch window source to the client/catalog)

Replace the subprocess gateway with an in-process engine seeded from `cfg.Providers` and the resolved model catalog. The context-budget call (`ResolveContextBudget`) stays; only its window source changes.

- [ ] **Step 1: Add an engine constructor helper**

Add to `main.go` (imports: `github.com/lcoder/lcoder/pkg/llm/catalog`, `github.com/lcoder/lcoder/pkg/llm/engine`, `github.com/lcoder/lcoder/pkg/llm/provider`; the existing `os`/`path/filepath` are reused):

```go
// buildEngine constructs the in-process LLM engine: catalog (with models.yaml
// overrides + background refresh unless disabled) seeded with the resolved
// provider connections from config/credentials.
func buildEngine(cfg config.Config) *engine.Engine {
	cachePath := filepath.Join(lcoderHome(), "cache", "models.json")
	cat := catalog.New(catalog.Options{
		Refresh:   !cfg.DisableModelDiscovery, // see note below
		CachePath: cachePath,
		Overrides: catalogOverridesFromConfig(cfg), // []catalog.Entry from cfg.Catalog
	})
	eng := engine.New(cat)
	for name, conn := range cfg.Providers {
		eng.RegisterProvider(name, provider.Conn{
			BaseURL: conn.BaseURL,
			APIKey:  conn.APIKey,
			Route:   conn.Route,
			Headers: conn.Headers,
		})
	}
	return eng
}
```

> **Adapt to existing config:** `lcoderHome()`, `cfg.DisableModelDiscovery`, and `catalogOverridesFromConfig` are names to reconcile with the codebase. Grep for how the home dir is currently derived (the credentials path logic) and reuse it; if there is no discovery toggle, pass `Refresh: true` unconditionally and drop the field. For `Overrides`, convert the existing `cfg.Catalog`/`ModelCatalog` entries (which carry `ContextWindow`/`Budget`) into `[]catalog.Entry`; if that mapping is non-trivial, implement a small `catalogOverridesFromConfig(cfg config.Config) []catalog.Entry` in `main.go` that reads the same fields `applyCatalogOverrides` used. Keep it minimal — only `ID`/`Provider`/`ContextWindow`/`Capabilities` are needed for window+list.

- [ ] **Step 2: Replace the gateway wiring at main.go:152-176**

Delete the `LCODER_MODELS_CONFIG`/`LCODER_PROVIDERS` env exports (152-164) and the `ensureGateway`/`NewClient(gatewayURL)` block (172-176). Replace with:

```go
	llmClient := llm.NewClient(buildEngine(cfg))
```

Remove the now-unused `cleanup` variable and every `cleanup()` call in the error paths of `start(...)` (the engine has no subprocess to tear down). If `cleanup` is referenced in many `return nil, err` sites, replace each `cleanup()` with nothing (delete the line).

- [ ] **Step 3: Switch the window lookup source**

At main.go:259-263, replace `lookupModelWindow(llmClient, cfg.Provider, cfg.Model)` with the catalog-backed client method:

```go
	window, _ := llmClient.ModelWindow(context.Background(), cfg.Provider, cfg.Model)
	budget, source := cfg.ResolveContextBudget(window)
	if source == "default" {
		fmt.Fprintf(os.Stderr, "warning: 未能自动获取模型 %q 的上下文窗口,回退默认 %d\n", cfg.Model, budget.MaxTotal)
	}
```

- [ ] **Step 4: Update the second call site (main.go:463-468)**

The provider-management path also calls `ensureGateway`+`NewClient(url)`. Replace with `client := llm.NewClient(buildEngine(cfg))` (or reuse the already-built `llmClient` if in scope). Remove its `cleanup`.

- [ ] **Step 5: Delete dead gateway plumbing**

Remove these now-unused functions from `main.go`: `ensureGateway` (879), `reachable` (918), `lookupModelWindow` (931), `waitForGateway` (941). Keep `makeContextManager` (970) unchanged. Remove any imports that become unused (e.g. `time`, `encoding/json` if only used by the deleted code — let the compiler tell you).

- [ ] **Step 6: Build and run the suite**

Run: `go build ./...` then `go test ./pkg/... ./cmd/...`
Expected: build succeeds; tests pass. Fix unused-import/variable errors the compiler flags.

- [ ] **Step 7: Format + vet + commit**

```bash
gofmt -w cmd/lcoder/main.go
go vet ./cmd/...
git add cmd/lcoder/main.go
git commit -m "feat(cli): construct in-process LLM engine, drop gateway subprocess"
```

---

## Task 14: Delete the Python gateway and dead Go transport

**Files:**
- Delete: `gateway/` (entire directory)
- Delete: `pkg/llm/gateway.go`
- Modify: any remaining references (`cfg.GatewayURL`, `--gateway-cmd`, `freePort`, Python config docs)

Only after Tasks 1–13 are green. This is the one-time cutover.

- [ ] **Step 1: Confirm nothing imports the gateway transport**

Run: `grep -rn "StartGateway\|GatewayManager\|GatewayHTTPError\|ensureGateway\|waitForGateway\|LCODER_PROVIDERS\|GatewayURL" pkg cmd`
Expected: no remaining references in non-deleted code. Resolve any stragglers (e.g. a `GatewayURL` field in `config.Config` / `configs/lcoder.yaml` — remove the field and its YAML/docs, and any `--gateway-cmd` flag in `main.go`).

- [ ] **Step 2: Delete the files**

```bash
git rm pkg/llm/gateway.go
git rm -r gateway/
```

- [ ] **Step 3: Remove gateway config surface**

If `config.Config` has a `GatewayURL` (or `GatewayCmd`) field, remove it, its `DefaultConfig`/confmap defaults, and the `configs/lcoder.yaml` keys + comments. Update `gateway/.claude/CLAUDE.md` removal (it goes with the directory). Grep `gateway` across `configs/` and `docs/` and prune stale references.

- [ ] **Step 4: Full build + test + vet + fmt**

Run:
```bash
go build ./...
go test ./pkg/... ./cmd/...
go vet ./pkg/... ./cmd/...
gofmt -l pkg cmd
```
Expected: build + tests pass, vet clean, gofmt no output. Confirm `ls gateway/` no longer exists.

- [ ] **Step 5: Manual smoke (before final commit)**

With real keys in `~/.lcoder/credentials.yaml`, run the binary against each of the 6 providers once: streaming text, a tool call, and a cost figure in the status line. Confirm no Python process spawns (`ps`/Task Manager shows only the Go binary). If any provider fails, fix the adapter before committing — this is the acceptance gate from the spec.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "chore: remove Python gateway and dead Go HTTP transport"
```

---

## Final Self-Review Checklist (run after all tasks)

- [ ] `go build ./...` clean; `go test ./pkg/... ./cmd/...` green; `go vet` clean on touched packages; `gofmt -l` empty on touched files.
- [ ] Adapter tests cover all five event kinds incl. **streamed tool-call argument fragments** (the historical bug).
- [ ] `GatewayEvent` payload shapes match the Critical Contracts table exactly (agent loop unchanged).
- [ ] No `gateway/` dir, no Python, no subprocess; binary runs standalone.
- [ ] 6 providers smoke-tested (text, tool call, cost).
