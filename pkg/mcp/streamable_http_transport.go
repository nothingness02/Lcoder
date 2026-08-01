package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// StreamableHTTPTransport implements the MCP 2025-03-26 streamable HTTP
// transport. All JSON-RPC requests are POSTed to a single URL; the server
// returns the session ID in the initialize response headers, which must be
// included in subsequent requests.
type StreamableHTTPTransport struct {
	url     string
	headers map[string]string
	client  *http.Client

	mu        sync.RWMutex
	sessionID string
	closed    bool
	nextID    int32
}

// NewStreamableHTTPTransport creates a streamable HTTP transport for the
// given endpoint. A zero timeout falls back to 30s.
func NewStreamableHTTPTransport(baseURL string, headers map[string]string, timeout time.Duration) (*StreamableHTTPTransport, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &StreamableHTTPTransport{
		url:     strings.TrimRight(baseURL, "/"),
		headers: copyHeaders(headers),
		client:  &http.Client{Timeout: timeout},
	}, nil
}

// Call sends a JSON-RPC request and unmarshals the result into v.
func (t *StreamableHTTPTransport) Call(ctx context.Context, method string, params any, v any) error {
	id := atomic.AddInt32(&t.nextID, 1)
	reqBody, err := json.Marshal(jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}

	t.mu.RLock()
	sessionID := t.sessionID
	t.mu.RUnlock()
	if method != "initialize" && sessionID == "" {
		return fmt.Errorf("streamable http: no session id; initialize first")
	}
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("streamable http %s returned %d: %s", method, resp.StatusCode, string(body))
	}

	// Capture session ID from the initialize response.
	if method == "initialize" {
		if sid := resp.Header.Get("mcp-session-id"); sid != "" {
			t.mu.Lock()
			t.sessionID = sid
			t.mu.Unlock()
		}
	}

	// The server may answer a POST either with a single JSON body or with an
	// SSE stream carrying the JSON-RPC response (2025-03-26 §Streamable HTTP).
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return t.readSSEResponse(resp.Body, id, v)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	var rpcResp jsonRPCResponse
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return fmt.Errorf("unmarshal response: %w (body: %s)", err, string(body))
	}
	if rpcResp.Error != nil {
		return fmt.Errorf("jsonrpc error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	if v != nil && rpcResp.Result != nil {
		if err := json.Unmarshal(rpcResp.Result, v); err != nil {
			return fmt.Errorf("unmarshal result: %w", err)
		}
	}
	return nil
}

// errResponseReceived signals that the matching JSON-RPC response arrived
// inside an SSE stream (success path out of readSSEResponse).
var errResponseReceived = fmt.Errorf("mcp: response received")

// readSSEResponse consumes an SSE stream until the JSON-RPC message with the
// request's id arrives. Server notifications interleaved in the stream
// (progress, log messages) are skipped; a stream that ends without the
// response is an error, never a silent empty result.
func (t *StreamableHTTPTransport) readSSEResponse(body io.Reader, wantID int32, v any) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 1024), 1024*1024)

	var data strings.Builder
	flush := func() error {
		payload := data.String()
		data.Reset()
		if payload == "" {
			return nil
		}
		var rpcResp jsonRPCResponse
		if err := json.Unmarshal([]byte(payload), &rpcResp); err != nil {
			return nil // 非 JSON 帧(心跳/注释),忽略
		}
		if !jsonIDMatches(rpcResp.ID, wantID) {
			return nil // 通知或其他请求的消息
		}
		if rpcResp.Error != nil {
			return fmt.Errorf("jsonrpc error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
		}
		if v != nil && rpcResp.Result != nil {
			if err := json.Unmarshal(rpcResp.Result, v); err != nil {
				return fmt.Errorf("unmarshal result: %w", err)
			}
		}
		return errResponseReceived
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				if err == errResponseReceived {
					return nil
				}
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
		// event:/id:/retry: 行按 SSE 规范跳过;响应归属由 JSON-RPC id 判定
	}
	if err := flush(); err != nil && err != errResponseReceived {
		return err
	} else if err == errResponseReceived {
		return nil
	}
	return fmt.Errorf("streamable http: SSE stream ended without a response for id %d", wantID)
}

// jsonIDMatches compares a JSON-RPC id (float64 after unmarshal, but servers
// may also send ints or strings) with the client-assigned int32 id.
func jsonIDMatches(got any, want int32) bool {
	switch id := got.(type) {
	case float64:
		return int64(id) == int64(want)
	case string:
		return id == fmt.Sprint(want)
	}
	return false
}

// Notify sends a JSON-RPC notification without waiting for a response.
func (t *StreamableHTTPTransport) Notify(method string, params any) error {
	reqBody, err := json.Marshal(jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	})
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), t.client.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}

	t.mu.RLock()
	sessionID := t.sessionID
	t.mu.RUnlock()
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// Close marks the transport as closed.
func (t *StreamableHTTPTransport) Close() error {
	t.mu.Lock()
	t.closed = true
	t.mu.Unlock()
	return nil
}

// Healthy reports whether the transport is still usable.
func (t *StreamableHTTPTransport) Healthy() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return !t.closed
}

type jsonRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *jsonRPCError   `json:"error"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
