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
