// pkg/llm/llmtest/llmtest.go
package llmtest

import (
	"context"
	"sync"
	"time"

	"github.com/lcoder/lcoder/pkg/llm"
	"github.com/lcoder/lcoder/pkg/llm/catalog"
	"github.com/lcoder/lcoder/pkg/llm/engine"
	"github.com/lcoder/lcoder/pkg/llm/provider"
	"github.com/lcoder/lcoder/pkg/models"
)

// ScriptAdapter replays one event script per turn. Each Stream call pops the
// next script; once exhausted it repeats the last one. When Delay > 0 events
// are emitted one per Delay (respecting ctx cancellation) so abort-mid-stream
// behavior can be exercised.
type ScriptAdapter struct {
	turns [][]provider.Event
	Delay time.Duration

	mu      sync.Mutex
	Calls   int
	LastReq models.TurnRequest
}

func (s *ScriptAdapter) Stream(ctx context.Context, conn provider.Conn, req models.TurnRequest) (<-chan provider.Event, error) {
	s.mu.Lock()
	script := s.turns[len(s.turns)-1]
	if s.Calls < len(s.turns) {
		script = s.turns[s.Calls]
	}
	s.Calls++
	s.LastReq = req
	delay := s.Delay
	s.mu.Unlock()

	ch := make(chan provider.Event)
	go func() {
		defer close(ch)
		for _, e := range script {
			if delay > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(delay):
				}
			}
			select {
			case <-ctx.Done():
				return
			case ch <- e:
			}
		}
	}()
	return ch, nil
}

// CallCount returns the number of Stream invocations so far.
func (s *ScriptAdapter) CallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Calls
}

// LastRequest returns the most recent turn request seen by the adapter.
func (s *ScriptAdapter) LastRequest() models.TurnRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.LastReq
}

// NewScript builds a client and exposes its adapter, so tests can assert on the
// number of turns or the last request. Provider routes are pre-registered so any
// test ModelRef resolves.
func NewScript(turns ...[]provider.Event) (*llm.Client, *ScriptAdapter) {
	cat := catalog.New(catalog.Options{Refresh: false})
	eng := engine.New(cat)
	adapter := &ScriptAdapter{turns: turns}
	eng.SetAdapterFactory(func(p provider.Protocol, marks provider.CacheMarks) provider.Adapter { return adapter })
	for _, p := range []string{"openai", "anthropic", "deepseek", "moonshot", "openrouter", "gemini"} {
		eng.RegisterProvider(p, provider.Conn{Route: p})
	}
	return llm.NewClient(eng), adapter
}

// Client builds an *llm.Client whose every turn is served by the given event
// scripts (one slice per turn).
func Client(turns ...[]provider.Event) *llm.Client {
	c, _ := NewScript(turns...)
	return c
}

// Turn bundles events into a single turn script.
func Turn(events ...provider.Event) []provider.Event { return events }

// RepeatText returns n text-delta events each carrying s.
func RepeatText(s string, n int) []provider.Event {
	out := make([]provider.Event, n)
	for i := range out {
		out[i] = Text(s)
	}
	return out
}

// Helpers to build common events.
func Start() provider.Event { return provider.Event{Kind: provider.KindStart} }
func Text(s string) provider.Event {
	return provider.Event{Kind: provider.KindTextDelta, Delta: s}
}
func ToolCall(index int, args string) provider.Event {
	return provider.Event{Kind: provider.KindToolCallDelta, ToolCallIndex: index, ArgumentsJSON: args}
}
func Done(msg models.AgentMessage, usage *models.LLMUsage) provider.Event {
	return provider.Event{Kind: provider.KindDone, Message: msg, Usage: usage}
}
func ErrorEvent(code, message string) provider.Event {
	return provider.Event{Kind: provider.KindError, Err: &provider.EventError{Code: code, Message: message}}
}
