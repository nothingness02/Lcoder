package builtin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/subagent"
)

var errFake = errors.New("fake failure")

type fakeSpawner struct {
	lastReq    subagent.SpawnRequest
	lastResume subagent.ResumeRequest
	outcome    *subagent.Outcome
}

func (f *fakeSpawner) Spawn(_ context.Context, req subagent.SpawnRequest) *subagent.Outcome {
	f.lastReq = req
	if f.outcome != nil {
		return f.outcome
	}
	return &subagent.Outcome{AgentID: "agent-test1", Summary: "done: " + req.Task}
}

func (f *fakeSpawner) Resume(_ context.Context, req subagent.ResumeRequest) *subagent.Outcome {
	f.lastResume = req
	if f.outcome != nil {
		return f.outcome
	}
	return &subagent.Outcome{AgentID: req.AgentID, Summary: "resumed: " + req.Task}
}

func testProfiles() map[string]subagent.Agent {
	return map[string]subagent.Agent{
		"coder":   {Name: "coder", Description: "writes code", Mode: "code"},
		"explore": {Name: "explore", Description: "reads code", Mode: "explore"},
	}
}

func TestSubagentDefinitionListsProfiles(t *testing.T) {
	tool := NewSubagent("/tmp", &fakeSpawner{}, testProfiles())
	def := tool.Definition()
	if def.Name != "subagent" {
		t.Errorf("name = %q, want subagent", def.Name)
	}
	for _, want := range []string{"coder", "explore", "zero context"} {
		if !strings.Contains(def.Description, want) {
			t.Errorf("description should mention %q:\n%s", want, def.Description)
		}
	}
}

func TestSubagentUnknownAgent(t *testing.T) {
	tool := NewSubagent("/tmp", &fakeSpawner{}, testProfiles())
	_, err := tool.Execute(context.Background(), "c1", map[string]any{
		"agent": "nope", "task": "x",
	})
	if err == nil || !strings.Contains(err.Error(), "unknown subagent") {
		t.Fatalf("expected unknown-agent error, got %v", err)
	}
	if !strings.Contains(err.Error(), "coder") {
		t.Fatalf("error should list available profiles, got %v", err)
	}
}

func TestSubagentSingleDelegates(t *testing.T) {
	spawner := &fakeSpawner{}
	tool := NewSubagent("/tmp", spawner, testProfiles())

	res, err := tool.Execute(context.Background(), "c1", map[string]any{
		"agent": "coder", "task": "implement feature X",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if spawner.lastReq.Profile.Name != "coder" || spawner.lastReq.Task != "implement feature X" {
		t.Fatalf("unexpected spawn request: %+v", spawner.lastReq)
	}
	text := res.Content[0].(models.TextContent).Text
	if !strings.Contains(text, "agent_id: agent-test1") || !strings.Contains(text, "status: completed") {
		t.Fatalf("result should carry agent id and status, got %q", text)
	}
}

func TestSubagentTimeoutOutcomeCarriesAgentID(t *testing.T) {
	spawner := &fakeSpawner{outcome: &subagent.Outcome{AgentID: "agent-slow", TimedOut: true, Summary: "partial findings"}}
	tool := NewSubagent("/tmp", spawner, testProfiles())

	res, err := tool.Execute(context.Background(), "c1", map[string]any{
		"agent": "explore", "task": "map the repo",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	text := res.Content[0].(models.TextContent).Text
	if !strings.Contains(text, "status: timeout") || !strings.Contains(text, "agent-slow") {
		t.Fatalf("timeout outcome should carry agent id, got %q", text)
	}
}

func TestSubagentFailedOutcomeShowsError(t *testing.T) {
	spawner := &fakeSpawner{outcome: &subagent.Outcome{AgentID: "agent-err", Err: errors.New("loop blew up")}}
	tool := NewSubagent("/tmp", spawner, testProfiles())

	res, err := tool.Execute(context.Background(), "c1", map[string]any{
		"agent": "coder", "task": "x",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	text := res.Content[0].(models.TextContent).Text
	if !strings.Contains(text, "status: failed") || !strings.Contains(text, "loop blew up") {
		t.Fatalf("failed outcome should show the error, got %q", text)
	}
}

func TestSubagentResumeRoutesToSpawner(t *testing.T) {
	spawner := &fakeSpawner{}
	tool := NewSubagent("/tmp", spawner, testProfiles())

	res, err := tool.Execute(context.Background(), "c1", map[string]any{
		"task": "continue the analysis", "resume": "agent-abc",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if spawner.lastResume.AgentID != "agent-abc" || spawner.lastResume.Task != "continue the analysis" {
		t.Fatalf("unexpected resume request: %+v", spawner.lastResume)
	}
	text := res.Content[0].(models.TextContent).Text
	if !strings.Contains(text, "agent_id: agent-abc") {
		t.Fatalf("resume outcome should keep the agent id, got %q", text)
	}
}

func TestSubagentTimeoutIncludesResumeHint(t *testing.T) {
	spawner := &fakeSpawner{outcome: &subagent.Outcome{AgentID: "agent-slow", TimedOut: true}}
	tool := NewSubagent("/tmp", spawner, testProfiles())

	res, err := tool.Execute(context.Background(), "c1", map[string]any{
		"agent": "explore", "task": "map the repo",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	text := res.Content[0].(models.TextContent).Text
	if !strings.Contains(text, "resume") || !strings.Contains(text, "agent-slow") {
		t.Fatalf("timeout output should carry a resume hint, got %q", text)
	}
}

// recordingSpawner returns per-task outcomes and records resume calls.
type recordingSpawner struct {
	spawnOut  map[string]*subagent.Outcome
	resumeOut map[string]*subagent.Outcome
	resumes   []subagent.ResumeRequest
}

func (f *recordingSpawner) Spawn(_ context.Context, req subagent.SpawnRequest) *subagent.Outcome {
	if out, ok := f.spawnOut[req.Task]; ok {
		return out
	}
	return &subagent.Outcome{AgentID: "agent-" + req.Task, Summary: "summary: " + req.Task}
}

func (f *recordingSpawner) Resume(_ context.Context, req subagent.ResumeRequest) *subagent.Outcome {
	f.resumes = append(f.resumes, req)
	if out, ok := f.resumeOut[req.AgentID]; ok {
		return out
	}
	return &subagent.Outcome{AgentID: req.AgentID, Summary: "resumed summary"}
}

func TestSubagentBackgroundNotifies(t *testing.T) {
	release := make(chan struct{})
	spawner := &blockingSpawner{release: release}
	tool := NewSubagent("/tmp", spawner, testProfiles())

	res, err := tool.Execute(context.Background(), "c1", map[string]any{
		"agent": "explore", "task": "slow research", "run_in_background": true,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	text := res.Content[0].(models.TextContent).Text
	if !strings.Contains(text, "started background subagent") || !strings.Contains(text, "do not poll") {
		t.Fatalf("background start should set expectations, got %q", text)
	}

	close(release)
	deadline := time.After(2 * time.Second)
	for {
		notes := tool.DrainNotifications()
		if len(notes) > 0 {
			if !strings.Contains(notes[0], "completed") || !strings.Contains(notes[0], "bg result") {
				t.Fatalf("unexpected notification: %q", notes[0])
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("no background notification arrived")
		case <-time.After(10 * time.Millisecond):
		}
	}
	// Drained: second drain is empty.
	if notes := tool.DrainNotifications(); len(notes) != 0 {
		t.Fatalf("notifications should be drained, got %v", notes)
	}
}

type blockingSpawner struct {
	release chan struct{}
}

func (b *blockingSpawner) Spawn(_ context.Context, req subagent.SpawnRequest) *subagent.Outcome {
	<-b.release
	return &subagent.Outcome{AgentID: "agent-bg", Summary: "bg result"}
}

func (b *blockingSpawner) Resume(_ context.Context, req subagent.ResumeRequest) *subagent.Outcome {
	return &subagent.Outcome{AgentID: req.AgentID}
}
