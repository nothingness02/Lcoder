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
// If the writer is an io.Closer it is closed so the peer observes EOF.
func (c *Conn) Close() error {
	c.failAll()
	if cl, ok := c.w.(io.Closer); ok {
		return cl.Close()
	}
	return nil
}
