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
	_, client := pipePair(t, echo, nil)

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
