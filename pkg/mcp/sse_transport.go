package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// SSETransport speaks MCP over an HTTP+SSE channel:
//   - a GET /sse stream receives server-initiated messages (endpoint event + responses)
//   - client requests are POSTed to the endpoint URL received from the server
//
// This matches the legacy MCP remote transport used by many reference servers.
type SSETransport struct {
	baseURL string
	headers map[string]string
	client  *http.Client

	mu      sync.Mutex
	nextID  int32
	pending map[int]chan Response
	closed  bool
	stopErr error

	ctx    context.Context
	cancel context.CancelFunc

	endpoint   string
	endpointCh chan string
	errCh      chan error
}

// NewSSETransport connects to a remote MCP server at baseURL.
// If timeout is zero, a 30s default is used for both SSE connection and POSTs.
func NewSSETransport(baseURL string, headers map[string]string, timeout time.Duration) (*SSETransport, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	t := &SSETransport{
		baseURL:    strings.TrimRight(baseURL, "/"),
		headers:    copyHeaders(headers),
		client:     &http.Client{Timeout: timeout},
		pending:    make(map[int]chan Response),
		ctx:        ctx,
		cancel:     cancel,
		endpointCh: make(chan string, 1),
		errCh:      make(chan error, 1),
	}
	go t.sseReader()
	select {
	case endpoint := <-t.endpointCh:
		t.endpoint = endpoint
	case err := <-t.errCh:
		return nil, err
	case <-time.After(timeout):
		t.cancel()
		return nil, fmt.Errorf("timeout waiting for sse endpoint event")
	}
	return t, nil
}

func (t *SSETransport) sseReader() {
	req, err := http.NewRequestWithContext(t.ctx, http.MethodGet, t.baseURL+"/sse", nil)
	if err != nil {
		t.errCh <- err
		return
	}
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := t.client.Do(req)
	if err != nil {
		t.errCh <- err
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.errCh <- fmt.Errorf("sse endpoint returned %d", resp.StatusCode)
		return
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	firstEndpoint := true
	var eventName string
	var data strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			payload := data.String()
			if eventName == "endpoint" && firstEndpoint {
				t.endpointCh <- t.resolveEndpoint(payload)
				firstEndpoint = false
			} else if eventName == "message" {
				t.dispatchResponse([]byte(payload))
			}
			eventName = ""
			data.Reset()
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(line, "data:"))
		}
	}
	if err := scanner.Err(); err != nil && err != io.EOF && t.ctx.Err() == nil {
		t.mu.Lock()
		t.stopErr = err
		t.mu.Unlock()
	}
}

func (t *SSETransport) resolveEndpoint(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return t.baseURL + "/messages"
	}
	if strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
		return endpoint
	}
	if strings.HasPrefix(endpoint, "/") {
		return t.baseURL + endpoint
	}
	u, err := url.Parse(t.baseURL)
	if err != nil {
		return endpoint
	}
	u.Path = endpoint
	return u.String()
}

func (t *SSETransport) dispatchResponse(payload []byte) {
	var resp Response
	if err := json.Unmarshal(payload, &resp); err != nil {
		return
	}
	t.mu.Lock()
	ch, ok := t.pending[resp.ID]
	delete(t.pending, resp.ID)
	t.mu.Unlock()
	if ok {
		ch <- resp
	}
}

// Call implements Transport.
func (t *SSETransport) Call(ctx context.Context, method string, params any, v any) error {
	id := int(atomic.AddInt32(&t.nextID, 1))
	paramsBytes, err := json.Marshal(params)
	if err != nil {
		return err
	}
	req := Request{JSONRPC: "2.0", ID: id, Method: method, Params: paramsBytes}

	respCh := make(chan Response, 1)
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return fmt.Errorf("mcp transport closed")
	}
	t.pending[id] = respCh
	t.mu.Unlock()

	if err := t.post(ctx, req); err != nil {
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
		return err
	}

	select {
	case <-ctx.Done():
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
		return ctx.Err()
	case resp := <-respCh:
		if resp.Error != nil {
			return resp.Error
		}
		if v != nil {
			return json.Unmarshal(resp.Result, v)
		}
		return nil
	}
}

// Notify implements Transport.
func (t *SSETransport) Notify(method string, params any) error {
	paramsBytes, err := json.Marshal(params)
	if err != nil {
		return err
	}
	req := Request{JSONRPC: "2.0", Method: method, Params: paramsBytes}
	return t.post(context.Background(), req)
}

func (t *SSETransport) post(ctx context.Context, req Request) error {
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(data))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	for k, v := range t.headers {
		httpReq.Header.Set(k, v)
	}
	resp, err := t.client.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("post %s returned %d: %s", t.endpoint, resp.StatusCode, string(body))
	}
	return nil
}

// Close implements Transport.
func (t *SSETransport) Close() error {
	t.cancel()
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	for _, ch := range t.pending {
		close(ch)
	}
	t.pending = make(map[int]chan Response)
	t.mu.Unlock()
	return nil
}

// Healthy implements Transport.
func (t *SSETransport) Healthy() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return !t.closed && t.stopErr == nil
}

func copyHeaders(h map[string]string) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		out[k] = v
	}
	return out
}
