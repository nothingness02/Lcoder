package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// StdioTransport runs an MCP server as a local subprocess and speaks
// newline-delimited JSON-RPC over its stdin/stdout pipes.
type StdioTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser

	mu      sync.Mutex
	nextID  int32
	pending map[int]chan Response
	closed  bool
	stopErr error
}

// NewStdioTransport starts command as an MCP stdio server.
func NewStdioTransport(command []string, env map[string]string) (*StdioTransport, error) {
	if len(command) == 0 {
		return nil, fmt.Errorf("mcp server command is empty")
	}

	cmd := exec.Command(command[0], command[1:]...)
	for k, v := range env {
		cmd.Env = append(os.Environ(), k+"="+v)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp start: %w", err)
	}

	t := &StdioTransport{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  stdout,
		stderr:  stderr,
		pending: make(map[int]chan Response),
	}
	go t.readLoop()
	go t.stderrLoop()
	return t, nil
}

// Call implements Transport.
func (t *StdioTransport) Call(ctx context.Context, method string, params any, v any) error {
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

	if err := t.send(req); err != nil {
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
func (t *StdioTransport) Notify(method string, params any) error {
	paramsBytes, err := json.Marshal(params)
	if err != nil {
		return err
	}
	req := Request{JSONRPC: "2.0", Method: method, Params: paramsBytes}
	return t.send(req)
}

func (t *StdioTransport) send(req Request) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return fmt.Errorf("mcp transport closed")
	}
	t.mu.Unlock()

	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = t.stdin.Write(data)
	return err
}

func (t *StdioTransport) readLoop() {
	scanner := bufio.NewScanner(t.stdout)
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var resp Response
		if err := json.Unmarshal(line, &resp); err != nil {
			continue
		}
		t.mu.Lock()
		ch, ok := t.pending[resp.ID]
		delete(t.pending, resp.ID)
		t.mu.Unlock()
		if ok {
			ch <- resp
		}
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		t.mu.Lock()
		t.stopErr = err
		t.mu.Unlock()
	}
}

func (t *StdioTransport) stderrLoop() {
	// Drain stderr to avoid blocking the child process.
	_, _ = io.Copy(io.Discard, t.stderr)
}

// Close implements Transport.
func (t *StdioTransport) Close() error {
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

	_ = t.stdin.Close()

	done := make(chan struct{})
	go func() {
		_ = t.cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = t.cmd.Process.Kill()
		<-done
	}
	return nil
}

// Healthy implements Transport.
func (t *StdioTransport) Healthy() bool {
	if t.cmd == nil || t.cmd.Process == nil {
		return false
	}
	return t.cmd.Process.Signal(os.Signal(nil)) == nil
}
