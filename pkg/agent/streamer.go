package agent

import (
	"context"
	"encoding/json"
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
}

// stream runs one assistant turn and returns the finalized assistant message.
func (s *streamer) stream(ctx context.Context, turn int, modelRef models.ModelRef, tools []models.ToolDefinition, setAbort func(context.CancelFunc), clearAbort func()) (models.AgentMessage, error) {
	streamCtx, streamCancel := context.WithCancel(ctx)
	setAbort(streamCancel)
	defer func() {
		clearAbort()
		streamCancel()
	}()

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

	turnStartTime := time.Now()
	stream, err := s.llm.StreamTurnRetry(streamCtx, req, llm.DefaultRetryConfig())
	if err != nil {
		return models.AgentMessage{}, err
	}
	defer stream.Close()

	var partial models.AgentMessage
	started := false
	ttftRecorded := false

	for {
		select {
		case <-streamCtx.Done():
			return partial, streamCtx.Err()
		default:
		}

		ev, ok, err := stream.Next(streamCtx)
		if err != nil {
			return partial, err
		}
		if !ok {
			break
		}

		switch ev.Kind {
		case provider.KindStart:
			partial = models.NewAgentMessage(models.RoleAssistant)
			if !started {
				started = true
				s.emitter.emit(streamCtx, events.MessageStartEvent{
					Base:    events.Base{Type: events.MessageStart, Turn: turn},
					Message: partial,
				})
			}
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
				Base:    events.Base{Type: events.MessageUpdate, Turn: turn},
				Delta:   delta,
				Message: partial,
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
				Base:    events.Base{Type: events.MessageUpdate, Turn: turn},
				Delta:   ev.ArgumentsJSON,
				Message: partial,
			})
		case provider.KindDone:
			msg, err := ev.FinalMessage()
			if err != nil {
				return partial, err
			}
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
			if usage, ok := ev.Usage(); ok {
				usage.Provider = s.cfg.Model.Provider
				usage.Model = s.cfg.Model.ID
				if s.obs != nil {
					_ = s.obs.RecordLLMUsage(usage)
				}
				// Feed the provider's real prompt-token accounting back to the
				// context manager so budget decisions use real counts.
				s.mgr.RecordRealUsage(usage)
			}
			return msg, nil
		case provider.KindError:
			if ge, ok := ev.Error(); ok {
				return models.AgentMessage{}, ge
			}
			return models.AgentMessage{}, fmt.Errorf("unknown engine error")
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
	return partial, nil
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

func updateToolCall(msg models.AgentMessage, call map[string]any, final bool) models.AgentMessage {
	if call == nil {
		return msg
	}
	id, _ := call["id"].(string)
	name, _ := call["name"].(string)
	args, _ := call["arguments"].(map[string]any)

	for i, part := range msg.Content {
		if tc, ok := part.(models.ToolCallContent); ok && tc.ID == id {
			if name != "" {
				tc.Name = name
			}
			if tc.Arguments == nil {
				tc.Arguments = make(map[string]any)
			}
			for k, v := range args {
				tc.Arguments[k] = v
			}
			msg.Content[i] = tc
			return msg
		}
	}

	if args == nil {
		args = make(map[string]any)
	}
	msg.Content = append(msg.Content, models.ToolCallContent{
		ID:        id,
		Name:      name,
		Arguments: args,
	})
	return msg
}

func formatToolCallDelta(call map[string]any) string {
	if call == nil {
		return ""
	}
	b, _ := json.Marshal(call)
	return string(b)
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
