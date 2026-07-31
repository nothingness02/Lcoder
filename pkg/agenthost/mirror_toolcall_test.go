package agenthost

import (
	"context"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/llm/llmtest"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/subagent"
)

// Tool-call argument JSON must not leak into the parent transcript as
// mirrored subagent text.
func TestMirrorSkipsToolCallDeltas(t *testing.T) {
	toolMsg := models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
		ID: "call_1", Name: "echo", Arguments: map[string]any{"text": "hi"},
	})
	client, _ := llmtest.NewScript(
		llmtest.Turn(
			llmtest.Start(),
			llmtest.ToolCall(0, `{"text":"hi"}`),
			llmtest.Done(toolMsg, nil),
		),
		llmtest.Turn(llmtest.Text("done"), llmtest.Done(models.AssistantMessage("done"), nil)),
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
	if got := rec.texts(); strings.Contains(got, `"text":"hi"`) {
		t.Fatalf("tool-call JSON leaked into mirrored text: %q", got)
	}
}
