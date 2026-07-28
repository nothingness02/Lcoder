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
	"github.com/lcoder/lcoder/pkg/session"
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
		Result:   models.NewToolExecutionResultText("orig"),
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
	fallback := func(_ context.Context, _ []models.AgentMessage, _ string) (string, error) {
		fallbackCalled = true
		return "fallback", nil
	}
	s, err := b.Summarizer(fallback)(context.Background(), []models.AgentMessage{models.UserMessage("hello")}, "")
	if err != nil || s != "ext summary" || fallbackCalled {
		t.Fatalf("s=%q err=%v fallbackCalled=%v", s, err, fallbackCalled)
	}
}

func TestSummarizerFallsBackWithoutHook(t *testing.T) {
	b, _ := newBridgeWithPeer(t, nil, proto.InitializeResult{Name: "a"})
	s, err := b.Summarizer(func(_ context.Context, _ []models.AgentMessage, _ string) (string, error) {
		return "fallback", nil
	})(context.Background(), []models.AgentMessage{models.UserMessage("hi")}, "")
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

func TestInputHookBlockAndTransform(t *testing.T) {
	handler := runtime.HandlerFunc{RequestFunc: func(_ context.Context, _ string, params json.RawMessage) (any, error) {
		var p proto.InputParams
		_ = json.Unmarshal(params, &p)
		if p.Text == "evil" {
			return proto.InputResult{Action: "block", Reason: "blocked"}, nil
		}
		return proto.InputResult{Action: "transform", Text: p.Text + "!"}, nil
	}}
	b, _ := newBridgeWithPeer(t, handler, proto.InitializeResult{Name: "a", Hooks: []string{proto.HookInput}})

	if text, proceed, reason := b.InputHook(context.Background(), "evil"); proceed || reason != "blocked" || text != "evil" {
		t.Fatalf("blocked input: text=%q proceed=%v reason=%q", text, proceed, reason)
	}
	if text, proceed, reason := b.InputHook(context.Background(), "hi"); !proceed || text != "hi!" || reason != "" {
		t.Fatalf("transformed input: text=%q proceed=%v reason=%q", text, proceed, reason)
	}
}

func TestBeforeToolCallHookAllowWithoutParams(t *testing.T) {
	allow := runtime.HandlerFunc{RequestFunc: func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
		return proto.ToolCallResult{Action: "allow"}, nil
	}}
	b, _ := newBridgeWithPeer(t, allow, proto.InitializeResult{Name: "a", Hooks: []string{proto.HookToolCall}})
	res, err := b.BeforeToolCall()(context.Background(), agent.ToolCallInfo{
		ToolCall: models.ToolCallContent{Name: "bash"},
		Args:     map[string]any{"command": "ls"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res != nil {
		t.Fatalf("unchanged args must yield nil result, got %+v", res)
	}
}

func TestAfterToolCallHookUnchangedResult(t *testing.T) {
	// Hook declared but returns no Result pointer: text flows through unchanged.
	noop := runtime.HandlerFunc{RequestFunc: func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
		return proto.ToolResultResult{}, nil
	}}
	b, _ := newBridgeWithPeer(t, noop, proto.InitializeResult{Name: "a", Hooks: []string{proto.HookToolResult}})
	res, err := b.AfterToolCall()(context.Background(), agent.ToolCallResultInfo{
		ToolCall: models.ToolCallContent{Name: "bash"},
		Result:   models.NewToolExecutionResultText("orig"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res != nil {
		t.Fatalf("unchanged result must yield nil, got %+v", res)
	}
	// No extension declares tool_result: same contract.
	b2, _ := newBridgeWithPeer(t, nil, proto.InitializeResult{Name: "b"})
	res, err = b2.AfterToolCall()(context.Background(), agent.ToolCallResultInfo{
		ToolCall: models.ToolCallContent{Name: "bash"},
		Result:   models.NewToolExecutionResultText("orig"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res != nil {
		t.Fatalf("no hook must yield nil, got %+v", res)
	}
}

func TestSummarizerFallsBackOnEmptySummary(t *testing.T) {
	empty := runtime.HandlerFunc{RequestFunc: func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
		return proto.BeforeCompactResult{Summary: ""}, nil
	}}
	b, _ := newBridgeWithPeer(t, empty, proto.InitializeResult{Name: "a", Hooks: []string{proto.HookBeforeCompact}})
	fallbackCalled := false
	s, err := b.Summarizer(func(_ context.Context, _ []models.AgentMessage, _ string) (string, error) {
		fallbackCalled = true
		return "fallback", nil
	})(context.Background(), []models.AgentMessage{models.UserMessage("hi")}, "")
	if err != nil || s != "fallback" || !fallbackCalled {
		t.Fatalf("s=%q err=%v fallbackCalled=%v", s, err, fallbackCalled)
	}
}

func TestEventSubscriptionSkipsUnsubscribed(t *testing.T) {
	got := make(chan string, 1)
	listener := runtime.HandlerFunc{NotifyFunc: func(method string, _ json.RawMessage) { got <- method }}
	b, _ := newBridgeWithPeer(t, listener, proto.InitializeResult{Name: "a", Events: []string{"turn_end"}})
	bus := events.New()
	unsub := b.SubscribeEvents(bus)
	defer unsub()
	_ = bus.Emit(context.Background(), events.TurnStartEvent{Base: events.Base{Type: events.TurnStart, Turn: 1}})
	select {
	case m := <-got:
		t.Fatalf("unsubscribed event forwarded: %s", m)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestSessionHandlerAppendAndGetEntries(t *testing.T) {
	store := session.NewStore(t.TempDir())
	sess, err := store.Create("/project")
	if err != nil {
		t.Fatal(err)
	}
	var logged string
	h := SessionHandler(sess, func(level, msg string) { logged = level + ":" + msg })
	ctx := context.Background()

	appendParams, _ := json.Marshal(proto.AppendEntryParams{CustomType: "my-ext/state", Data: json.RawMessage(`{"n":1}`)})
	if _, err := h.HandleRequest(ctx, proto.MethodSessionAppendEntry, appendParams); err != nil {
		t.Fatal(err)
	}

	getParams, _ := json.Marshal(map[string]string{"prefix": "my-ext/"})
	res, err := h.HandleRequest(ctx, proto.MethodSessionGetEntries, getParams)
	if err != nil {
		t.Fatal(err)
	}
	out := res.(proto.GetEntriesResult)
	if len(out.Entries) != 1 || out.Entries[0].CustomType != "my-ext/state" || string(out.Entries[0].Data) != `{"n":1}` {
		t.Fatalf("entries %+v", out.Entries)
	}

	// Un-namespaced custom_type is rejected.
	bad, _ := json.Marshal(proto.AppendEntryParams{CustomType: "noslash", Data: json.RawMessage(`{}`)})
	if _, err := h.HandleRequest(ctx, proto.MethodSessionAppendEntry, bad); err == nil {
		t.Fatal("expected error for un-namespaced custom_type")
	}

	// host/log notification reaches the log func.
	logParams, _ := json.Marshal(proto.HostLogParams{Level: "info", Message: "hello"})
	h.HandleNotification(proto.MethodHostLog, logParams)
	if logged != "info:hello" {
		t.Fatalf("logged %q", logged)
	}
}
