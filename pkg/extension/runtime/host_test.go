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

func TestHostToolCallAllowWithoutParamsReturnsNil(t *testing.T) {
	allow := HandlerFunc{RequestFunc: func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
		return proto.ToolCallResult{Action: "allow"}, nil
	}}
	h, _ := newHostWithPeer(t, allow, proto.InitializeResult{Name: "a", Hooks: []string{proto.HookToolCall}})
	res := h.RunToolCallHooks(context.Background(), "bash", map[string]any{"command": "ls"})
	if res.Block || res.Params != nil {
		t.Fatalf("res %+v", res)
	}
	// No hooks at all: same contract.
	h2, _ := newHostWithPeer(t, nil, proto.InitializeResult{Name: "b"})
	res = h2.RunToolCallHooks(context.Background(), "bash", map[string]any{"command": "ls"})
	if res.Block || res.Params != nil {
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
	if host.DeadCount() != 1 {
		t.Fatal("host never noticed peer death")
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

func TestHostToolCallParamsChainAcrossExtensions(t *testing.T) {
	add := HandlerFunc{RequestFunc: func(_ context.Context, _ string, params json.RawMessage) (any, error) {
		var p proto.ToolCallParams
		_ = json.Unmarshal(params, &p)
		p.Params["extra"] = "added"
		return proto.ToolCallResult{Action: "allow", Params: p.Params}, nil
	}}
	seen := make(chan map[string]any, 1)
	check := HandlerFunc{RequestFunc: func(_ context.Context, _ string, params json.RawMessage) (any, error) {
		var p proto.ToolCallParams
		_ = json.Unmarshal(params, &p)
		seen <- p.Params
		return proto.ToolCallResult{Action: "allow"}, nil
	}}
	h, _ := newHostWithPeer(t, add, proto.InitializeResult{Name: "a", Hooks: []string{proto.HookToolCall}})
	if err := h.AddPeer2(check, proto.InitializeResult{Name: "b", Hooks: []string{proto.HookToolCall}}); err != nil {
		t.Fatal(err)
	}
	res := h.RunToolCallHooks(context.Background(), "bash", map[string]any{"command": "ls"})
	if res.Block || res.Params["extra"] != "added" {
		t.Fatalf("res %+v", res)
	}
	got := <-seen
	if got["extra"] != "added" {
		t.Fatalf("second extension did not see chained params: %+v", got)
	}
}

func TestHostBeforeCompactErrorFailsClosed(t *testing.T) {
	broken := HandlerFunc{RequestFunc: func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
		return nil, &proto.RPCError{Code: -32000, Message: "boom"}
	}}
	consulted := false
	never := HandlerFunc{RequestFunc: func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
		consulted = true
		return proto.BeforeCompactResult{Summary: "short"}, nil
	}}
	h, _ := newHostWithPeer(t, broken, proto.InitializeResult{Name: "a", Hooks: []string{proto.HookBeforeCompact}})
	if err := h.AddPeer2(never, proto.InitializeResult{Name: "b", Hooks: []string{proto.HookBeforeCompact}}); err != nil {
		t.Fatal(err)
	}
	s, ok := h.RunBeforeCompactHook(context.Background(), "long conversation", 1000)
	if ok || s != "" {
		t.Fatalf("s=%q ok=%v", s, ok)
	}
	if consulted {
		t.Fatal("second extension must not be consulted after an error")
	}
}
