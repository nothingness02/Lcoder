package rpcserver

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/lcoder/lcoder/pkg/agentapi"
)

// errClientDisconnected is returned to the agent when the client went away
// (stdin EOF / server shutdown) while an approval was pending; the run
// aborts with this as the block reason.
var errClientDisconnected = errors.New("rpcserver: client disconnected while approval was pending")

// approvalBridge implements agentapi.UserConfirmation over the wire: each
// Confirm becomes an approval_request and blocks until the client's
// approval_response arrives, the run's ctx is cancelled (abort), or the
// server shuts down.
type approvalBridge struct {
	srv *Server

	counter atomic.Int64
	done    chan struct{}
	once    sync.Once

	// pending maps a server-issued request id ("srv-N") to the buffered
	// channel the waiting Confirm call receives the decision on.
	mu      sync.Mutex
	pending map[string]chan agentapi.ConfirmResult
}

func newApprovalBridge(srv *Server) *approvalBridge {
	return &approvalBridge{
		srv:     srv,
		done:    make(chan struct{}),
		pending: make(map[string]chan agentapi.ConfirmResult),
	}
}

// Confirm satisfies agentapi.UserConfirmation; the scoped variant carries
// the full decision.
func (b *approvalBridge) Confirm(ctx context.Context, info agentapi.ToolCallInfo) (bool, error) {
	res, err := b.ConfirmWithScope(ctx, info)
	return res.Allow, err
}

// ConfirmWithScope emits the approval_request and blocks for the answer.
func (b *approvalBridge) ConfirmWithScope(ctx context.Context, info agentapi.ToolCallInfo) (agentapi.ConfirmResult, error) {
	deny := agentapi.ConfirmResult{Allow: false, Scope: agentapi.ScopeDeny}

	id := fmt.Sprintf("srv-%d", b.counter.Add(1))
	ch := make(chan agentapi.ConfirmResult, 1)

	b.mu.Lock()
	select {
	case <-b.done:
		b.mu.Unlock()
		return deny, errClientDisconnected
	default:
	}
	b.pending[id] = ch
	b.mu.Unlock()
	defer b.remove(id)

	err := b.srv.write(approvalRequest{
		Type: "approval_request",
		ID:   id,
		Request: approvalPayload{
			ToolCallID: info.ToolCall.ID,
			ToolName:   info.ToolCall.Name,
			Args:       info.Args,
			Command:    info.BashCommand(),
		},
	})
	if err != nil {
		return deny, fmt.Errorf("rpcserver: send approval request: %w", err)
	}

	select {
	case res := <-ch:
		return res, nil
	case <-ctx.Done():
		// Abort cancels the run's ctx: release the pending confirmation with
		// cancellation semantics so the executor stops waiting.
		return deny, ctx.Err()
	case <-b.done:
		return deny, errClientDisconnected
	}
}

// resolve delivers the client's decision to the waiting Confirm call. An
// unknown id is ignored: the request may already have been released by an
// abort or a disconnect.
func (b *approvalBridge) resolve(id string, res agentapi.ConfirmResult) {
	b.mu.Lock()
	ch, ok := b.pending[id]
	if ok {
		delete(b.pending, id)
	}
	b.mu.Unlock()
	if ok {
		ch <- res
	}
}

func (b *approvalBridge) remove(id string) {
	b.mu.Lock()
	delete(b.pending, id)
	b.mu.Unlock()
}

// close releases every pending confirmation with a disconnect error. The
// waiting Confirm calls observe done directly, so no per-channel send is
// needed; a decision racing close is harmless (buffered channel).
func (b *approvalBridge) close() {
	b.once.Do(func() { close(b.done) })
}
