package agenthost

import (
	"context"
	"fmt"

	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/events"
)

// mirror.go implements kimi-code's mirrorAgentRun: a child agent's activity
// is projected onto the parent's event bus as simplified, flat
// SubagentActivityEvents so the TUI can render it nested under the parent's
// subagent tool call. The projection is lossy on purpose — consumers never
// see two agents' interleaved message/tool streams.

// mirrorChild subscribes to the child's bus and re-emits its activity on the
// parent bus. The returned stop function takes the final outcome status
// (completed/timeout/failed) so group displays can settle per-child state.
// Mirroring is best-effort and nil safe.
func (h *Host) mirrorChild(child *agent.Agent, agentID, profile, parentToolCallID string) (stop func(status string)) {
	if h.cfg.ParentBus == nil || parentToolCallID == "" {
		return func(string) {}
	}
	emit := func(kind events.SubagentActivityKind, text string) {
		_ = h.cfg.ParentBus.Emit(context.Background(), events.SubagentActivityEvent{
			Base:             events.Base{Type: events.SubagentActivity},
			AgentID:          agentID,
			ParentToolCallID: parentToolCallID,
			Profile:          profile,
			Kind:             kind,
			Text:             text,
		})
	}
	emit(events.SubagentStarted, "")
	unsub := child.Subscribe(func(ctx context.Context, ev events.Event) error {
		switch e := ev.(type) {
		case events.MessageUpdateEvent:
			if !e.IsThinking && e.Delta != "" {
				emit(events.SubagentText, e.Delta)
			}
		case events.ToolExecutionStartEvent:
			emit(events.SubagentToolStart, e.ToolName)
		case events.ToolExecutionEndEvent:
			emit(events.SubagentToolEnd, e.ToolName)
		case events.TurnEndEvent:
			emit(events.SubagentTurn, fmt.Sprintf("%d", e.Turn))
		case events.ErrorEvent:
			emit(events.SubagentFailed, e.Message)
		}
		return nil
	})
	var stopped bool
	return func(status string) {
		if stopped {
			return
		}
		stopped = true
		unsub()
		if status == "failed" {
			emit(events.SubagentFailed, "")
		} else {
			emit(events.SubagentCompleted, status)
		}
	}
}
