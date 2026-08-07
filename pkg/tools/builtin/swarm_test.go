package builtin

import (
	"context"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/llm/provider"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/subagent"
)

func TestSwarmDefinition(t *testing.T) {
	tool := NewSwarm("/tmp", &fakeSpawner{}, testProfiles())
	def := tool.Definition()
	if def.Name != SwarmToolName {
		t.Errorf("name = %q, want %s", def.Name, SwarmToolName)
	}
	for _, want := range []string{itemPlaceholder, "coder", "explore", "resume_agent_ids"} {
		if !strings.Contains(def.Description, want) {
			t.Errorf("description should mention %q:\n%s", want, def.Description)
		}
	}
}

func TestSwarmAggregates(t *testing.T) {
	spawner := &recordingSpawner{}
	tool := NewSwarm("/tmp", spawner, testProfiles())

	res, err := tool.Execute(context.Background(), "c1", map[string]any{
		"subagent_type":   "explore",
		"prompt_template": "Research {{item}} and report findings.",
		"items":           []any{"pkg/agent", "pkg/tools"},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	text := res.Content[0].(models.TextContent).Text
	if !strings.Contains(text, `completed="2"`) {
		t.Fatalf("swarm should complete both items, got:\n%s", text)
	}
	if !strings.Contains(text, `item="pkg/agent"`) || !strings.Contains(text, `item="pkg/tools"`) {
		t.Fatalf("result should tag each item, got:\n%s", text)
	}
}

// A non-rate-limit failure is NOT auto-retried: the item is reported failed
// and the model resumes it explicitly via resume_agent_ids (aligned with
// kimi's AgentRunBatch, whose retry path is reserved for rate limits).
func TestSwarmFailureNotAutoRetried(t *testing.T) {
	spawner := &recordingSpawner{
		spawnOut: map[string]*subagent.Outcome{
			"Research pkg/agent and report findings.": {AgentID: "agent-fail", Err: errFake},
		},
	}
	tool := NewSwarm("/tmp", spawner, testProfiles())

	res, err := tool.Execute(context.Background(), "c1", map[string]any{
		"subagent_type":   "explore",
		"prompt_template": "Research {{item}} and report findings.",
		"items":           []any{"pkg/agent", "pkg/tools"},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(spawner.resumes) != 0 {
		t.Fatalf("non-rate-limit failure must not auto-resume, got %+v", spawner.resumes)
	}
	text := res.Content[0].(models.TextContent).Text
	if !strings.Contains(text, `outcome="failed"`) || !strings.Contains(text, "agent-fail") {
		t.Fatalf("failed item should be reported with its agent id, got:\n%s", text)
	}
	if !strings.Contains(text, "resume_agent_ids") {
		t.Fatalf("failed item should carry a resume_agent_ids hint, got:\n%s", text)
	}
}

// A rate-limited item is requeued with exponential backoff and recovered via
// its own journal (resume). Two rate-limited items coordinate: each is
// requeued while the other is still unfinished, then both recover.
func TestSwarmRateLimitRecovers(t *testing.T) {
	spawner := &recordingSpawner{
		spawnOut: map[string]*subagent.Outcome{
			"Research pkg/agent and report findings.": {AgentID: "agent-rl-a", Err: rateLimitErr()},
			"Research pkg/tools and report findings.":  {AgentID: "agent-rl-b", Err: rateLimitErr()},
		},
		resumeOut: map[string]*subagent.Outcome{
			"agent-rl-a": {AgentID: "agent-rl-a", Summary: "recovered a"},
			"agent-rl-b": {AgentID: "agent-rl-b", Summary: "recovered b"},
		},
	}
	tool := NewSwarm("/tmp", spawner, testProfiles())

	res, err := tool.Execute(context.Background(), "c1", map[string]any{
		"subagent_type":   "explore",
		"prompt_template": "Research {{item}} and report findings.",
		"items":           []any{"pkg/agent", "pkg/tools"},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(spawner.resumes) != 2 {
		t.Fatalf("both rate-limited items should resume their own journals, got %+v", spawner.resumes)
	}
	text := res.Content[0].(models.TextContent).Text
	if !strings.Contains(text, "recovered a") || !strings.Contains(text, "recovered b") {
		t.Fatalf("recovered summaries should appear, got:\n%s", text)
	}
	if !strings.Contains(text, `completed="2"`) {
		t.Fatalf("both items should complete after recovery, got:\n%s", text)
	}
}

func TestSwarmValidation(t *testing.T) {
	tool := NewSwarm("/tmp", &recordingSpawner{}, testProfiles())

	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"template without placeholder", map[string]any{
			"subagent_type": "explore", "prompt_template": "no placeholder", "items": []any{"a", "b"},
		}, itemPlaceholder},
		{"fewer than 2 items", map[string]any{
			"subagent_type": "explore", "prompt_template": "do {{item}}", "items": []any{"a"},
		}, "at least 2"},
		{"duplicate expanded prompts", map[string]any{
			"subagent_type": "explore", "prompt_template": "do {{item}}", "items": []any{"a", "a"},
		}, "same prompt"},
		{"items without template", map[string]any{
			"items": []any{"a", "b"},
		}, "prompt_template"},
		{"no items and no resume", map[string]any{
			"subagent_type": "explore",
		}, "at least 2 items or a non-empty resume_agent_ids"},
		{"unknown subagent type", map[string]any{
			"subagent_type": "nope", "prompt_template": "do {{item}}", "items": []any{"a", "b"},
		}, "unknown subagent type"},
		{"empty item string", map[string]any{
			"subagent_type": "explore", "prompt_template": "do {{item}}", "items": []any{"a", ""},
		}, "non-empty string"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := tool.Execute(context.Background(), "c1", c.args)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("expected error containing %q, got %v", c.want, err)
			}
		})
	}
}

func TestSwarmTooManyItems(t *testing.T) {
	tool := NewSwarm("/tmp", &recordingSpawner{}, testProfiles())
	items := make([]any, 0, maxSwarmItems+1)
	for i := 0; i <= maxSwarmItems; i++ {
		items = append(items, "x"+string(rune('a'+i%26)))
	}
	_, err := tool.Execute(context.Background(), "c1", map[string]any{
		"prompt_template": "do {{item}}", "items": items,
	})
	if err == nil || !strings.Contains(err.Error(), "too many subagents") {
		t.Fatalf("expected too-many error, got %v", err)
	}
}

// resume_agent_ids and items mix in one call: resumes run first (sorted by
// agent id), spawns after; resumed units are tagged mode="resume".
func TestSwarmResumeMixed(t *testing.T) {
	spawner := &recordingSpawner{
		resumeOut: map[string]*subagent.Outcome{
			"agent-b": {AgentID: "agent-b", Summary: "resumed b"},
			"agent-a": {AgentID: "agent-a", Summary: "resumed a"},
		},
	}
	tool := NewSwarm("/tmp", spawner, testProfiles())

	res, err := tool.Execute(context.Background(), "c1", map[string]any{
		"subagent_type":    "coder",
		"prompt_template":  "Review {{item}}",
		"items":            []any{"x.go"},
		"resume_agent_ids": map[string]any{"agent-b": "continue b", "agent-a": "continue a"},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	// 1 spawn + 2 resumes; all complete.
	text := res.Content[0].(models.TextContent).Text
	if !strings.Contains(text, `completed="3"`) {
		t.Fatalf("mixed swarm should complete all three units, got:\n%s", text)
	}
	if !strings.Contains(text, `mode="resume" agent_id="agent-a"`) || !strings.Contains(text, `mode="resume" agent_id="agent-b"`) {
		t.Fatalf("resumed units should be tagged mode=\"resume\", got:\n%s", text)
	}
	if !strings.Contains(text, `item="x.go"`) {
		t.Fatalf("spawned unit should keep its item tag, got:\n%s", text)
	}
	if len(spawner.resumes) != 2 {
		t.Fatalf("expected 2 resume calls, got %d", len(spawner.resumes))
	}
	// Both resumed agents must be covered; execution order is not guaranteed
	// under the batch's concurrent launch (result order is, by index).
	resumed := map[string]bool{}
	for _, r := range spawner.resumes {
		resumed[r.AgentID] = true
	}
	if !resumed["agent-a"] || !resumed["agent-b"] {
		t.Fatalf("both resume targets should be resumed, got %+v", spawner.resumes)
	}
}

// resume_agent_ids alone (no items) is allowed, even with a single entry.
func TestSwarmResumeOnly(t *testing.T) {
	spawner := &recordingSpawner{
		resumeOut: map[string]*subagent.Outcome{
			"agent-x": {AgentID: "agent-x", Summary: "resumed x"},
		},
	}
	tool := NewSwarm("/tmp", spawner, testProfiles())

	res, err := tool.Execute(context.Background(), "c1", map[string]any{
		"resume_agent_ids": map[string]any{"agent-x": "keep going"},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	text := res.Content[0].(models.TextContent).Text
	if !strings.Contains(text, `completed="1"`) || !strings.Contains(text, `mode="resume" agent_id="agent-x"`) {
		t.Fatalf("resume-only swarm result malformed:\n%s", text)
	}
}

func rateLimitErr() error {
	return &provider.EventError{Code: "rate_limit", Message: "429 too many requests"}
}

var _ = errFake // shared with subagent_test.go
