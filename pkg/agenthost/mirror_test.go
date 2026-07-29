package agenthost

import (
	"context"
	"sync"
	"testing"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm/llmtest"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/subagent"
)

// busRecorder collects SubagentActivityEvents for assertions.
type busRecorder struct {
	mu     sync.Mutex
	events []events.SubagentActivityEvent
}

func (r *busRecorder) handler(_ context.Context, ev events.Event) error {
	if a, ok := ev.(events.SubagentActivityEvent); ok {
		r.mu.Lock()
		r.events = append(r.events, a)
		r.mu.Unlock()
	}
	return nil
}

func (r *busRecorder) kinds() []events.SubagentActivityKind {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]events.SubagentActivityKind, len(r.events))
	for i, e := range r.events {
		out[i] = e.Kind
	}
	return out
}

func (r *busRecorder) texts() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var s string
	for _, e := range r.events {
		s += e.Text
	}
	return s
}

// The mirror projects a child's activity onto the parent bus as flat
// activity events, tagged with agent id, parent tool call id, and profile.
func TestMirrorProjectsChildActivity(t *testing.T) {
	toolMsg := models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
		ID: "call_1", Name: "echo", Arguments: map[string]any{"text": "hi"},
	})
	client, _ := llmtest.NewScript(
		llmtest.Turn(llmtest.Start(), llmtest.Text("working"), llmtest.Done(toolMsg, nil)),
		llmtest.Turn(llmtest.Text("final answer"), llmtest.Done(models.AssistantMessage("final answer"), nil)),
	)
	host := testHost(client)

	parentBus := events.New()
	rec := &busRecorder{}
	parentBus.Subscribe(rec.handler)
	host.cfg.ParentBus = parentBus

	out := host.Spawn(context.Background(), subagent.SpawnRequest{
		Profile:          subagent.Agent{Name: "explore", Mode: "code"},
		Task:             "research",
		ParentToolCallID: "parent-call-1",
	})
	if out.Err != nil {
		t.Fatalf("spawn: %v", out.Err)
	}

	kinds := rec.kinds()
	if len(kinds) == 0 {
		t.Fatal("expected mirrored activity events")
	}
	if kinds[0] != events.SubagentStarted {
		t.Fatalf("first event should be started, got %v", kinds[0])
	}
	if kinds[len(kinds)-1] != events.SubagentCompleted {
		t.Fatalf("last event should be completed, got %v", kinds[len(kinds)-1])
	}

	var sawText, sawToolStart, sawToolEnd, sawTurn bool
	for _, e := range rec.events {
		if e.AgentID != out.AgentID || e.ParentToolCallID != "parent-call-1" || e.Profile != "explore" {
			t.Fatalf("event tagging wrong: %+v", e)
		}
		switch e.Kind {
		case events.SubagentText:
			sawText = true
		case events.SubagentToolStart:
			sawToolStart = true
		case events.SubagentToolEnd:
			sawToolEnd = true
		case events.SubagentTurn:
			sawTurn = true
		}
	}
	if !sawText || !sawToolStart || !sawToolEnd || !sawTurn {
		t.Fatalf("mirror incomplete: text=%v toolStart=%v toolEnd=%v turn=%v", sawText, sawToolStart, sawToolEnd, sawTurn)
	}
	if got := rec.texts(); !containsAll(got, "working", "final answer", "echo") {
		t.Fatalf("mirrored text should include deltas and tool names, got %q", got)
	}
}

// No parent bus or no parent tool call id: mirroring is silently disabled.
func TestMirrorDisabledWithoutParentBus(t *testing.T) {
	client, _ := llmtest.NewScript(llmtest.Turn(llmtest.Done(models.AssistantMessage("x"), nil)))
	host := testHost(client) // no ParentBus
	out := host.Spawn(context.Background(), subagent.SpawnRequest{
		Profile:          subagent.Agent{Name: "explore", Mode: "code"},
		Task:             "x",
		ParentToolCallID: "call-1",
	})
	if out.Err != nil {
		t.Fatalf("spawn without parent bus should still work: %v", out.Err)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
