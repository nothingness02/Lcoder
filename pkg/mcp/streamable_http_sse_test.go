package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 2025-03-26 规范允许 server 用 text/event-stream 响应 POST:工具结果以若干
// SSE 事件送达,其中可能夹带通知。客户端必须按请求 id 聚合出响应。
func TestStreamableHTTPSSEToolCallResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/mcp" {
			if r.Header.Get("mcp-session-id") == "" {
				// initialize:JSON 响应 + session id
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("mcp-session-id", "sess-1")
				fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{"tools":{}},"serverInfo":{"name":"sse-server","version":"1.0"}}}`)
				return
			}
			// tools/call:SSE 响应,先夹一条通知再给响应(id 回显请求)
			body, _ := io.ReadAll(r.Body)
			var rpcReq struct {
				ID int `json:"id"`
			}
			_ = json.Unmarshal(body, &rpcReq)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher, _ := w.(http.Flusher)
			fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progress\":0.5}}\n\n")
			flusher.Flush()
			fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"sse result\"}]}}\n\n", rpcReq.ID)
			flusher.Flush()
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	transport, err := NewStreamableHTTPTransport(server.URL+"/mcp", nil, 5*time.Second)
	if err != nil {
		t.Fatalf("create transport: %v", err)
	}
	defer transport.Close()

	client, err := NewClient("sse-server", transport)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}

	// 直接调 transport.Call 以控制 id(initialize 用了 id=1,tools/list 用了 id=2
	// —— 所以这里的预期响应 id 是下一个;用 client.CallTool 走全链路更稳)。
	res, err := client.CallTool(context.Background(), "anything", map[string]any{})
	if err != nil {
		t.Fatalf("SSE tool call must succeed, got: %v", err)
	}
	if got := res.ContentText(); got != "sse result" {
		t.Fatalf("ContentText = %q, want sse result", got)
	}
}

// SSE 流中途断开且没有匹配的响应 → 明确报错而不是挂起。
func TestStreamableHTTPSSEStreamEndsWithoutResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("mcp-session-id") == "" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("mcp-session-id", "sess-1")
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{"tools":{}},"serverInfo":{"name":"x","version":"1"}}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.Contains(string(body), "tools/list") {
			fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"tools\":[]}}\n\n")
			return
		}
		// tools/call:只有通知,没有响应
		fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\n")
	}))
	defer server.Close()

	transport, _ := NewStreamableHTTPTransport(server.URL+"/mcp", nil, 5*time.Second)
	defer transport.Close()
	client, err := NewClient("x", transport)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if _, err := client.CallTool(context.Background(), "t", map[string]any{}); err == nil {
		t.Fatal("expected error when SSE stream ends without response")
	}
}
