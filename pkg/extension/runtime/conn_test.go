package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
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
	_, client := pipePair(t, echo, nil)

	var out map[string]any
	err := client.Call(context.Background(), proto.MethodHookToolCall,
		proto.ToolCallParams{Tool: "bash", Params: map[string]any{"command": "ls"}}, &out)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if out["tool"] != "bash" {
		t.Fatalf("tool = %v, want bash", out["tool"])
	}
	params, ok := out["params"].(map[string]any)
	if !ok || params["command"] != "ls" {
		t.Fatalf("params = %v, want command=ls", out["params"])
	}
}

func TestConnCallErrorPropagates(t *testing.T) {
	h := HandlerFunc{
		RequestFunc: func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
			return nil, &proto.RPCError{Code: -32601, Message: "nope"}
		},
	}
	_, client := pipePair(t, h, nil)
	err := client.Call(context.Background(), "x/y", nil, nil)
	var rpcErr *proto.RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("err = %v, want *proto.RPCError", err)
	}
	if rpcErr.Code != -32601 {
		t.Fatalf("code = %d, want -32601", rpcErr.Code)
	}
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

func TestConnInFlightCallFailsOnLocalClose(t *testing.T) {
	block := HandlerFunc{RequestFunc: func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	_, client := pipePair(t, block, nil)

	errCh := make(chan error, 1)
	go func() { errCh <- client.Call(context.Background(), "x/y", nil, nil) }()

	// Give the call a moment to go in-flight, then close locally.
	time.Sleep(50 * time.Millisecond)
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case err := <-errCh:
		if err == nil || !strings.Contains(err.Error(), "connection lost") {
			t.Fatalf("err = %v, want connection lost", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight call did not fail after Close")
	}
}
