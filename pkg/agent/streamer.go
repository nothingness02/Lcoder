package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm"
	"github.com/lcoder/lcoder/pkg/llm/provider"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/observability"
)

// streamer owns the LLM turn streaming logic. It builds the turn request,
// applies optional context transforms, consumes provider events, and assembles
// the assistant message.
type streamer struct {
	cfg     *Config
	llm     *llm.Client
	mgr     *contextmgr.Manager
	obs     *observability.Collector
	emitter *eventEmitter

	// lastUsage is the provider usage of the most recent completed stream,
	// consumed by the run loop for StopContext and goal accounting.
	lastUsage    models.LLMUsage
	lastUsageSet bool
}

// stream runs one assistant turn and returns the finalized assistant message.
func (s *streamer) stream(ctx context.Context, turn int, modelRef models.ModelRef, tools []models.ToolDefinition, setAbort func(context.CancelFunc), clearAbort func()) (models.AgentMessage, error) {
	streamCtx, streamCancel := context.WithCancel(ctx)
	setAbort(streamCancel)
	defer func() {
		clearAbort()
		streamCancel()
	}()

	s.lastUsageSet = false

	req, err := s.mgr.BuildTurnRequest(modelRef, tools)
	if err != nil {
		return models.AgentMessage{}, fmt.Errorf("build turn request: %w", err)
	}

	if s.cfg.TransformContext != nil {
		originalLen := len(req.Messages)
		transformed, terr := s.cfg.TransformContext(ctx, req.Messages)
		if terr != nil {
			return models.AgentMessage{}, fmt.Errorf("transform context: %w", terr)
		}
		req.Messages = transformed
		// Preserve cache breakpoints only when the transform kept the message
		// count/order intact; otherwise recompute a safe minimal set.
		if len(req.Messages) != originalLen {
			req.CacheBreakpoints = minimalCacheBreakpoints(req.Messages)
		}
		// Recompute max_tokens against the transformed messages plus the system
		// prompt so the output cap still fits inside the context window.
		systemEstimate := s.mgr.EstimateTokens([]models.AgentMessage{
			models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: req.SystemPrompt}),
		})
		inputTokens := systemEstimate + s.mgr.EstimateTokens(req.Messages)
		req.Generation.MaxTokens = s.mgr.Budget().ResolveMaxTokens(inputTokens)
	}

	rc := llm.DefaultRetryConfig()
	for attempt := 0; ; attempt++ {
		stream, err := s.llm.StreamTurnRetry(streamCtx, req, rc)
		if err != nil {
			// 建流阶段的重试已在 StreamTurnRetry 内耗尽,不再嵌套重试。
			return models.AgentMessage{}, err
		}
		gotContent, msg, err := s.consume(streamCtx, stream, turn)
		if err == nil {
			return msg, nil
		}
		if gotContent || !llm.IsRetryable(err) || attempt == rc.MaxAttempts-1 {
			return msg, err
		}
		// 未流出任何内容的流内失败:整轮重试是安全的(没有任何
		// partial 输出被 emit 或持久化)。
		backoff := llm.Backoff(rc, attempt, 0)
		s.emitter.emit(streamCtx, events.LLMRetryEvent{
			Base:    events.Base{Type: events.LLMRetry, Turn: turn},
			Layer:   "turn",
			Attempt: attempt + 1,
			WaitMs:  backoff.Milliseconds(),
			Err:     err.Error(),
		})
		timer := time.NewTimer(backoff)
		select {
		case <-streamCtx.Done():
			timer.Stop()
			return msg, err
		case <-timer.C:
		}
	}
}

// consume drains one established event stream and assembles the assistant
// message. gotContent reports whether any delta (text/thinking/tool-call) was
// seen — false means a failure is safe to retry as a whole turn. KindStart
// deliberately does NOT count: every built-in adapter emits it right after
// the 200 response, before any SSE frame, so counting it would make the
// whole-turn retry unreachable on real providers (e.g. an overloaded_error
// frame arriving before the first delta).
func (s *streamer) consume(streamCtx context.Context, stream <-chan provider.Event, turn int) (gotContent bool, msg models.AgentMessage, err error) {
	turnStartTime := time.Now()

	var partial models.AgentMessage
	started := false
	ttftRecorded := false

loop:
	for {
		select {
		case <-streamCtx.Done():
			return started, partial, streamCtx.Err()
		case ev, ok := <-stream:
			if !ok {
				break loop
			}

			switch ev.Kind {
			case provider.KindStart:
				// Initialize the buffer only; MessageStart is emitted on the
				// first delta (or Done), so a pre-content failure leaves no
				// visible trace and the retry starts clean.
				partial = models.NewAgentMessage(models.RoleAssistant)
			case provider.KindTextDelta, provider.KindThinkingDelta:
				if !started {
					started = true
					partial = models.NewAgentMessage(models.RoleAssistant)
					s.emitter.emit(streamCtx, events.MessageStartEvent{
						Base:    events.Base{Type: events.MessageStart, Turn: turn},
						Message: partial,
					})
				}
				if !ttftRecorded && s.obs != nil {
					ttftRecorded = true
					_ = s.obs.RecordTTFT(turn, time.Since(turnStartTime).Milliseconds())
				}
				delta := ev.Delta
				if ev.Kind == provider.KindTextDelta {
					partial = updateText(partial, delta)
				} else {
					partial = updateThinking(partial, delta)
				}
				s.emitter.emit(streamCtx, events.MessageUpdateEvent{
					Base:       events.Base{Type: events.MessageUpdate, Turn: turn},
					Delta:      delta,
					IsThinking: ev.Kind == provider.KindThinkingDelta,
					Message:    partial,
				})
			case provider.KindToolCallDelta:
				if !started {
					started = true
					partial = models.NewAgentMessage(models.RoleAssistant)
					s.emitter.emit(streamCtx, events.MessageStartEvent{
						Base:    events.Base{Type: events.MessageStart, Turn: turn},
						Message: partial,
					})
				}
				s.emitter.emit(streamCtx, events.MessageUpdateEvent{
					Base:       events.Base{Type: events.MessageUpdate, Turn: turn},
					Delta:      ev.ArgumentsJSON,
					IsToolCall: true, // argument JSON: not assistant text
					Message:    partial,
				})
			case provider.KindDone:
				msg := ev.Message
				if !started {
					s.emitter.emit(streamCtx, events.MessageStartEvent{
						Base:    events.Base{Type: events.MessageStart, Turn: turn},
						Message: msg,
					})
				}
				s.emitter.emit(streamCtx, events.MessageEndEvent{
					Base:    events.Base{Type: events.MessageEnd, Turn: turn},
					Message: msg,
				})
				if ev.Usage != nil {
					usage := *ev.Usage
					usage.Provider = s.cfg.Model.Provider
					usage.Model = s.cfg.Model.ID
					if s.obs != nil {
						_ = s.obs.RecordLLMUsage(usage)
					}
					// Feed the provider's real prompt-token accounting back to the
					// context manager so budget decisions use real counts.
					s.mgr.RecordRealUsage(usage)
					s.lastUsage = usage
					s.lastUsageSet = true
				}
				return started, msg, nil
			case provider.KindError:
				if ev.Err != nil {
					return started, models.AgentMessage{}, ev.Err
				}
				return started, models.AgentMessage{}, fmt.Errorf("unknown engine error")
			}
		}
	}

	// Stream ended without a done event: use accumulated partial.
	if !started {
		s.emitter.emit(streamCtx, events.MessageStartEvent{
			Base:    events.Base{Type: events.MessageStart, Turn: turn},
			Message: partial,
		})
	}
	s.emitter.emit(streamCtx, events.MessageEndEvent{
		Base:    events.Base{Type: events.MessageEnd, Turn: turn},
		Message: partial,
	})
	return started, partial, nil
}

// takeUsage returns the usage of the last completed stream, if the provider
// reported one.
func (s *streamer) takeUsage() (models.LLMUsage, bool) {
	return s.lastUsage, s.lastUsageSet
}

func updateText(msg models.AgentMessage, delta string) models.AgentMessage {
	if len(msg.Content) == 0 {
		msg.Content = []models.ContentPart{models.TextContent{Text: delta}}
		return msg
	}
	if text, ok := msg.Content[0].(models.TextContent); ok {
		text.Text += delta
		msg.Content[0] = text
		return msg
	}
	msg.Content = append([]models.ContentPart{models.TextContent{Text: delta}}, msg.Content...)
	return msg
}

func updateThinking(msg models.AgentMessage, delta string) models.AgentMessage {
	if len(msg.Content) == 0 {
		msg.Content = []models.ContentPart{models.ThinkingContent{Text: delta}}
		return msg
	}
	if thinking, ok := msg.Content[0].(models.ThinkingContent); ok {
		thinking.Text += delta
		msg.Content[0] = thinking
		return msg
	}
	msg.Content = append([]models.ContentPart{models.ThinkingContent{Text: delta}}, msg.Content...)
	return msg
}

// minimalCacheBreakpoints returns a safe, minimal set of cache breakpoints for a
// flat message list. It anchors the start of the non-system messages and the
// last user turn, which is enough to make caching work after a custom context
// transform has changed message count or order.
func minimalCacheBreakpoints(msgs []models.AgentMessage) []int {
	if len(msgs) == 0 {
		return nil
	}
	var bps []int
	bps = append(bps, 0)
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == models.RoleUser {
			bps = append(bps, i)
			break
		}
	}
	return bps
}
