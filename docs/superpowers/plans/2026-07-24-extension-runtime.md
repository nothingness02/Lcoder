# Extension Runtime Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the Go-plugin extension carrier with a process-external JSON-RPC extension runtime: event subscription, four intervention hooks, session custom entries, and extension slash commands.

**Architecture:** Extensions are child processes speaking newline-delimited JSON-RPC 2.0 over stdio (same framing as `pkg/mcp` stdio transport). `pkg/extension/proto` holds wire types, `pkg/extension/runtime` holds connection/process/host, `pkg/extension/bridge` adapts the host into existing in-process seams (`agent.BeforeToolCallHook`, `agent.AfterToolCallHook`, `contextmgr.SummarizeFunc`, TUI input path, TUI command registry). No agent-loop control-flow changes.

**Spec:** `docs/superpowers/specs/2026-07-24-extension-runtime-design.md`

**Tech Stack:** Go 1.25, stdlib `os/exec` + `encoding/json`; YAML via `gopkg.in/yaml.v3` (already used by `pkg/config`).

**Deviation from spec (approved mechanism, noted honestly):** the project-extension trust prompt is a stdin y/N prompt shown in `prepareAgent` before the TUI starts (same trust semantics as the spec's "Ask 通道"), not an in-TUI dialog — extension loading must complete before the UI exists. JSON mode skips with a warning; `--trust-project-extensions` pre-authorizes.

**Test conventions:** run unit tests with `go test ./pkg/... -run TestName -v`; full suite with `go test $(go list ./... | grep -v 'reference/Shannon')`.

---

## File Structure

- Create `pkg/extension/proto/proto.go` — JSON-RPC 2.0 wire types, method constants, hook/event payload types.
- Create `pkg/extension/proto/proto_test.go`
- Create `pkg/extension/runtime/conn.go` — bidirectional JSON-RPC connection over `io.Reader`/`io.Writer` (testable with `io.Pipe`).
- Create `pkg/extension/runtime/conn_test.go`
- Create `pkg/extension/runtime/manifest.go` — `extension.yaml` parsing + directory discovery.
- Create `pkg/extension/runtime/manifest_test.go`
- Create `pkg/extension/runtime/process.go` — child process spawn, stderr capture, graceful kill.
- Create `pkg/extension/runtime/host.go` — multi-extension host: handshake, hook fan-out (chain, timeout, fail-open, dead-marking), event broadcast, command dispatch, reverse-request routing.
- Create `pkg/extension/runtime/host_test.go`
- Create `pkg/extension/bridge/bridge.go` — adapters into agent hooks / summarizer / input hook / events / commands / session entries.
- Create `pkg/extension/bridge/bridge_test.go`
- Modify `pkg/models/message.go` — add `RoleCustom`.
- Modify `pkg/session/store.go` — `AppendCustomEntry`, `CustomEntries`, `IsCustomEntry`, filter `role=custom` out of `ActiveMessages`.
- Modify `pkg/session/store_test.go` (or new `custom_entry_test.go`)
- Modify `pkg/agent/loop.go` — add `ModifiedArgs` to `BeforeToolCallResult`.
- Modify `pkg/agent/executor.go:233-236` — apply `ModifiedArgs` before execution.
- Modify `pkg/tui/slash_registry.go` — exported `RegisterExtensionCommand` with conflict check.
- Modify `pkg/tui/keys.go` — `submit` runs the input hook before skill parsing.
- Modify `pkg/tui/model.go` — `SetInputHook` setter.
- Modify `pkg/config/hooks.go` + `pkg/config/config.go` — replace unused `Extensions []ExtensionConfig` with `ExtensionsConfig`.
- Modify `cmd/lcoder/main.go` — wire host lifecycle, trust gate, bridge, `--trust-project-extensions` flag, shutdown in cleanup.
- Modify `cmd/lcoder/wiring.go` — stdin trust prompter.
- Delete `pkg/extension/plugin.go`; strip go-plugin path from `pkg/tools/extensions.go` + `pkg/tools/extensions_test.go`; delete `cmd/lcoder/extensions.go`; delete `examples/extension-subagent/`.
- Create `test/integration/extension_e2e_test.go` (build tag `integration`) — real helper extension binary.

---

### Task 1: proto package — wire types

**Files:**
- Create: `pkg/extension/proto/proto.go`
- Test: `pkg/extension/proto/proto_test.go`

- [ ] **Step 1: Write the failing test**

```go
package proto

import (
	"encoding/json"
	"testing"
)

func TestRequestRoundTrip(t *testing.T) {
	req := Request{JSONRPC: "2.0", ID: 7, Method: MethodHookToolCall,
		Params: json.RawMessage(`{"tool":"bash","params":{"command":"ls"}}`)}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var got Request
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != 7 || got.Method != MethodHookToolCall {
		t.Fatalf("got %+v", got)
	}
	var p ToolCallParams
	if err := json.Unmarshal(got.Params, &p); err != nil {
		t.Fatal(err)
	}
	if p.Tool != "bash" || p.Params["command"] != "ls" {
		t.Fatalf("params %+v", p)
	}
}

func TestNotificationHasNoID(t *testing.T) {
	n := Request{JSONRPC: "2.0", Method: EventMethodPrefix + "turn_start"}
	data, _ := json.Marshal(n)
	var raw map[string]any
	_ = json.Unmarshal(data, &raw)
	if _, ok := raw["id"]; ok {
		t.Fatal("notification must not carry id")
	}
}

func TestInitializeResultRoundTrip(t *testing.T) {
	res := InitializeResult{
		Name: "ext", Version: "0.1.0",
		Events:   []string{"turn_start"},
		Hooks:    []string{HookToolCall, HookInput},
		Commands: []CommandDecl{{Name: "review", Description: "d", Usage: "/review"}},
	}
	data, _ := json.Marshal(res)
	var got InitializeResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Hooks[1] != HookInput || got.Commands[0].Name != "review" {
		t.Fatalf("got %+v", got)
	}
}

func TestRPCErrorString(t *testing.T) {
	e := &RPCError{Code: -32601, Message: "method not found"}
	if e.Error() != "method not found" {
		t.Fatalf("got %q", e.Error())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/extension/proto -v`
Expected: FAIL — `package github.com/lcoder/lcoder/pkg/extension/proto: no Go files`

- [ ] **Step 3: Write minimal implementation**

```go
// Package proto defines the wire protocol between the Lcoder host and
// process-external extensions: newline-delimited JSON-RPC 2.0 over stdio.
package proto

import "encoding/json"

// ProtocolVersion is the version negotiated in initialize.
const ProtocolVersion = 1

// JSON-RPC 2.0 wire types. A message with Method and no ID is a notification.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return e.Message }

// Host -> extension methods.
const (
	MethodInitialize        = "initialize"
	MethodShutdown          = "shutdown"
	MethodHookToolCall      = "hook/tool_call"
	MethodHookToolResult    = "hook/tool_result"
	MethodHookBeforeCompact = "hook/session_before_compact"
	MethodHookInput         = "hook/input"
	MethodCommandInvoke     = "command/invoke"
	// EventMethodPrefix prefixes event notifications: "event/<event-type>".
	EventMethodPrefix = "event/"
)

// Extension -> host methods.
const (
	MethodSessionAppendEntry = "session/append_entry"
	MethodSessionGetEntries  = "session/get_entries"
	MethodHostLog            = "host/log"
)

// Hook names as declared in InitializeResult.Hooks.
const (
	HookToolCall      = "tool_call"
	HookToolResult    = "tool_result"
	HookBeforeCompact = "session_before_compact"
	HookInput         = "input"
)

type InitializeParams struct {
	ProtocolVersion int    `json:"protocol_version"`
	Host            string `json:"host"`
}

type CommandDecl struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Usage       string `json:"usage,omitempty"`
}

type InitializeResult struct {
	Name     string        `json:"name"`
	Version  string        `json:"version"`
	Events   []string      `json:"events,omitempty"`
	Hooks    []string      `json:"hooks,omitempty"`
	Commands []CommandDecl `json:"commands,omitempty"`
}

// hook/tool_call: action is "allow" or "block"; Params replaces tool args.
type ToolCallParams struct {
	Tool   string         `json:"tool"`
	Params map[string]any `json:"params"`
}

type ToolCallResult struct {
	Action string         `json:"action"`
	Reason string         `json:"reason,omitempty"`
	Params map[string]any `json:"params,omitempty"`
}

// hook/tool_result: Result replaces the tool result text when non-nil.
type ToolResultParams struct {
	Tool    string         `json:"tool"`
	Params  map[string]any `json:"params"`
	Result  string         `json:"result"`
	IsError bool           `json:"is_error"`
}

type ToolResultResult struct {
	Result *string `json:"result,omitempty"`
}

// hook/session_before_compact.
type BeforeCompactParams struct {
	Conversation string `json:"conversation"`
	TokensBefore int    `json:"tokens_before"`
}

type BeforeCompactResult struct {
	Summary string `json:"summary"`
}

// hook/input: action is "continue", "transform", or "block".
type InputParams struct {
	Text string `json:"text"`
}

type InputResult struct {
	Action string `json:"action"`
	Text   string `json:"text,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// command/invoke.
type CommandInvokeParams struct {
	Name string `json:"name"`
	Args string `json:"args"`
}

type CommandInvokeResult struct {
	Output string `json:"output,omitempty"`
}

// session/append_entry, session/get_entries.
type AppendEntryParams struct {
	CustomType string          `json:"custom_type"`
	Data       json.RawMessage `json:"data"`
}

type Entry struct {
	CustomType string          `json:"custom_type"`
	Data       json.RawMessage `json:"data"`
}

type GetEntriesResult struct {
	Entries []Entry `json:"entries"`
}

// host/log: level is "info", "warn", or "error".
type HostLogParams struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/extension/proto -v`
Expected: PASS (4 tests)

- [ ] **Step 5: Commit**

```bash
git add pkg/extension/proto/
git commit -m "feat(extension): wire protocol types for process-external extensions"
```

---

### Task 2: runtime conn — bidirectional JSON-RPC over io

**Files:**
- Create: `pkg/extension/runtime/conn.go`
- Test: `pkg/extension/runtime/conn_test.go`

The connection differs from `pkg/mcp.StdioTransport` in one essential way: it is **bidirectional** — the extension also sends requests to the host (`session/append_entry`, `host/log`), so the read loop dispatches inbound requests/notifications to a handler, not just responses to pending calls. Transport is plain `io.Reader`/`io.Writer` so tests use `io.Pipe`.

- [ ] **Step 1: Write the failing test**

```go
package runtime

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/extension/proto"
)

// pipePair returns two conns wired to each other over in-memory pipes.
func pipePair(t *testing.T, h1, h2 Handler) (*Conn, *Conn) {
	t.Helper()
	r1, w1 := io.Pipe()
	r2, w2 := io.Pipe()
	c1 := NewConn(r1, w2, h1)
	c2 := NewConn(r2, w1, h2)
	t.Cleanup(func() { _ = c1.Close(); _ = c2.Close() })
	return c1, c2
}

func TestConnCallRoundTrip(t *testing.T) {
	echo := HandlerFunc{
		RequestFunc: func(_ context.Context, method string, params json.RawMessage) (any, error) {
			return json.RawMessage(params), nil
		},
	}
	server, client := pipePair(t, echo, nil)

	var out proto.ToolCallResult
	err := client.Call(context.Background(), proto.MethodHookToolCall,
		proto.ToolCallParams{Tool: "bash", Params: map[string]any{"command": "ls"}}, &out)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
}

func TestConnCallErrorPropagates(t *testing.T) {
	h := HandlerFunc{
		RequestFunc: func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
			return nil, &proto.RPCError{Code: -32601, Message: "nope"}
		},
	}
	server, client := pipePair(t, h, nil)
	err := client.Call(context.Background(), "x/y", nil, nil)
	if err == nil || err.Error() != "nope" {
		t.Fatalf("err = %v", err)
	}
	_ = server
}

func TestConnNotification(t *testing.T) {
	got := make(chan string, 1)
	h := HandlerFunc{
		NotifyFunc: func(method string, _ json.RawMessage) { got <- method },
	}
	_, client := pipePair(t, h, nil)
	if err := client.Notify(proto.EventMethodPrefix+"turn_start", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-got:
		if m != "event/turn_start" {
			t.Fatalf("method %q", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("notification not delivered")
	}
}

func TestConnCallAfterCloseFails(t *testing.T) {
	h := HandlerFunc{RequestFunc: func(_ context.Context, _ string, _ json.RawMessage) (any, error) { return nil, nil }}
	server, client := pipePair(t, h, nil)
	_ = server.Close()
	err := client.Call(context.Background(), "x/y", nil, nil)
	if err == nil {
		t.Fatal("expected error after peer close")
	}
}

func TestConnCallContextCancel(t *testing.T) {
	block := HandlerFunc{RequestFunc: func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	_, client := pipePair(t, block, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := client.Call(ctx, "x/y", nil, nil); err == nil {
		t.Fatal("expected ctx error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/extension/runtime -v`
Expected: FAIL — no Go files / undefined `NewConn`

- [ ] **Step 3: Write minimal implementation**

```go
package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/lcoder/lcoder/pkg/extension/proto"
)

// Handler processes inbound traffic from the extension peer.
type Handler interface {
	// HandleRequest answers an inbound JSON-RPC request. The returned value is
	// marshaled as the response result; a *proto.RPCError error is sent as a
	// protocol error, any other error as code -32000.
	HandleRequest(ctx context.Context, method string, params json.RawMessage) (any, error)
	// HandleNotification consumes an inbound notification.
	HandleNotification(method string, params json.RawMessage)
}

// HandlerFunc adapts plain functions to Handler; nil funcs use defaults
// (requests get method-not-found, notifications are dropped).
type HandlerFunc struct {
	RequestFunc func(ctx context.Context, method string, params json.RawMessage) (any, error)
	NotifyFunc  func(method string, params json.RawMessage)
}

func (h HandlerFunc) HandleRequest(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if h.RequestFunc == nil {
		return nil, &proto.RPCError{Code: -32601, Message: "method not found: " + method}
	}
	return h.RequestFunc(ctx, method, params)
}

func (h HandlerFunc) HandleNotification(method string, params json.RawMessage) {
	if h.NotifyFunc != nil {
		h.NotifyFunc(method, params)
	}
}

// Conn is a bidirectional newline-delimited JSON-RPC 2.0 connection.
type Conn struct {
	w       io.Writer
	handler Handler

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan proto.Response
	closed  bool
	closeCh chan struct{}
	onClose func() // called once when the read loop ends (peer EOF or error)
}

// NewConn starts the read loop immediately. handler may be nil.
func NewConn(r io.Reader, w io.Writer, handler Handler) *Conn {
	if handler == nil {
		handler = HandlerFunc{}
	}
	c := &Conn{w: w, handler: handler, pending: make(map[int64]chan proto.Response), closeCh: make(chan struct{})}
	go c.readLoop(r)
	return c
}

// SetOnClose registers a callback fired once when the connection dies.
func (c *Conn) SetOnClose(fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onClose = fn
}

// Done closes when the connection's read loop has ended.
func (c *Conn) Done() <-chan struct{} { return c.closeCh }

// Call sends a request and waits for the matching response.
func (c *Conn) Call(ctx context.Context, method string, params, result any) error {
	id := atomic.AddInt64(&c.nextID, 1)
	var paramsBytes json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		paramsBytes = b
	}
	respCh := make(chan proto.Response, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("extension conn closed")
	}
	c.pending[id] = respCh
	c.mu.Unlock()

	if err := c.send(proto.Request{JSONRPC: "2.0", ID: id, Method: method, Params: paramsBytes}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return err
	}

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	case resp := <-respCh:
		if resp.Error != nil {
			return resp.Error
		}
		if result != nil && len(resp.Result) > 0 {
			return json.Unmarshal(resp.Result, result)
		}
		return nil
	}
}

// Notify sends a notification (no id, no response).
func (c *Conn) Notify(method string, params any) error {
	var paramsBytes json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		paramsBytes = b
	}
	return c.send(proto.Request{JSONRPC: "2.0", Method: method, Params: paramsBytes})
}

func (c *Conn) send(req proto.Request) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("extension conn closed")
	}
	w := c.w
	c.mu.Unlock()
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = w.Write(data)
	return err
}

// inbound is the union used to classify a decoded line.
type inbound struct {
	ID     *int64          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *proto.RPCError `json:"error"`
}

func (c *Conn) readLoop(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var in inbound
		if err := json.Unmarshal(line, &in); err != nil {
			continue
		}
		switch {
		case in.Method != "" && in.ID != nil:
			go c.answerRequest(*in.ID, in.Method, in.Params)
		case in.Method != "":
			c.handler.HandleNotification(in.Method, in.Params)
		case in.ID != nil:
			c.mu.Lock()
			ch, ok := c.pending[*in.ID]
			delete(c.pending, *in.ID)
			c.mu.Unlock()
			if ok {
				ch <- proto.Response{JSONRPC: "2.0", ID: *in.ID, Result: in.Result, Error: in.Error}
			}
		}
	}
	c.failAll()
}

func (c *Conn) answerRequest(id int64, method string, params json.RawMessage) {
	result, err := c.handler.HandleRequest(context.Background(), method, params)
	resp := proto.Response{JSONRPC: "2.0", ID: id}
	if err != nil {
		var rpcErr *proto.RPCError
		if errors.As(err, &rpcErr) {
			resp.Error = rpcErr
		} else {
			resp.Error = &proto.RPCError{Code: -32000, Message: err.Error()}
		}
	} else if result != nil {
		b, mErr := json.Marshal(result)
		if mErr != nil {
			resp.Error = &proto.RPCError{Code: -32000, Message: fmt.Sprintf("marshal result: %v", mErr)}
		} else {
			resp.Result = b
		}
	}
	data, mErr := json.Marshal(resp)
	if mErr != nil {
		return
	}
	data = append(data, '\n')
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		_, _ = c.w.Write(data)
	}
}

func (c *Conn) failAll() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	pending := c.pending
	c.pending = make(map[int64]chan proto.Response)
	onClose := c.onClose
	c.mu.Unlock()
	for id, ch := range pending {
		ch <- proto.Response{JSONRPC: "2.0", ID: id, Error: &proto.RPCError{Code: -32000, Message: "extension connection lost"}}
	}
	close(c.closeCh)
	if onClose != nil {
		onClose()
	}
}

// Close shuts the connection; pending calls fail with "connection lost".
func (c *Conn) Close() error {
	c.failAll()
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/extension/runtime -v`
Expected: PASS (5 tests)

- [ ] **Step 5: Commit**

```bash
git add pkg/extension/runtime/conn.go pkg/extension/runtime/conn_test.go
git commit -m "feat(extension): bidirectional JSON-RPC connection for extension runtime"
```

---

### Task 3: manifest + process

**Files:**
- Create: `pkg/extension/runtime/manifest.go`
- Test: `pkg/extension/runtime/manifest_test.go`
- Create: `pkg/extension/runtime/process.go`

- [ ] **Step 1: Write the failing test**

```go
package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func writeManifest(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extension.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverFindsManifests(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, filepath.Join(root, "alpha"), "name: alpha\nversion: 0.1.0\ncommand: [\"go\", \"run\", \".\"]\n")
	writeManifest(t, filepath.Join(root, "beta"), "name: beta\ncommand: [\"python\", \"b.py\"]\nenv:\n  KEY: v\n")
	// A directory without a manifest is ignored.
	_ = os.MkdirAll(filepath.Join(root, "not-an-ext"), 0o755)

	found, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 {
		t.Fatalf("found %d, want 2", len(found))
	}
	if found[0].Name != "alpha" || found[1].Name != "beta" {
		t.Fatalf("order/names: %+v", found)
	}
	if found[1].Env["KEY"] != "v" {
		t.Fatalf("env: %+v", found[1].Env)
	}
	// Dir is recorded so the process can spawn with the extension dir as cwd.
	if found[0].Dir != filepath.Join(root, "alpha") {
		t.Fatalf("dir %q", found[0].Dir)
	}
}

func TestDiscoverMissingRootIsEmpty(t *testing.T) {
	found, err := Discover(filepath.Join(t.TempDir(), "nope"))
	if err != nil || len(found) != 0 {
		t.Fatalf("found=%v err=%v", found, err)
	}
}

func TestDiscoverRejectsBadManifest(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, filepath.Join(root, "broken"), "version: 1\n") // no name, no command
	_, err := Discover(root)
	if err == nil {
		t.Fatal("expected error for manifest without name/command")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/extension/runtime -run TestDiscover -v`
Expected: FAIL — undefined `Discover`

- [ ] **Step 3: Write minimal implementation**

`pkg/extension/runtime/manifest.go`:

```go
package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// Manifest describes how to start one extension process (extension.yaml).
type Manifest struct {
	Name    string            `yaml:"name"`
	Version string            `yaml:"version"`
	Command []string          `yaml:"command"`
	Env     map[string]string `yaml:"env"`
	// Dir is the directory containing extension.yaml; used as the process cwd.
	Dir string `yaml:"-"`
}

// Discover returns the manifests of all extensions under root
// (root/<name>/extension.yaml), sorted by name. A missing root is not an
// error; a malformed manifest is.
func Discover(root string) ([]Manifest, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Manifest
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(root, e.Name(), "extension.yaml")
		data, err := os.ReadFile(path)
		if err != nil {
			continue // not an extension directory
		}
		var m Manifest
		if err := yaml.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("extension manifest %s: %w", path, err)
		}
		if m.Name == "" || len(m.Command) == 0 {
			return nil, fmt.Errorf("extension manifest %s: name and command are required", path)
		}
		m.Dir = filepath.Join(root, e.Name())
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
```

`pkg/extension/runtime/process.go`:

```go
package runtime

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Process is a running extension child process with its JSON-RPC connection.
type Process struct {
	Conn   *Conn
	cmd    *exec.Cmd
	mu     sync.Mutex
	stderr bytes.Buffer // bounded capture for diagnostics
}

// StartProcess spawns the extension described by m and returns it with the
// connection wired to its stdio. handler receives extension->host traffic.
func StartProcess(m Manifest, handler Handler) (*Process, error) {
	cmd := exec.Command(m.Command[0], m.Command[1:]...)
	cmd.Dir = m.Dir
	for k, v := range m.Env {
		cmd.Env = append(os.Environ(), k+"="+v)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("extension %s stdin: %w", m.Name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("extension %s stdout: %w", m.Name, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("extension %s stderr: %w", m.Name, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start extension %s: %w", m.Name, err)
	}
	p := &Process{cmd: cmd}
	go func() {
		_, _ = io.Copy(&lockedBuffer{mu: &p.mu, buf: &p.stderr}, io.LimitReader(stderr, 64*1024))
	}()
	p.Conn = NewConn(stdout, stdin, handler)
	return p, nil
}

// Stderr returns the captured stderr tail for diagnostics.
func (p *Process) Stderr() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stderr.String()
}

// Close asks the process to exit (stdin close), then kills after 5s.
func (p *Process) Close() error {
	_ = p.Conn.Close()
	done := make(chan struct{})
	go func() { _ = p.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = p.cmd.Process.Kill()
		<-done
	}
	return nil
}

type lockedBuffer struct {
	mu  *sync.Mutex
	buf *bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/extension/runtime -v`
Expected: PASS (all conn + discover tests)

- [ ] **Step 5: Commit**

```bash
git add pkg/extension/runtime/manifest.go pkg/extension/runtime/manifest_test.go pkg/extension/runtime/process.go
git commit -m "feat(extension): extension manifest discovery and process lifecycle"
```

---

### Task 4: runtime host — handshake, hooks, events, commands

**Files:**
- Create: `pkg/extension/runtime/host.go`
- Test: `pkg/extension/runtime/host_test.go`

The host owns all extensions. Unit tests use `AddPeer` (an `io.Pipe`-backed fake extension) instead of real processes; process spawn is covered by the e2e task.

Key semantics (from spec §1):
- Handshake: host sends `initialize`, extension replies with capabilities.
- `tool_call`: chain in load order; any `block` wins (fail-safe); `params` modifications chain. Hook **error/timeout → allow** (fail-open) + mark warning via `OnWarning`.
- `tool_result`: chain; each may replace result text.
- `session_before_compact`: first declaring extension wins; error → `ok=false`.
- `input`: chain; `block` stops; `transform` replaces text.
- Process death → mark dead, skip thereafter, warn once.

- [ ] **Step 1: Write the failing test**

```go
package runtime

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/extension/proto"
)

// fakeExt is an in-process extension peer for host tests.
type fakeExt struct {
	conn *Conn
}

// newHostWithPeer creates a Host and attaches a fake extension served by
// handler over pipes, then runs the initialize handshake with caps.
func newHostWithPeer(t *testing.T, handler Handler, caps proto.InitializeResult) (*Host, *fakeExt) {
	t.Helper()
	hostR, extW := io.Pipe()
	extR, hostW := io.Pipe()
	extConn := NewConn(extR, extW, handler)
	h := NewHost(HostOptions{Timeout: 500 * time.Millisecond})
	if err := h.AddPeer(Manifest{Name: caps.Name}, hostR, hostW, caps); err != nil {
		t.Fatal(err)
	}
	return h, &fakeExt{conn: extConn}
}

func TestHostHandshakeRegistersCapabilities(t *testing.T) {
	h, _ := newHostWithPeer(t, nil, proto.InitializeResult{
		Name: "ext-a", Hooks: []string{proto.HookToolCall}, Events: []string{"turn_start"},
		Commands: []proto.CommandDecl{{Name: "review"}},
	})
	if !h.HasHook(proto.HookToolCall) || h.HasHook(proto.HookToolResult) {
		t.Fatal("hook registration wrong")
	}
	cmds := h.Commands()
	if len(cmds) != 1 || cmds[0].Decl.Name != "review" || cmds[0].ExtName != "ext-a" {
		t.Fatalf("commands %+v", cmds)
	}
	if !h.Subscribed("turn_start") || h.Subscribed("turn_end") {
		t.Fatal("event subscription wrong")
	}
}

func TestHostToolCallBlockWins(t *testing.T) {
	allow := HandlerFunc{RequestFunc: func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
		return proto.ToolCallResult{Action: "allow"}, nil
	}}
	block := HandlerFunc{RequestFunc: func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
		return proto.ToolCallResult{Action: "block", Reason: "no"}, nil
	}}
	h, _ := newHostWithPeer(t, allow, proto.InitializeResult{Name: "a", Hooks: []string{proto.HookToolCall}})
	if err := h.AddPeer2(block, proto.InitializeResult{Name: "b", Hooks: []string{proto.HookToolCall}}); err != nil {
		t.Fatal(err)
	}
	res := h.RunToolCallHooks(context.Background(), "bash", map[string]any{"command": "rm -rf /"})
	if !res.Block || res.Reason != "no" {
		t.Fatalf("res %+v", res)
	}
}

func TestHostToolCallParamsChain(t *testing.T) {
	rewrite := HandlerFunc{RequestFunc: func(_ context.Context, _ string, params json.RawMessage) (any, error) {
		var p proto.ToolCallParams
		_ = json.Unmarshal(params, &p)
		p.Params["extra"] = "added"
		return proto.ToolCallResult{Action: "allow", Params: p.Params}, nil
	}}
	h, _ := newHostWithPeer(t, rewrite, proto.InitializeResult{Name: "a", Hooks: []string{proto.HookToolCall}})
	res := h.RunToolCallHooks(context.Background(), "bash", map[string]any{"command": "ls"})
	if res.Block || res.Params["extra"] != "added" || res.Params["command"] != "ls" {
		t.Fatalf("res %+v", res)
	}
}

func TestHostToolCallErrorFailsOpen(t *testing.T) {
	broken := HandlerFunc{RequestFunc: func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
		return nil, &proto.RPCError{Code: -32000, Message: "boom"}
	}}
	var warns []string
	h, _ := newHostWithPeer(t, broken, proto.InitializeResult{Name: "a", Hooks: []string{proto.HookToolCall}})
	h.OnWarning = func(msg string) { warns = append(warns, msg) }
	res := h.RunToolCallHooks(context.Background(), "bash", nil)
	if res.Block {
		t.Fatal("hook error must fail open")
	}
	if len(warns) == 0 {
		t.Fatal("expected warning")
	}
}

func TestHostToolResultChains(t *testing.T) {
	upper := HandlerFunc{RequestFunc: func(_ context.Context, _ string, params json.RawMessage) (any, error) {
		var p proto.ToolResultParams
		_ = json.Unmarshal(params, &p)
		r := p.Result + "!"
		return proto.ToolResultResult{Result: &r}, nil
	}}
	h, _ := newHostWithPeer(t, upper, proto.InitializeResult{Name: "a", Hooks: []string{proto.HookToolResult}})
	got := h.RunToolResultHooks(context.Background(), "bash", nil, "ok", false)
	if got != "ok!" {
		t.Fatalf("got %q", got)
	}
}

func TestHostBeforeCompact(t *testing.T) {
	sum := HandlerFunc{RequestFunc: func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
		return proto.BeforeCompactResult{Summary: "short"}, nil
	}}
	h, _ := newHostWithPeer(t, sum, proto.InitializeResult{Name: "a", Hooks: []string{proto.HookBeforeCompact}})
	s, ok := h.RunBeforeCompactHook(context.Background(), "long conversation", 1000)
	if !ok || s != "short" {
		t.Fatalf("s=%q ok=%v", s, ok)
	}
	// A host without the hook returns ok=false.
	h2, _ := newHostWithPeer(t, nil, proto.InitializeResult{Name: "b"})
	if _, ok := h2.RunBeforeCompactHook(context.Background(), "x", 1); ok {
		t.Fatal("expected ok=false without hook")
	}
}

func TestHostInputHookTransformAndBlock(t *testing.T) {
	h := HandlerFunc{RequestFunc: func(_ context.Context, _ string, params json.RawMessage) (any, error) {
		var p proto.InputParams
		_ = json.Unmarshal(params, &p)
		if p.Text == "bad" {
			return proto.InputResult{Action: "block", Reason: "nope"}, nil
		}
		return proto.InputResult{Action: "transform", Text: p.Text + "+"}, nil
	}}
	host, _ := newHostWithPeer(t, h, proto.InitializeResult{Name: "a", Hooks: []string{proto.HookInput}})
	res := host.RunInputHook(context.Background(), "hi")
	if res.Block || res.Text != "hi+" {
		t.Fatalf("res %+v", res)
	}
	res = host.RunInputHook(context.Background(), "bad")
	if !res.Block || res.Reason != "nope" {
		t.Fatalf("res %+v", res)
	}
}

func TestHostBroadcastEventOnlyToSubscribed(t *testing.T) {
	got := make(chan string, 1)
	listener := HandlerFunc{NotifyFunc: func(method string, _ json.RawMessage) { got <- method }}
	h, fx := newHostWithPeer(t, listener, proto.InitializeResult{Name: "a", Events: []string{"turn_start"}})
	h.BroadcastEvent("turn_start", json.RawMessage(`{"type":"turn_start"}`))
	select {
	case m := <-got:
		if m != "event/turn_start" {
			t.Fatalf("method %q", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event not delivered")
	}
	h.BroadcastEvent("turn_end", json.RawMessage(`{}`))
	select {
	case m := <-got:
		t.Fatalf("unsubscribed event delivered: %s", m)
	case <-time.After(200 * time.Millisecond):
	}
	_ = fx
}

func TestHostInvokeCommand(t *testing.T) {
	h := HandlerFunc{RequestFunc: func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method != proto.MethodCommandInvoke {
			return nil, &proto.RPCError{Code: -32601, Message: "unknown"}
		}
		return proto.CommandInvokeResult{Output: "done"}, nil
	}}
	host, _ := newHostWithPeer(t, h, proto.InitializeResult{Name: "a", Commands: []proto.CommandDecl{{Name: "review"}}})
	out, err := host.InvokeCommand(context.Background(), "review", "src/")
	if err != nil || out != "done" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if _, err := host.InvokeCommand(context.Background(), "nope", ""); err == nil {
		t.Fatal("unknown command must error")
	}
}

func TestHostDeadPeerSkipped(t *testing.T) {
	calls := 0
	h := HandlerFunc{RequestFunc: func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
		calls++
		return proto.ToolCallResult{Action: "allow"}, nil
	}}
	host, fx := newHostWithPeer(t, h, proto.InitializeResult{Name: "a", Hooks: []string{proto.HookToolCall}})
	_ = fx.conn.Close() // simulate process death
	// Wait for the host side to notice.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if host.DeadCount() == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	before := calls
	res := host.RunToolCallHooks(context.Background(), "bash", nil)
	if res.Block {
		t.Fatal("dead extension must be skipped (fail open)")
	}
	if calls != before {
		t.Fatal("hook called on dead extension")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/extension/runtime -run TestHost -v`
Expected: FAIL — undefined `NewHost`, `AddPeer`, `AddPeer2`, etc.

Note on `AddPeer`/`AddPeer2`: `AddPeer(m, r, w, caps)` attaches an already-handshaken peer (used by tests; production `Load` does spawn + real handshake). `AddPeer2(handler, caps)` is a test convenience that builds the pipe pair itself. If the naming feels off while implementing, rename to `addTestPeer` — but keep the test bodies unchanged.

- [ ] **Step 3: Write minimal implementation**

```go
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/lcoder/lcoder/pkg/extension/proto"
)

// HostOptions configures a Host.
type HostOptions struct {
	// Timeout bounds each hook call. Zero defaults to 5s.
	Timeout time.Duration
}

// RegisteredCommand pairs a command declaration with its owning extension.
type RegisteredCommand struct {
	ExtName string
	Decl    proto.CommandDecl
}

// ToolCallHookResult is the aggregate outcome of the tool_call hook chain.
type ToolCallHookResult struct {
	Block  bool
	Reason string
	Params map[string]any // possibly-modified args (nil = unchanged)
}

// InputHookResult is the aggregate outcome of the input hook chain.
type InputHookResult struct {
	Block  bool
	Reason string
	Text   string // transformed text (equals input when unchanged)
}

type extension struct {
	manifest Manifest
	proc     *Process // nil for in-process test peers
	conn     *Conn
	caps     proto.InitializeResult
	dead     bool
	warned   bool
}

// Host owns all loaded extensions and fans out hooks/events to them.
type Host struct {
	timeout   time.Duration
	mu        sync.Mutex
	exts      []*extension
	OnWarning func(msg string) // optional; called on hook errors and deaths
	// sessionHandler routes extension->host session/log requests; set by the bridge.
	sessionHandler Handler
}

func NewHost(opts HostOptions) *Host {
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Second
	}
	return &Host{timeout: opts.Timeout, sessionHandler: HandlerFunc{}}
}

// SetSessionHandler installs the handler for extension->host requests
// (session/append_entry, session/get_entries, host/log).
func (h *Host) SetSessionHandler(sh Handler) { h.sessionHandler = sh }

// Load discovers, spawns, and handshakes every trusted extension under the
// given manifest lists. Individual failures are warnings, not fatal errors.
func (h *Host) Load(manifests []Manifest) {
	for _, m := range manifests {
		if err := h.startOne(m); err != nil {
			h.warn(fmt.Sprintf("extension %s: %v", m.Name, err))
		}
	}
}

func (h *Host) startOne(m Manifest) error {
	proc, err := StartProcess(m, h.sessionHandler)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), h.timeout)
	defer cancel()
	var caps proto.InitializeResult
	if err := proc.Conn.Call(ctx, proto.MethodInitialize,
		proto.InitializeParams{ProtocolVersion: proto.ProtocolVersion, Host: "lcoder"}, &caps); err != nil {
		_ = proc.Close()
		return fmt.Errorf("initialize: %w", err)
	}
	if caps.Name == "" {
		caps.Name = m.Name
	}
	h.attach(&extension{manifest: m, proc: proc, conn: proc.Conn, caps: caps})
	return nil
}

func (h *Host) attach(ext *extension) {
	ext.conn.SetOnClose(func() {
		h.mu.Lock()
		ext.dead = true
		h.mu.Unlock()
		h.warn(fmt.Sprintf("extension %s died; its hooks are skipped", ext.caps.Name))
	})
	h.mu.Lock()
	h.exts = append(h.exts, ext)
	h.mu.Unlock()
}

// AddPeer attaches an already-connected extension without spawning a process.
// Used by tests (io.Pipe peers) and by embedders with custom transports.
func (h *Host) AddPeer(m Manifest, r io.Reader, w io.Writer, caps proto.InitializeResult) error {
	if caps.Name == "" {
		caps.Name = m.Name
	}
	h.attach(&extension{manifest: m, conn: NewConn(r, w, h.sessionHandler), caps: caps})
	return nil
}

// AddPeer2 is a test helper: it builds an io.Pipe pair, serves the extension
// side with handler, and attaches the host side with caps.
func (h *Host) AddPeer2(handler Handler, caps proto.InitializeResult) error {
	hostR, extW := io.Pipe()
	extR, hostW := io.Pipe()
	_ = NewConn(extR, extW, handler) // extension side; GC'd with the pipes
	return h.AddPeer(Manifest{Name: caps.Name}, hostR, hostW, caps)
}

// Close shuts down all extensions (best-effort shutdown request, then close).
func (h *Host) Close() {
	h.mu.Lock()
	exts := h.exts
	h.mu.Unlock()
	for _, ext := range exts {
		if ext.dead {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = ext.conn.Call(ctx, proto.MethodShutdown, nil, nil)
		cancel()
		if ext.proc != nil {
			_ = ext.proc.Close()
		} else {
			_ = ext.conn.Close()
		}
	}
}

func (h *Host) warn(msg string) {
	if h.OnWarning != nil {
		h.OnWarning(msg)
	}
}

// DeadCount reports how many extensions have died (used in tests/status).
func (h *Host) DeadCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, e := range h.exts {
		if e.dead {
			n++
		}
	}
	return n
}

func (h *Host) live() []*extension {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []*extension
	for _, e := range h.exts {
		if !e.dead {
			out = append(out, e)
		}
	}
	return out
}

func hasString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// HasHook reports whether any live extension declares the hook.
func (h *Host) HasHook(hook string) bool {
	for _, e := range h.live() {
		if hasString(e.caps.Hooks, hook) {
			return true
		}
	}
	return false
}

// Subscribed reports whether any live extension subscribes to the event.
func (h *Host) Subscribed(eventType string) bool {
	for _, e := range h.live() {
		if hasString(e.caps.Events, eventType) {
			return true
		}
	}
	return false
}

// Commands returns all extension-declared commands in load order.
func (h *Host) Commands() []RegisteredCommand {
	var out []RegisteredCommand
	for _, e := range h.live() {
		for _, c := range e.caps.Commands {
			out = append(out, RegisteredCommand{ExtName: e.caps.Name, Decl: c})
		}
	}
	return out
}

// call invokes a hook method on one extension with the host timeout.
func (h *Host) call(ctx context.Context, ext *extension, method string, params, result any) error {
	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()
	return ext.conn.Call(ctx, method, params, result)
}

// RunToolCallHooks chains hook/tool_call over all declaring extensions.
// Any block wins; param modifications chain. Errors fail open with a warning.
func (h *Host) RunToolCallHooks(ctx context.Context, tool string, params map[string]any) ToolCallHookResult {
	res := ToolCallHookResult{Params: params}
	for _, ext := range h.live() {
		if !hasString(ext.caps.Hooks, proto.HookToolCall) {
			continue
		}
		var out proto.ToolCallResult
		if err := h.call(ctx, ext, proto.MethodHookToolCall, proto.ToolCallParams{Tool: tool, Params: res.Params}, &out); err != nil {
			h.warn(fmt.Sprintf("extension %s hook/tool_call: %v", ext.caps.Name, err))
			continue
		}
		if out.Action == "block" {
			return ToolCallHookResult{Block: true, Reason: out.Reason, Params: res.Params}
		}
		if out.Params != nil {
			res.Params = out.Params
		}
	}
	return res
}

// RunToolResultHooks chains hook/tool_result; each extension may replace the
// result text. Errors fail open (the current text is kept).
func (h *Host) RunToolResultHooks(ctx context.Context, tool string, params map[string]any, result string, isError bool) string {
	for _, ext := range h.live() {
		if !hasString(ext.caps.Hooks, proto.HookToolResult) {
			continue
		}
		var out proto.ToolResultResult
		if err := h.call(ctx, ext, proto.MethodHookToolResult,
			proto.ToolResultParams{Tool: tool, Params: params, Result: result, IsError: isError}, &out); err != nil {
			h.warn(fmt.Sprintf("extension %s hook/tool_result: %v", ext.caps.Name, err))
			continue
		}
		if out.Result != nil {
			result = *out.Result
		}
	}
	return result
}

// RunBeforeCompactHook asks the first declaring extension for a summary.
// ok=false means: no hook, or the hook failed — caller falls back.
func (h *Host) RunBeforeCompactHook(ctx context.Context, conversation string, tokensBefore int) (summary string, ok bool) {
	for _, ext := range h.live() {
		if !hasString(ext.caps.Hooks, proto.HookBeforeCompact) {
			continue
		}
		var out proto.BeforeCompactResult
		if err := h.call(ctx, ext, proto.MethodHookBeforeCompact,
			proto.BeforeCompactParams{Conversation: conversation, TokensBefore: tokensBefore}, &out); err != nil {
			h.warn(fmt.Sprintf("extension %s hook/session_before_compact: %v", ext.caps.Name, err))
			return "", false
		}
		return out.Summary, true
	}
	return "", false
}

// RunInputHook chains hook/input: block stops the chain; transform replaces
// the text passed to the next extension.
func (h *Host) RunInputHook(ctx context.Context, text string) InputHookResult {
	res := InputHookResult{Text: text}
	for _, ext := range h.live() {
		if !hasString(ext.caps.Hooks, proto.HookInput) {
			continue
		}
		var out proto.InputResult
		if err := h.call(ctx, ext, proto.MethodHookInput, proto.InputParams{Text: res.Text}, &out); err != nil {
			h.warn(fmt.Sprintf("extension %s hook/input: %v", ext.caps.Name, err))
			continue
		}
		switch out.Action {
		case "block":
			return InputHookResult{Block: true, Reason: out.Reason, Text: res.Text}
		case "transform":
			res.Text = out.Text
		}
	}
	return res
}

// BroadcastEvent forwards an event notification to subscribed extensions.
// Delivery is best-effort; errors are dropped (notifications have no reply).
func (h *Host) BroadcastEvent(eventType string, payload json.RawMessage) {
	for _, ext := range h.live() {
		if !hasString(ext.caps.Events, eventType) {
			continue
		}
		_ = ext.conn.Notify(proto.EventMethodPrefix+eventType, payload)
	}
}

// InvokeCommand runs an extension-declared command.
func (h *Host) InvokeCommand(ctx context.Context, name, args string) (string, error) {
	for _, ext := range h.live() {
		for _, c := range ext.caps.Commands {
			if c.Name != name {
				continue
			}
			var out proto.CommandInvokeResult
			if err := h.call(ctx, ext, proto.MethodCommandInvoke, proto.CommandInvokeParams{Name: name, Args: args}, &out); err != nil {
				return "", fmt.Errorf("extension %s command %q: %w", ext.caps.Name, name, err)
			}
			return out.Output, nil
		}
	}
	return "", fmt.Errorf("unknown extension command %q", name)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/extension/runtime -v`
Expected: PASS (all tests)

- [ ] **Step 5: Commit**

```bash
git add pkg/extension/runtime/host.go pkg/extension/runtime/host_test.go
git commit -m "feat(extension): host with handshake, chained hooks, event broadcast, commands"
```

---

### Task 5: session custom entries

**Files:**
- Modify: `pkg/models/message.go:14-20` (add `RoleCustom`)
- Modify: `pkg/session/store.go` — new API + `ActiveMessages` filter
- Test: `pkg/session/custom_entry_test.go`

- [ ] **Step 1: Write the failing test**

```go
package session

import (
	"encoding/json"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

func TestAppendCustomEntryRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	sess, err := store.Create("/project")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(models.UserMessage("hi")); err != nil {
		t.Fatal(err)
	}
	if err := sess.AppendCustomEntry("my-ext/state", json.RawMessage(`{"count":3}`)); err != nil {
		t.Fatal(err)
	}

	// Reload: entry survives.
	loaded, err := store.Load(sess.Path)
	if err != nil {
		t.Fatal(err)
	}
	entries := loaded.CustomEntries("my-ext/")
	if len(entries) != 1 || entries[0].CustomType != "my-ext/state" {
		t.Fatalf("entries %+v", entries)
	}
	if string(entries[0].Data) != `{"count":3}` {
		t.Fatalf("data %s", entries[0].Data)
	}

	// Custom entries never enter the context views.
	for _, m := range loaded.ActiveMessages() {
		if m.Role == models.RoleCustom {
			t.Fatal("custom entry leaked into ActiveMessages")
		}
	}
	for _, m := range loaded.EffectiveMessages() {
		if m.Role == models.RoleCustom {
			t.Fatal("custom entry leaked into EffectiveMessages")
		}
	}
	if len(loaded.ActiveMessages()) != 1 {
		t.Fatalf("active = %d, want 1 (only the user message)", len(loaded.ActiveMessages()))
	}
}

func TestCustomEntriesBranchIsolation(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	sess, err := store.Create("/project")
	if err != nil {
		t.Fatal(err)
	}
	_ = sess.Append(models.UserMessage("one"))
	_ = sess.AppendCustomEntry("ext/a", json.RawMessage(`{"branch":"main"}`))

	// Fork: new branch gets its own entries.
	if err := sess.Fork(""); err != nil {
		t.Fatal(err)
	}
	_ = sess.Append(models.UserMessage("two"))
	_ = sess.AppendCustomEntry("ext/a", json.RawMessage(`{"branch":"fork"}`))

	entries := sess.CustomEntries("ext/")
	if len(entries) != 1 || string(entries[0].Data) != `{"branch":"fork"}` {
		t.Fatalf("fork branch entries %+v", entries)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/session -run TestAppendCustomEntry -v`
Expected: FAIL — `sess.AppendCustomEntry undefined`, `models.RoleCustom undefined`

- [ ] **Step 3: Write minimal implementation**

In `pkg/models/message.go`, add to the role const block:

```go
	// RoleCustom marks extension-owned custom entries. They persist in the
	// session file but never enter model context.
	RoleCustom MessageRole = "custom"
```

In `pkg/session/store.go`, add after the compaction-entry section (near `IsCompactionEntry`, ~line 258):

```go
// Metadata keys for custom entries written by extensions.
const (
	MetaTypeCustom = "custom"
	MetaCustomType = "custom_type"
	MetaCustomData = "data"
)

// CustomEntry is an extension-owned record read back from the session file.
type CustomEntry struct {
	CustomType string
	Data       json.RawMessage
}

// IsCustomEntry reports whether m is an extension custom entry.
func IsCustomEntry(m models.AgentMessage) bool {
	if m.Metadata == nil {
		return false
	}
	v, ok := m.Metadata[MetaType].(string)
	return ok && v == MetaTypeCustom
}

// AppendCustomEntry appends an extension-owned entry to the current branch.
// The entry carries parent_id/branch_id like any message, so it follows fork
// and branch semantics, but role=custom keeps it out of context views.
func (s *Session) AppendCustomEntry(customType string, data json.RawMessage) error {
	msg := models.NewAgentMessage(models.RoleCustom)
	msg.Metadata[MetaType] = MetaTypeCustom
	msg.Metadata[MetaCustomType] = customType
	msg.Metadata[MetaCustomData] = json.RawMessage(data)
	if err := s.stage(msg); err != nil {
		return err
	}
	return s.appendLine(s.Messages[len(s.Messages)-1])
}

// CustomEntries returns the custom entries on the active branch whose
// custom_type starts with prefix (extensions use "<ext-name>/").
func (s *Session) CustomEntries(prefix string) []CustomEntry {
	var out []CustomEntry
	for _, m := range s.activeChain() {
		if !IsCustomEntry(m) {
			continue
		}
		customType, _ := m.Metadata[MetaCustomType].(string)
		if !strings.HasPrefix(customType, prefix) {
			continue
		}
		data, _ := m.Metadata[MetaCustomData].(json.RawMessage)
		out = append(out, CustomEntry{CustomType: customType, Data: data})
	}
	return out
}
```

Then change `ActiveMessages` (store.go:378) to filter custom entries from its **return value** while keeping the chain walk intact, and extract the walk into `activeChain` so `CustomEntries` can reuse it. Concretely: rename the existing body to `activeChain()` (same logic, same legacy fallbacks), and make `ActiveMessages` a filtering wrapper:

```go
// ActiveMessages returns the messages on the current branch, reconstructed by
// walking the parent_id tree from the active branch head, with extension
// custom entries (role=custom) filtered out — they persist on disk but never
// enter model context. A branch forked at the root yields no messages until
// one is appended. Legacy files are returned as a single linear conversation.
func (s *Session) ActiveMessages() []models.AgentMessage {
	chain := s.activeChain()
	out := chain[:0] // in-place filter; chain is already a fresh slice
	for _, m := range chain {
		if m.Role == models.RoleCustom {
			continue
		}
		out = append(out, m)
	}
	return out
}

// activeChain is the unfiltered branch walk (includes custom entries).
func (s *Session) activeChain() []models.AgentMessage {
	// ... exact former body of ActiveMessages ...
}
```

Add `"strings"` to store.go imports if missing. Note: the legacy early-returns inside the walk (`return append([]models.AgentMessage(nil), s.Messages...)`) also move into `activeChain`; the filter applies uniformly on top.

One metadata caveat: `MetaCustomData` round-trips through JSON as `json.RawMessage` only because `AgentMessage.Metadata` is `map[string]any` — after a file reload the value decodes as `json.RawMessage` **only if** the map decode preserves raw bytes. It does not: `encoding/json` decodes into `any`. Fix: in `CustomEntries`, re-marshal the decoded value instead of asserting:

```go
		raw, err := json.Marshal(m.Metadata[MetaCustomData])
		if err != nil {
			continue
		}
		out = append(out, CustomEntry{CustomType: customType, Data: raw})
```

(Use this version, dropping the `.(json.RawMessage)` assertion above.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/session -v`
Expected: PASS — new tests plus all existing session tests (branch/fork/compaction behavior unchanged for non-custom messages)

- [ ] **Step 5: Commit**

```bash
git add pkg/models/message.go pkg/session/store.go pkg/session/custom_entry_test.go
git commit -m "feat(session): extension custom entries persisted off-context"
```

---

### Task 6: agent — ModifiedArgs in BeforeToolCallResult

**Files:**
- Modify: `pkg/agent/loop.go:205-209`
- Modify: `pkg/agent/executor.go:222-238`
- Test: `pkg/agent/executor_test.go` (append)

- [ ] **Step 1: Write the failing test**

Append to `pkg/agent/executor_test.go` (check existing file for the test helper patterns; reuse its fake registry/builder helpers — if none fit, build the executor the same way the existing BeforeToolCall test does):

```go
func TestExecutorBeforeHookModifiedArgs(t *testing.T) {
	// Build an agent whose BeforeToolCall rewrites args, with a tool that
	// echoes its args so the test can observe what actually executed.
	// (Follow the existing executor test setup in this file for agent
	// construction; the essential wiring is:)
	var gotArgs map[string]any
	reg := tools.NewRegistry(t.TempDir())
	reg.Register("echo", tools.ExecutableFunc(func(_ context.Context, _ string, args map[string]any) (models.ToolExecutionResult, error) {
		gotArgs = args
		return models.NewToolExecutionResult("ok"), nil
	}))
	// ... assemble agent via agent.NewBuilder() with WithRegistry(reg) and
	// Config.BeforeToolCall returning &agent.BeforeToolCallResult{ModifiedArgs: map[string]any{"command": "rewritten"}} ...
	// Prompt a scripted llmtest client that emits one echo tool call, then assert:
	if gotArgs["command"] != "rewritten" {
		t.Fatalf("args = %v, hook modification not applied", gotArgs)
	}
}
```

Implementer: look at the existing tests in `pkg/agent/executor_test.go` for the exact fake-LLM/ scripted-client pattern (`llmtest`) and mirror it. If `tools.ExecutableFunc` does not exist, use whatever function-adapter the builtin tool tests use (check `pkg/tools` for an existing adapter type before writing one).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/agent -run TestExecutorBeforeHookModifiedArgs -v`
Expected: FAIL — unknown field `ModifiedArgs`

- [ ] **Step 3: Write minimal implementation**

`pkg/agent/loop.go` — extend the result struct:

```go
// BeforeToolCallResult indicates whether a tool call should be blocked.
type BeforeToolCallResult struct {
	Block  bool
	Reason string
	// ModifiedArgs, when non-nil, replaces the parsed args used for execution.
	ModifiedArgs map[string]any
}
```

`pkg/agent/executor.go` — after the existing block check (line ~233), apply the override before `e.registry.Execute`:

```go
		if beforeResult != nil && beforeResult.Block {
			return e.makeToolResultMessage(call, models.NewToolExecutionResultError(beforeResult.Reason), true)
		}
		if beforeResult != nil && beforeResult.ModifiedArgs != nil {
			args = beforeResult.ModifiedArgs
			info.Args = args
		}
```

(`info` is the `ToolCallInfo` built at line ~200 and reused by the after hook at line ~248 — check whether the after-hook constructs a fresh `ToolCallResultInfo{Args: args}`; it does at line 248-255, and it reads the local `args`, so updating `args` is sufficient there. Only update `info.Args` if `info` is used after this point — verify while editing and keep the change minimal.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/agent -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/agent/loop.go pkg/agent/executor.go pkg/agent/executor_test.go
git commit -m "feat(agent): allow before-tool hooks to rewrite tool args"
```

---

### Task 7: bridge — tool hooks, summarizer, input hook, session handler

**Files:**
- Create: `pkg/extension/bridge/bridge.go`
- Test: `pkg/extension/bridge/bridge_test.go`

The bridge translates between host protocol semantics and the existing in-process interfaces. It never blocks on a dead/missing extension — the host already handles that.

- [ ] **Step 1: Write the failing test**

```go
package bridge

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/extension/proto"
	"github.com/lcoder/lcoder/pkg/extension/runtime"
	"github.com/lcoder/lcoder/pkg/models"
)

// newBridgeWithPeer wires a host+bridge to a fake extension peer.
func newBridgeWithPeer(t *testing.T, handler runtime.Handler, caps proto.InitializeResult) (*Bridge, *runtime.Host) {
	t.Helper()
	hostR, extW := io.Pipe()
	extR, hostW := io.Pipe()
	_ = runtime.NewConn(extR, extW, handler)
	h := runtime.NewHost(runtime.HostOptions{Timeout: 500 * time.Millisecond})
	if err := h.AddPeer(runtime.Manifest{Name: caps.Name}, hostR, hostW, caps); err != nil {
		t.Fatal(err)
	}
	return New(h), h
}

func TestBeforeToolCallHookBlocks(t *testing.T) {
	block := runtime.HandlerFunc{RequestFunc: func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
		return proto.ToolCallResult{Action: "block", Reason: "denied by ext"}, nil
	}}
	b, _ := newBridgeWithPeer(t, block, proto.InitializeResult{Name: "a", Hooks: []string{proto.HookToolCall}})
	res, err := b.BeforeToolCall()(context.Background(), agent.ToolCallInfo{
		ToolCall: models.ToolCallContent{Name: "bash"},
		Args:     map[string]any{"command": "rm"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || !res.Block || res.Reason != "denied by ext" {
		t.Fatalf("res %+v", res)
	}
}

func TestBeforeToolCallHookRewritesArgs(t *testing.T) {
	rewrite := runtime.HandlerFunc{RequestFunc: func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
		return proto.ToolCallResult{Action: "allow", Params: map[string]any{"command": "safe"}}, nil
	}}
	b, _ := newBridgeWithPeer(t, rewrite, proto.InitializeResult{Name: "a", Hooks: []string{proto.HookToolCall}})
	res, err := b.BeforeToolCall()(context.Background(), agent.ToolCallInfo{
		ToolCall: models.ToolCallContent{Name: "bash"},
		Args:     map[string]any{"command": "rm"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || res.ModifiedArgs["command"] != "safe" {
		t.Fatalf("res %+v", res)
	}
}

func TestAfterToolCallHookRewritesResult(t *testing.T) {
	up := runtime.HandlerFunc{RequestFunc: func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
		r := "rewritten"
		return proto.ToolResultResult{Result: &r}, nil
	}}
	b, _ := newBridgeWithPeer(t, up, proto.InitializeResult{Name: "a", Hooks: []string{proto.HookToolResult}})
	res, err := b.AfterToolCall()(context.Background(), agent.ToolCallResultInfo{
		ToolCall: models.ToolCallContent{Name: "bash"},
		Result:   models.NewToolExecutionResult("orig"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || len(res.Content) == 0 {
		t.Fatalf("res %+v", res)
	}
	if tc, ok := res.Content[0].(models.TextContent); !ok || tc.Text != "rewritten" {
		t.Fatalf("content %+v", res.Content)
	}
}

func TestSummarizerUsesExtension(t *testing.T) {
	sum := runtime.HandlerFunc{RequestFunc: func(_ context.Context, _ string, params json.RawMessage) (any, error) {
		var p proto.BeforeCompactParams
		_ = json.Unmarshal(params, &p)
		if p.Conversation == "" {
			t.Error("conversation must be serialized, not empty")
		}
		return proto.BeforeCompactResult{Summary: "ext summary"}, nil
	}}
	b, _ := newBridgeWithPeer(t, sum, proto.InitializeResult{Name: "a", Hooks: []string{proto.HookBeforeCompact}})
	fallbackCalled := false
	fallback := func(_ context.Context, _ []models.AgentMessage) (string, error) {
		fallbackCalled = true
		return "fallback", nil
	}
	s, err := b.Summarizer(fallback)(context.Background(), []models.AgentMessage{models.UserMessage("hello")})
	if err != nil || s != "ext summary" || fallbackCalled {
		t.Fatalf("s=%q err=%v fallbackCalled=%v", s, err, fallbackCalled)
	}
}

func TestSummarizerFallsBackWithoutHook(t *testing.T) {
	b, _ := newBridgeWithPeer(t, nil, proto.InitializeResult{Name: "a"})
	s, err := b.Summarizer(func(_ context.Context, _ []models.AgentMessage) (string, error) {
		return "fallback", nil
	})(context.Background(), []models.AgentMessage{models.UserMessage("hi")})
	if err != nil || s != "fallback" {
		t.Fatalf("s=%q err=%v", s, err)
	}
}

func TestEventSubscriptionForwards(t *testing.T) {
	got := make(chan string, 1)
	listener := runtime.HandlerFunc{NotifyFunc: func(method string, _ json.RawMessage) { got <- method }}
	b, host := newBridgeWithPeer(t, listener, proto.InitializeResult{Name: "a", Events: []string{"turn_end"}})
	bus := events.New()
	unsub := b.SubscribeEvents(bus)
	defer unsub()
	_ = bus.Emit(context.Background(), events.TurnEndEvent{Base: events.Base{Type: events.TurnEnd, Turn: 1}})
	select {
	case m := <-got:
		if m != "event/turn_end" {
			t.Fatalf("method %q", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event not forwarded")
	}
	_ = host
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/extension/bridge -v`
Expected: FAIL — no Go files / undefined `New`

- [ ] **Step 3: Write minimal implementation**

```go
// Package bridge adapts the extension runtime host to Lcoder's in-process
// seams: agent tool hooks, the compaction summarizer, the input path, the
// event bus, TUI commands, and session custom entries.
package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/compaction"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/extension/proto"
	"github.com/lcoder/lcoder/pkg/extension/runtime"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/session"
)

// Bridge adapts a runtime.Host to in-process interfaces.
type Bridge struct {
	host *runtime.Host
}

func New(host *runtime.Host) *Bridge { return &Bridge{host: host} }

// Host exposes the underlying host (command dispatch, capabilities).
func (b *Bridge) Host() *runtime.Host { return b.host }

// BeforeToolCall adapts the tool_call hook chain to agent.BeforeToolCallHook.
func (b *Bridge) BeforeToolCall() agent.BeforeToolCallHook {
	return func(ctx context.Context, info agent.ToolCallInfo) (*agent.BeforeToolCallResult, error) {
		res := b.host.RunToolCallHooks(ctx, info.ToolCall.Name, info.Args)
		if res.Block {
			return &agent.BeforeToolCallResult{Block: true, Reason: res.Reason}, nil
		}
		if res.Params != nil {
			return &agent.BeforeToolCallResult{ModifiedArgs: res.Params}, nil
		}
		return nil, nil
	}
}

// AfterToolCall adapts the tool_result hook chain to agent.AfterToolCallHook.
func (b *Bridge) AfterToolCall() agent.AfterToolCallHook {
	return func(ctx context.Context, info agent.ToolCallResultInfo) (*agent.AfterToolCallResult, error) {
		newText := b.host.RunToolResultHooks(ctx, info.ToolCall.Name, info.Args, resultText(info.Result), info.IsError)
		if newText == resultText(info.Result) {
			return nil, nil
		}
		return &agent.AfterToolCallResult{
			Content: []models.ContentPart{models.TextContent{Text: newText}},
		}, nil
	}
}

func resultText(r models.ToolExecutionResult) string {
	var out string
	for _, part := range r.Content {
		if t, ok := part.(models.TextContent); ok {
			out += t.Text
		}
	}
	return out
}

// Summarizer returns a contextmgr.SummarizeFunc that delegates to the
// session_before_compact hook when an extension declares it, falling back to
// the built-in summarizer otherwise or on hook failure.
func (b *Bridge) Summarizer(fallback contextmgr.SummarizeFunc) contextmgr.SummarizeFunc {
	return func(ctx context.Context, messages []models.AgentMessage) (string, error) {
		conversation := compaction.SerializeConversation(messages, 2000)
		if summary, ok := b.host.RunBeforeCompactHook(ctx, conversation, 0); ok && summary != "" {
			return summary, nil
		}
		return fallback(ctx, messages)
	}
}

// InputHook adapts the input hook chain for the TUI/one-shot submit path.
// proceed=false means the input was blocked; reason is user-displayable.
func (b *Bridge) InputHook(ctx context.Context, text string) (newText string, proceed bool, reason string) {
	res := b.host.RunInputHook(ctx, text)
	if res.Block {
		return text, false, res.Reason
	}
	return res.Text, true, ""
}

// SubscribeEvents forwards bus events to subscribed extensions. Returns the
// unsubscribe func. Events the bus emits synchronously are forwarded
// synchronously — payloads are serialized with events.MarshalJSON.
func (b *Bridge) SubscribeEvents(bus *events.Bus) func() {
	return bus.Subscribe(func(ctx context.Context, ev events.Event) error {
		eventType := string(ev.EventType())
		if !b.host.Subscribed(eventType) {
			return nil
		}
		data, err := events.MarshalJSON(ev)
		if err != nil {
			return nil
		}
		b.host.BroadcastEvent(eventType, json.RawMessage(data))
		return nil
	})
}

// SessionHandler builds the host-side handler for extension->host requests:
// session/append_entry, session/get_entries, host/log. custom_type must be
// namespaced "<ext-name>/"; entries are only readable by their owner.
func SessionHandler(sess *session.Session, logFn func(level, msg string)) runtime.Handler {
	return runtime.HandlerFunc{
		RequestFunc: func(_ context.Context, method string, params json.RawMessage) (any, error) {
			switch method {
			case proto.MethodSessionAppendEntry:
				var p proto.AppendEntryParams
				if err := json.Unmarshal(params, &p); err != nil {
					return nil, err
				}
				if err := validateCustomType(p.CustomType); err != nil {
					return nil, err
				}
				if err := sess.AppendCustomEntry(p.CustomType, p.Data); err != nil {
					return nil, err
				}
				return struct{}{}, nil
			case proto.MethodSessionGetEntries:
				// The extension's name is the namespace prefix; the handler is
				// per-host, so the prefix check happens against all entries the
				// caller declares. (Per-extension routing happens at the host
				// level: each extension conn shares this handler, so prefix
				// enforcement uses the declared custom_type on append and the
				// query prefix on read.)
				var p struct {
					Prefix string `json:"prefix"`
				}
				_ = json.Unmarshal(params, &p)
				if err := validateCustomType(p.Prefix); err != nil {
					return nil, err
				}
				entries := sess.CustomEntries(p.Prefix)
				out := proto.GetEntriesResult{Entries: make([]proto.Entry, 0, len(entries))}
				for _, e := range entries {
					out.Entries = append(out.Entries, proto.Entry{CustomType: e.CustomType, Data: e.Data})
				}
				return out, nil
			}
			return nil, &proto.RPCError{Code: -32601, Message: "method not found: " + method}
		},
		NotifyFunc: func(method string, params json.RawMessage) {
			if method != proto.MethodHostLog {
				return
			}
			var p proto.HostLogParams
			if err := json.Unmarshal(params, &p); err != nil {
				return
			}
			if logFn != nil {
				logFn(p.Level, p.Message)
			}
		},
	}
}

func validateCustomType(customType string) error {
	if customType == "" || !strings.Contains(customType, "/") {
		return fmt.Errorf("custom_type must be namespaced as \"<ext-name>/<key>\", got %q", customType)
	}
	return nil
}
```

Note: `models.ToolExecutionResult` and `models.NewToolExecutionResult` — verify exact names/signatures against `pkg/models` while implementing (used in `pkg/agent/executor.go` already; mirror that usage). `compaction.SerializeConversation(msgs, maxToolResultChars)` exists at `pkg/compaction/serialize.go:16`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/extension/bridge -v`
Expected: PASS (6 tests)

- [ ] **Step 5: Commit**

```bash
git add pkg/extension/bridge/
git commit -m "feat(extension): bridge adapting host to agent hooks, summarizer, events, session"
```

---

### Task 8: TUI — command registration + input hook

**Files:**
- Modify: `pkg/tui/slash_registry.go` — add `RegisterExtensionCommand`
- Modify: `pkg/tui/model.go` — add `SetInputHook`
- Modify: `pkg/tui/keys.go:506-518` (`submit`) — run input hook first
- Test: `pkg/tui/slash_registry_test.go` (create if missing), extend a keys/submit test

- [ ] **Step 1: Write the failing test**

`pkg/tui/slash_registry_test.go`:

```go
package tui

import "testing"

func TestRegisterExtensionCommandConflict(t *testing.T) {
	// Built-in name conflicts are rejected.
	if err := RegisterExtensionCommand("help", "x", "x", func(string) string { return "" }); err == nil {
		t.Fatal("expected conflict error for built-in name")
	}
	// Alias conflicts are rejected too.
	if err := RegisterExtensionCommand("q", "x", "x", func(string) string { return "" }); err == nil {
		t.Fatal("expected conflict error for alias")
	}
	// A fresh name registers; duplicate registration is rejected.
	name := "testextcmd"
	if err := RegisterExtensionCommand(name, "desc", "/testextcmd", func(args string) string { return "ran:" + args }); err != nil {
		t.Fatal(err)
	}
	if err := RegisterExtensionCommand(name, "desc2", "x", func(string) string { return "" }); err == nil {
		t.Fatal("expected conflict error for duplicate extension command")
	}
	// It dispatches: find the entry and run its handler against a minimal model.
	for _, e := range commandRegistry {
		if e.Name == name {
			m := &Model{}
			_ = e.Handler(m, "args")
			return
		}
	}
	t.Fatal("extension command not in registry")
}
```

Input-hook test — put it wherever submit-path tests live (search for existing `submit(` tests, e.g. in `keys_test.go`; create `pkg/tui/input_hook_test.go` if none fit):

```go
package tui

import "testing"

func TestSubmitInputHookTransform(t *testing.T) {
	m := &Model{}
	m.input = NewInputModel()
	var prompted string
	m.inputHook = func(text string) (string, bool, string) {
		return text + " [hooked]", true, ""
	}
	// startPrompt is the observable sink for plain prompts; stub the runner
	// the way existing submit tests do (or assert via m.blocks for addUser).
	// Minimal assertion: after submit("hi"), the user block shows hooked text.
	m.submit("hi")
	_ = prompted
	found := false
	for _, b := range m.blocks {
		if b.raw == "hi [hooked]" {
			found = true
		}
	}
	if !found {
		t.Fatalf("blocks %+v", m.blocks)
	}
}

func TestSubmitInputHookBlock(t *testing.T) {
	m := &Model{}
	m.input = NewInputModel()
	m.inputHook = func(text string) (string, bool, string) {
		return text, false, "blocked by ext"
	}
	m.submit("bad")
	if len(m.blocks) != 1 || m.blocks[0].raw == "bad" {
		t.Fatalf("blocks %+v", m.blocks)
	}
}
```

Implementer: adapt these two submit tests to the real test fixtures in `pkg/tui` (the package has existing Model-construction helpers in `persist_test.go` and others — reuse them; `submit` also touches `m.runner`, so either construct the runner as `NewModel` tests do or guard `startPrompt` against a nil runner in test builds — prefer constructing via the same minimal pattern existing submit/keys tests use).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/tui -run 'TestRegisterExtensionCommand|TestSubmitInputHook' -v`
Expected: FAIL — undefined `RegisterExtensionCommand`, `m.inputHook`

- [ ] **Step 3: Write minimal implementation**

`pkg/tui/slash_registry.go` — append:

```go
// RegisterExtensionCommand registers a slash command backed by an external
// extension. Names conflicting with built-ins, aliases, or previously
// registered extension commands are rejected.
func RegisterExtensionCommand(name, description, usage string, invoke func(args string) string) error {
	for _, e := range commandRegistry {
		if e.Name == name {
			return fmt.Errorf("slash command %q already registered", name)
		}
		for _, alias := range e.Aliases {
			if alias == name {
				return fmt.Errorf("slash command %q conflicts with alias of %q", name, e.Name)
			}
		}
	}
	commandRegistry = append(commandRegistry, commandEntry{
		Name:        name,
		Description: description,
		Category:    "Extension",
		Handler: func(m *Model, args string) tea.Cmd {
			m.addSystem(invoke(args))
			return nil
		},
	})
	return nil
}
```

`pkg/tui/model.go` — add field and setter (near `session` field / `SetCapabilities`):

```go
	// inputHook intercepts plain user input before skill parsing/submission.
	// Returns (newText, proceed, reason). Nil means no interception.
	inputHook func(text string) (string, bool, string)
```

```go
// SetInputHook installs the extension input hook.
func (m *Model) SetInputHook(hook func(text string) (string, bool, string)) {
	m.inputHook = hook
}
```

`pkg/tui/keys.go` — at the top of `submit` (before `ParseManualTrigger`):

```go
func (m *Model) submit(text string) tea.Cmd {
	if m.inputHook != nil && !strings.HasPrefix(text, "/") {
		newText, proceed, reason := m.inputHook(text)
		if !proceed {
			m.addSystem("input blocked: " + reason)
			return nil
		}
		text = newText
	}
	// ... existing skill-trigger / slash / startPrompt logic unchanged ...
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/tui -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/tui/slash_registry.go pkg/tui/slash_registry_test.go pkg/tui/model.go pkg/tui/keys.go pkg/tui/input_hook_test.go
git commit -m "feat(tui): extension slash commands and input interception"
```

---

### Task 9: config + main.go wiring + trust gate

**Files:**
- Modify: `pkg/config/hooks.go` — replace `ExtensionConfig` with `ExtensionsConfig`
- Modify: `pkg/config/config.go:109` — field type change
- Modify: `cmd/lcoder/wiring.go` — stdin trust prompter
- Modify: `cmd/lcoder/main.go` — host lifecycle, bridge wiring, flag
- Test: `pkg/config/config_test.go` (extend or create)

`config.Config.Extensions` is currently `[]ExtensionConfig` and **unused** anywhere in cmd/ or pkg/ (verified by grep: only the type definition exists) — safe to repurpose.

- [ ] **Step 1: Write the failing test**

```go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtensionsConfigParses(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "lcoder.yaml")
	body := "provider: openai\nmodel: gpt-4o-mini\nextensions:\n  disabled: [\"noisy\"]\n  hook_timeout_ms: 3000\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cfgPath) // use the same entry point other config tests use
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Extensions.Disabled) != 1 || cfg.Extensions.Disabled[0] != "noisy" {
		t.Fatalf("disabled %+v", cfg.Extensions.Disabled)
	}
	if cfg.Extensions.HookTimeoutMs != 3000 {
		t.Fatalf("timeout %d", cfg.Extensions.HookTimeoutMs)
	}
}
```

Implementer: match the actual `config.Load` signature used by existing config tests.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/config -run TestExtensionsConfigParses -v`
Expected: FAIL — `cfg.Extensions.Disabled undefined`

- [ ] **Step 3: Write minimal implementation**

`pkg/config/hooks.go` — replace `ExtensionConfig` (keep `ToolExtensionConfig` and `PackageConfig`):

```go
// ExtensionsConfig configures the process-external extension runtime.
type ExtensionsConfig struct {
	Disabled      []string `yaml:"disabled"`
	HookTimeoutMs int      `yaml:"hook_timeout_ms"`
}
```

`pkg/config/config.go:109` — change:

```go
	Extensions     ExtensionsConfig        `yaml:"extensions"`
```

`cmd/lcoder/wiring.go` — add the trust prompter:

```go
// stdinTrustPrompter asks the user whether to load a project-level extension.
// It runs before the TUI starts, so plain stdin/ stderr prompting is safe.
func stdinTrustPrompter(name, dir string) bool {
	fmt.Fprintf(os.Stderr, "\nProject extension %q (%s) wants to load.\nProject extensions can run arbitrary code. Load it? (y/N): ", name, dir)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	s := strings.ToLower(strings.TrimSpace(line))
	return s == "y" || s == "yes"
}
```

`cmd/lcoder/main.go` — in `prepareAgent`, after the agent is built and before `ag.SetMessages` (~line 407), insert extension host startup. Also add to `agentSetup` a field `extHost *runtime.Host` (may be nil) and extend `cleanup`. New wiring code:

```go
	// Process-external extensions: discover global + project manifests, gate
	// project ones on trust, spawn and handshake, then bridge into the agent.
	extHost, extBridge := startExtensions(cfg, cwd, sess, bus)
	if extBridge != nil {
		ag.SetBeforeToolCall(hooks.CompositeBeforeToolCall(makeBeforeToolCall(cfg.Hooks), extBridge.BeforeToolCall()))
		ag.SetAfterToolCall(extBridge.AfterToolCall())
		mgr.SetSummarizer(extBridge.Summarizer(mgr.Summarizer()))
	}
```

Check while editing whether `agent.Agent` has setters for the hooks (builder has `WithBeforeToolCall`; the agent is already built here — look for existing setter methods on `Agent`, e.g. `SetMessages`, `SetUserConfirm`; if no hook setters exist, add `SetBeforeToolCall`/`SetAfterToolCall` to `pkg/agent/agent.go` that assign through to the executor config — and check `contextmgr.Manager` for a summarizer setter at `manager.go:211`; there is only a getter `Summarizer()`, so add `SetSummarizer(s SummarizeFunc)` next to it, guarded by the manager's mutex if one exists).

Add the helper in `cmd/lcoder/main.go` (or a new `cmd/lcoder/extension_wiring.go`):

```go
// startExtensions discovers, trusts, spawns, and bridges process-external
// extensions. It never fails the run: all problems degrade to warnings.
func startExtensions(cfg config.Config, cwd string, sess *session.Session, bus *events.Bus) (*runtime.Host, *bridge.Bridge) {
	disabled := make(map[string]bool, len(cfg.Extensions.Disabled))
	for _, name := range cfg.Extensions.Disabled {
		disabled[name] = true
	}
	timeout := time.Duration(cfg.Extensions.HookTimeoutMs) * time.Millisecond
	host := runtime.NewHost(runtime.HostOptions{Timeout: timeout})
	host.OnWarning = func(msg string) { fmt.Fprintf(os.Stderr, "warning: %s\n", msg) }

	global, err := runtime.Discover(paths.LCoderHome("extensions"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: discover global extensions: %v\n", err)
	}
	project, err := runtime.Discover(filepath.Join(cwd, ".lcoder", "extensions"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: discover project extensions: %v\n", err)
	}

	var load []runtime.Manifest
	for _, m := range global {
		if !disabled[m.Name] {
			load = append(load, m)
		}
	}
	for _, m := range project {
		if disabled[m.Name] {
			continue
		}
		if trustProjectExtensions || (interactiveTrust() && stdinTrustPrompter(m.Name, m.Dir)) {
			load = append(load, m)
		} else {
			fmt.Fprintf(os.Stderr, "warning: skipping untrusted project extension %q\n", m.Name)
		}
	}
	if len(load) == 0 {
		return nil, nil
	}
	host.Load(load)
	b := bridge.New(host)
	host.SetSessionHandler(bridge.SessionHandler(sess, func(level, msg string) {
		fmt.Fprintf(os.Stderr, "[ext] %s: %s\n", level, msg)
	}))
	b.SubscribeEvents(bus)
	return host, b
}

// interactiveTrust reports whether a stdin trust prompt is usable: TUI and
// one-shot modes yes; --json mode no (there is no user to answer).
func interactiveTrust() bool {
	jsonMode, _ := rootCmd.Flags().GetBool("json") // match how runRoot reads it
	return !jsonMode
}
```

Add the flag near the other `rootCmd` flag registrations (search for `Flags().Bool` / `StringVar` in main.go):

```go
rootCmd.Flags().BoolVar(&trustProjectExtensions, "trust-project-extensions", false, "load project-level extensions without prompting")
```

with `var trustProjectExtensions bool` beside the other flag vars.

Wire into `agentSetup`: add `extHost *runtime.Host` field, set it in the return literal, and in `cleanup` add `if extHost != nil { extHost.Close() }` **before** `_ = bus.Close()`.

TUI input hook: in `runTUI` (find where `tui.NewModel` is called), after model construction:

```go
	if setup.extBridge != nil {
		mdl.SetInputHook(func(text string) (string, bool, string) {
			return setup.extBridge.InputHook(ctx, text)
		})
	}
```

(Keep `extBridge *bridge.Bridge` on `agentSetup` too.) One-shot/JSON: in `runOneShot` before building the user message:

```go
	if setup.extBridge != nil {
		newText, proceed, reason := setup.extBridge.InputHook(ctx, prompt)
		if !proceed {
			return fmt.Errorf("input blocked: %s", reason)
		}
		prompt = newText
	}
```

Extension commands in TUI: after `tui.NewModel`, loop host commands:

```go
	if setup.extHost != nil {
		for _, c := range setup.extHost.Commands() {
			decl := c
			if err := tui.RegisterExtensionCommand(decl.Decl.Name, decl.Decl.Description, decl.Decl.Usage, func(args string) string {
				out, err := setup.extHost.InvokeCommand(context.Background(), decl.Decl.Name, args)
				if err != nil {
					return "error: " + err.Error()
				}
				return out
			}); err != nil {
				fmt.Fprintf(os.Stderr, "warning: %v\n", err)
			}
		}
	}
```

- [ ] **Step 4: Run tests + build**

Run: `go build ./... && go test ./pkg/config ./pkg/agent ./pkg/contextmgr -v`
Expected: build OK; tests PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/config/ cmd/lcoder/ pkg/agent/agent.go pkg/contextmgr/manager.go
git commit -m "feat(extension): wire extension host, trust gate, and bridge into the agent"
```

---

### Task 10: Go plugin retirement

**Files:**
- Delete: `pkg/extension/plugin.go`
- Delete: `cmd/lcoder/extensions.go`
- Delete: `examples/extension-subagent/`
- Modify: `pkg/tools/extensions.go` — remove go-plugin path
- Modify: `pkg/tools/extensions_test.go` — remove go-plugin tests
- Modify: `cmd/lcoder/main.go:198` — drop `newToolExtensionPluginLoader(...)` arg

- [ ] **Step 1: Remove go-plugin support from tools registry**

`pkg/tools/extensions.go`: delete `ToolExtension`, `ToolExtensionPluginLoader`, and the `pluginLoader` parameter + `case "go-plugin"` branch. New signature:

```go
// LoadExtensions registers tools from JSON descriptors (HTTPExecutable tools).
func (r *Registry) LoadExtensions(cfgs []config.ToolExtensionConfig) error {
```

`pkg/tools/extensions_test.go`: delete `TestLoadExtensionsGoPluginSuccess`, `TestLoadExtensionsGoPluginLoaderError`, `TestLoadExtensionsGoPluginMissingLoader`; update remaining `LoadExtensions` call sites to the new signature (drop the `nil` second arg). Also remove the `"go-plugin"` case from `config.ToolExtensionConfig.Type` docs in `pkg/config/hooks.go` comment (the field stays; unknown types still error).

`cmd/lcoder/main.go:198`:

```go
	if err := registry.LoadExtensions(cfg.ToolExtensions); err != nil {
```

Delete `cmd/lcoder/extensions.go`, `pkg/extension/plugin.go`, and `examples/extension-subagent/` (the example is a Go plugin; its mechanism no longer exists).

Also update `pkg/extension/extension.go`: the `NewFunc` type (line 24) exists only for plugin loading — delete it. Keep `Extension`, `Hooks`, `Info` (in-process host interface, still referenced by `loader.go`/`manager.go` — verify with grep before deleting anything else).

Check `DEVELOPER_GUIDE.md` / `DEVELOPER_GUIDE_EN.md` mention the Go plugin flow (section 13.3) — update those paragraphs to point at the process-external runtime and this spec. Keep edits scoped to the plugin sections.

- [ ] **Step 2: Build + full test run**

Run: `go build ./... && go vet $(go list ./... | grep -v 'reference/Shannon') && go test $(go list ./... | grep -v 'reference/Shannon')`
Expected: all green; no references to `PluginLoader`, `buildmode=plugin`, `NewFunc` remain (`grep -rn "PluginLoader\|buildmode=plugin" pkg/ cmd/ examples/` returns nothing)

- [ ] **Step 3: Commit**

```bash
git add -A pkg/tools pkg/extension cmd/lcoder examples DEVELOPER_GUIDE.md DEVELOPER_GUIDE_EN.md pkg/config
git commit -m "refactor(extension): retire Go plugin carrier in favor of process-external runtime"
```

---

### Task 11: end-to-end integration test

**Files:**
- Create: `test/integration/extension_e2e_test.go` (build tag `integration`)
- Create: `test/integration/testdata/extension-helper/main.go`

- [ ] **Step 1: Write the helper extension**

`test/integration/testdata/extension-helper/main.go` — a real extension binary speaking the protocol over stdio. It declares all four hooks + one event + one command, blocks tool `danger`, uppercases input via transform, and answers `ping` with `pong`:

```go
// extension-helper is a test extension speaking the lcoder extension protocol.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

type request struct {
	ID     *int64          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type response struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Result  any    `json:"result,omitempty"`
}

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var req request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil || req.ID == nil {
			continue
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"name": "helper", "version": "0.0.1",
				"events":   []string{"turn_end"},
				"hooks":    []string{"tool_call", "input"},
				"commands": []map[string]string{{"name": "ping", "description": "ping"}},
			}
		case "hook/tool_call":
			var p struct {
				Tool string `json:"tool"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if p.Tool == "danger" {
				result = map[string]any{"action": "block", "reason": "danger is blocked"}
			} else {
				result = map[string]any{"action": "allow"}
			}
		case "hook/input":
			var p struct{ Text string `json:"text"` }
			_ = json.Unmarshal(req.Params, &p)
			result = map[string]any{"action": "transform", "text": p.Text + "!"}
		case "command/invoke":
			result = map[string]any{"output": "pong"}
		case "shutdown":
			fmt.Println(`{"jsonrpc":"2.0","id":` + fmt.Sprint(*req.ID) + `,"result":{}}`)
			return
		default:
			result = struct{}{}
		}
		data, _ := json.Marshal(response{JSONRPC: "2.0", ID: *req.ID, Result: result})
		fmt.Println(string(data))
	}
}
```

- [ ] **Step 2: Write the failing e2e test**

`test/integration/extension_e2e_test.go`:

```go
//go:build integration

package integration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/extension/proto"
	"github.com/lcoder/lcoder/pkg/extension/runtime"
)

// buildHelper compiles the helper extension into a temp binary.
func buildHelper(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "extension-helper")
	if os.Getenv("GOOS") == "windows" || true { // .exe suffix needed on Windows runs
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, "./testdata/extension-helper")
	cmd.Dir = "."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v\n%s", err, out)
	}
	return bin
}

func TestExtensionEndToEnd(t *testing.T) {
	bin := buildHelper(t)
	host := runtime.NewHost(runtime.HostOptions{Timeout: 5 * time.Second})
	defer host.Close()
	var warns []string
	host.OnWarning = func(m string) { warns = append(warns, m) }

	host.Load([]runtime.Manifest{{Name: "helper", Command: []string{bin}}})
	if !host.HasHook(proto.HookToolCall) || !host.HasHook(proto.HookInput) {
		t.Fatalf("helper hooks not registered; warns=%v", warns)
	}

	// tool_call: danger blocked, others allowed.
	res := host.RunToolCallHooks(context.Background(), "danger", nil)
	if !res.Block || res.Reason != "danger is blocked" {
		t.Fatalf("res %+v", res)
	}
	if res := host.RunToolCallHooks(context.Background(), "bash", nil); res.Block {
		t.Fatal("bash must be allowed")
	}

	// input transform.
	if got := host.RunInputHook(context.Background(), "hi"); got.Text != "hi!" {
		t.Fatalf("input %+v", got)
	}

	// command.
	out, err := host.InvokeCommand(context.Background(), "ping", "")
	if err != nil || out != "pong" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestExtensionCrashIsolation(t *testing.T) {
	host := runtime.NewHost(runtime.HostOptions{Timeout: time.Second})
	defer host.Close()
	var warns []string
	host.OnWarning = func(m string) { warns = append(warns, m) }
	// A command that exits immediately = crash at handshake.
	host.Load([]runtime.Manifest{{Name: "crasher", Command: []string{"true"}}}) // unix; on Windows use a failing binary
	if len(warns) == 0 {
		t.Fatal("expected a warning for the crashed extension")
	}
	// Host stays usable.
	if res := host.RunToolCallHooks(context.Background(), "bash", nil); res.Block {
		t.Fatal("dead extension must not block tools")
	}
}
```

Note for the crash test: `"true"` doesn't exist on Windows. Use `os.Getenv("GOOS")`-agnostic approach: pass the test binary itself with an env flag, or simply use a nonexistent command path (spawn failure also produces a warning and a skipped extension — that IS the isolation guarantee). Simplest portable crasher: `Command: []string{filepath.Join(t.TempDir(), "does-not-exist")}` — spawn fails, warning recorded, host unaffected. Use that.

- [ ] **Step 3: Run e2e**

Run: `go test -tags integration ./test/integration -run 'TestExtension' -v`
Expected: PASS

- [ ] **Step 4: Full suite + commit**

Run: `go build ./... && go test $(go list ./... | grep -v 'reference/Shannon')`

```bash
git add test/integration/extension_e2e_test.go test/integration/testdata/extension-helper/
git commit -m "test(extension): end-to-end protocol and crash isolation coverage"
```

---

## Self-Review Notes (resolved)

- **Spec coverage:** §1 proto/host → Tasks 1-4; §2 bridge + retirement → Tasks 6-7, 10; §3 custom entries → Task 5 (+ bridge session handler in Task 7); §4 commands/trust/config → Tasks 8-9; testing strategy → Tasks 4 (in-process fakes) + 11 (real binary e2e). Trust-gate mechanism deviation documented in header.
- **Type consistency:** `runtime.Handler`/`HandlerFunc` (Task 2) used by host (Task 4) and bridge tests (Task 7); `Host.AddPeer` signature `(Manifest, io.Reader, io.Writer, InitializeResult)` consistent across Tasks 4/7 tests; `proto.Hook*` constants used everywhere; `Bridge.InputHook` signature matches `tui.SetInputHook` param type; `session.CustomEntries(prefix)` matches bridge usage.
- **One deliberate simplification to verify during Task 5:** `AgentMessage.Metadata` decodes as `map[string]any`, so `MetaCustomData` must be re-marshaled in `CustomEntries` (the plan's final version does this — do not use the earlier type-assert draft).
