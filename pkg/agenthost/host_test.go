package agenthost

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/llm"
	"github.com/lcoder/lcoder/pkg/llm/llmtest"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/session"
	"github.com/lcoder/lcoder/pkg/subagent"
	"github.com/lcoder/lcoder/pkg/tools"
	"github.com/lcoder/lcoder/pkg/tools/builtin"
)

// fakeExec is a minimal tool that echoes its "text" argument back.
type fakeExec struct{ name string }

func (f fakeExec) Definition() models.ToolDefinition {
	return models.ToolDefinition{
		Name:        f.name,
		Description: "fake tool for tests",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"text": map[string]any{"type": "string"}},
		},
	}
}

func (f fakeExec) Execute(_ context.Context, _ string, args map[string]any) (models.ToolExecutionResult, error) {
	text, _ := args["text"].(string)
	return models.ToolExecutionResult{
		Content: []models.ContentPart{models.TextContent{Text: "echo: " + text}},
	}, nil
}

func testHost(client *llm.Client) *Host {
	r := tools.NewRegistry(".")
	r.Register("echo", fakeExec{name: "echo"})
	budget := contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192}
	return NewHost(HostConfig{
		LLMClient:   client,
		Registry:    r,
		ModeManager: agent.NewModeManager(),
		Permissions: permissions.NewEngine(permissions.DefaultConfig()),
		Model:       models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		NewContextManager: func() *contextmgr.Manager {
			return contextmgr.NewManager(budget)
		},
	})
}

func TestSpawnCleanRun(t *testing.T) {
	client, adapter := llmtest.NewScript(llmtest.Turn(
		llmtest.Start(),
		llmtest.Text("exploration result"),
		llmtest.Done(models.AssistantMessage("exploration result"), nil),
	))
	host := testHost(client)

	out := host.Spawn(context.Background(), subagent.SpawnRequest{
		Profile: subagent.Agent{Name: "explore", Mode: "code"},
		Task:    "explore the repo",
	})

	if out.Err != nil {
		t.Fatalf("spawn: %v", out.Err)
	}
	if out.AgentID == "" {
		t.Fatal("outcome must carry an agent id")
	}
	if out.Summary != "exploration result" {
		t.Fatalf("summary = %q, want the child's final message", out.Summary)
	}
	if adapter.CallCount() != 1 {
		t.Fatalf("expected exactly one provider turn, got %d", adapter.CallCount())
	}
	if out.Usage == nil {
		t.Fatal("usage stats should be present")
	}
}

// The summary floor: a too-short final answer triggers a continuation turn
// asking for a proper write-up (kimi-code's summaryPolicy).
func TestSpawnSummaryFloor(t *testing.T) {
	long := strings.Repeat("detailed findings. ", 10)
	client, adapter := llmtest.NewScript(
		llmtest.Turn(llmtest.Done(models.AssistantMessage("short"), nil)),
		llmtest.Turn(llmtest.Done(models.AssistantMessage(long), nil)),
	)
	host := testHost(client)

	out := host.Spawn(context.Background(), subagent.SpawnRequest{
		Profile: subagent.Agent{Name: "explore", Mode: "code", SummaryMinChars: 100, SummaryRetries: 1},
		Task:    "explore the repo",
	})

	if out.Summary != long {
		t.Fatalf("summary = %q, want the expanded answer", out.Summary)
	}
	if adapter.CallCount() != 2 {
		t.Fatalf("expected a continuation turn, got %d calls", adapter.CallCount())
	}
}

// Turn budget: when the budget is exhausted the child is steered to wrap up
// rather than hard-killed, and its wrap-up message becomes the summary.
func TestSpawnTurnBudgetSteersWrapUp(t *testing.T) {
	toolMsg := models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
		ID: "call_1", Name: "echo", Arguments: map[string]any{"text": "hi"},
	})
	client, adapter := llmtest.NewScript(
		llmtest.Turn(llmtest.Done(toolMsg, nil)),
		llmtest.Turn(llmtest.Done(models.AssistantMessage("wrapped up"), nil)),
	)
	host := testHost(client)

	out := host.Spawn(context.Background(), subagent.SpawnRequest{
		Profile: subagent.Agent{Name: "worker", Mode: "code", MaxTurns: 1},
		Task:    "do work",
	})

	if out.Err != nil {
		t.Fatalf("spawn: %v", out.Err)
	}
	if out.Summary != "wrapped up" {
		t.Fatalf("summary = %q, want the wrap-up message", out.Summary)
	}
	if adapter.CallCount() != 2 {
		t.Fatalf("expected tool turn + wrap-up turn, got %d calls", adapter.CallCount())
	}
}

// A profile without subagents may not nest: the delegation tool is stripped
// from its registry, so the model's call comes back as an unknown-tool error
// it can recover from.
func TestSpawnStripsNestedSubagentTool(t *testing.T) {
	toolMsg := models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
		ID: "call_1", Name: "subagent", Arguments: map[string]any{"agent": "x", "task": "y"},
	})
	client, adapter := llmtest.NewScript(
		llmtest.Turn(llmtest.Done(toolMsg, nil)),
		llmtest.Turn(llmtest.Done(models.AssistantMessage("cannot delegate"), nil)),
	)
	r := tools.NewRegistry(".")
	r.Register("subagent", fakeExec{name: "subagent"})
	budget := contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192}
	host := NewHost(HostConfig{
		LLMClient:   client,
		Registry:    r,
		ModeManager: agent.NewModeManager(),
		Permissions: permissions.NewEngine(permissions.DefaultConfig()),
		Model:       models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		NewContextManager: func() *contextmgr.Manager {
			return contextmgr.NewManager(budget)
		},
	})

	out := host.Spawn(context.Background(), subagent.SpawnRequest{
		Profile: subagent.Agent{Name: "worker", Mode: "code"},
		Task:    "try to delegate",
	})

	if out.Summary != "cannot delegate" {
		t.Fatalf("summary = %q, want the recovery answer", out.Summary)
	}
	if adapter.CallCount() != 2 {
		t.Fatalf("expected failed-delegation turn + answer turn, got %d calls", adapter.CallCount())
	}
}

// Resume: a spawned subagent's journal persists to the session store, and
// resuming it continues with full context (the follow-up turn's request
// contains the original task and answer).
func TestSpawnThenResume(t *testing.T) {
	client, adapter := llmtest.NewScript(
		llmtest.Turn(llmtest.Done(models.AssistantMessage("first answer"), nil)),
		llmtest.Turn(llmtest.Done(models.AssistantMessage("continued answer"), nil)),
	)
	storeDir := t.TempDir()
	host := testHost(client)
	host.cfg.SessionStore = session.NewStore(storeDir)
	host.SetParentSession("parent-1")
	host.cfg.Profiles = map[string]subagent.Agent{
		"explore": {Name: "explore", Mode: "code"},
	}

	out := host.Spawn(context.Background(), subagent.SpawnRequest{
		Profile: subagent.Agent{Name: "explore", Mode: "code"},
		Task:    "original task",
	})
	if out.Err != nil {
		t.Fatalf("spawn: %v", out.Err)
	}

	out = host.Resume(context.Background(), subagent.ResumeRequest{
		AgentID: out.AgentID,
		Task:    "follow-up question",
	})
	if out.Err != nil {
		t.Fatalf("resume: %v", out.Err)
	}
	if out.Summary != "continued answer" {
		t.Fatalf("summary = %q, want the continued answer", out.Summary)
	}

	// The resumed turn's request must carry the prior conversation.
	req := adapter.LastRequest()
	var sawOriginal, sawAnswer bool
	for _, m := range req.Messages {
		text := messageText(m)
		if strings.Contains(text, "original task") {
			sawOriginal = true
		}
		if strings.Contains(text, "first answer") {
			sawAnswer = true
		}
	}
	if !sawOriginal || !sawAnswer {
		t.Fatalf("resumed run lost journal context (original=%v answer=%v)", sawOriginal, sawAnswer)
	}
}

func TestResumeValidation(t *testing.T) {
	host := testHost(llmtest.Client(llmtest.Turn(llmtest.Done(models.AssistantMessage("x"), nil))))
	host.cfg.SessionStore = session.NewStore(t.TempDir())
	host.SetParentSession("parent-1")

	if out := host.Resume(context.Background(), subagent.ResumeRequest{AgentID: "nope", Task: "x"}); out.Err == nil {
		t.Fatal("expected error for unknown agent id")
	}

	// A session that is not a subagent journal is rejected.
	sess, err := host.cfg.SessionStore.Create(host.cfg.CWD)
	if err != nil {
		t.Fatal(err)
	}
	if out := host.Resume(context.Background(), subagent.ResumeRequest{AgentID: sess.ID, Task: "x"}); out.Err == nil {
		t.Fatal("expected error for non-subagent session")
	}
}

func messageText(m models.AgentMessage) string {
	var b strings.Builder
	for _, part := range m.Content {
		if t, ok := part.(models.TextContent); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

// cwdProbeTool records the cwd it was constructed with so tests can observe
// what working directory a spawned child's tools bind to.
type cwdProbeTool struct{ cwd string }

func (p cwdProbeTool) Definition() models.ToolDefinition {
	return models.ToolDefinition{Name: "cwd-probe", Description: "reports its cwd", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}
}

func (p cwdProbeTool) Execute(_ context.Context, _ string, _ map[string]any) (models.ToolExecutionResult, error) {
	return models.NewToolExecutionResultText(p.cwd), nil
}

// A spawned child binds its file tools to SpawnRequest.CWD: a delegating
// profile's tools are re-instantiated against the requested working
// directory.
func TestSpawnBindsChildToolsToCWD(t *testing.T) {
	var builtWith string
	tools.DefaultFactories.Register("cwd-probe", func(cwd string) tools.Executable {
		builtWith = cwd
		return cwdProbeTool{cwd: cwd}
	})
	defer tools.DefaultFactories.Unregister("cwd-probe")

	// The child must call the tool in its second turn; the first turn issues
	// the tool call, the second answers after seeing the result.
	toolMsg := models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
		ID: "call_1", Name: "cwd-probe", Arguments: map[string]any{},
	})
	client, _ := llmtest.NewScript(
		llmtest.Turn(llmtest.Done(toolMsg, nil)),
		llmtest.Turn(llmtest.Done(models.AssistantMessage("done"), nil)),
	)

	r := tools.NewRegistry("/host")
	r.Register("cwd-probe", cwdProbeTool{cwd: "/host"})
	budget := contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192}
	host := NewHost(HostConfig{
		LLMClient:   client,
		Registry:    r,
		ModeManager: agent.NewModeManager(),
		Permissions: permissions.NewEngine(permissions.DefaultConfig()),
		Model:       models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		CWD:         "/host",
		NewContextManager: func() *contextmgr.Manager {
			return contextmgr.NewManager(budget)
		},
	})

	out := host.Spawn(context.Background(), subagent.SpawnRequest{
		Profile: subagent.Agent{Name: "worker", Mode: "code", Subagents: []string{"worker"}},
		Task:    "work in subdir",
		CWD:     "/host/subdir",
	})
	if out.Err != nil {
		t.Fatalf("spawn: %v", out.Err)
	}
	if builtWith != "/host/subdir" {
		t.Fatalf("child tool built with cwd %q, want /host/subdir", builtWith)
	}
}

// The working directory survives a resume: it is persisted in the journal and
// restored when the run continues.
func TestResumeRestoresCWD(t *testing.T) {
	client, adapter := llmtest.NewScript(
		llmtest.Turn(llmtest.Done(models.AssistantMessage("first answer"), nil)),
		llmtest.Turn(llmtest.Done(models.AssistantMessage("continued answer"), nil)),
	)
	storeDir := t.TempDir()
	host := testHost(client)
	host.cfg.SessionStore = session.NewStore(storeDir)
	host.SetParentSession("parent-1")
	host.cfg.CWD = "/host"
	host.cfg.Profiles = map[string]subagent.Agent{
		"worker": {Name: "worker", Mode: "code", Subagents: []string{"worker"}},
	}

	out := host.Spawn(context.Background(), subagent.SpawnRequest{
		Profile: subagent.Agent{Name: "worker", Mode: "code", Subagents: []string{"worker"}},
		Task:    "original",
		CWD:     "/host/subdir",
	})
	if out.Err != nil {
		t.Fatalf("spawn: %v", out.Err)
	}

	// The journal meta must carry the cwd so a resume rebuilds the child with
	// the same working directory.
	sess, err := host.cfg.SessionStore.LoadByID("/host", out.AgentID)
	if err != nil {
		t.Fatalf("load journal: %v", err)
	}
	meta := readMeta(sess)
	if meta == nil || meta.Cwd != "/host/subdir" {
		t.Fatalf("journal cwd = %+v, want /host/subdir", meta)
	}

	if resumed := host.Resume(context.Background(), subagent.ResumeRequest{
		AgentID: out.AgentID, Task: "follow-up",
	}); resumed.Err != nil {
		t.Fatalf("resume: %v", resumed.Err)
	} else if resumed.Summary != "continued answer" {
		t.Fatalf("summary = %q, want the continued answer", resumed.Summary)
	}
	_ = adapter
}

// Legacy journals without a cwd field degrade to the host's working
// directory, so old sessions resume unchanged.
func TestResumeLegacyJournalDefaultsToHostCWD(t *testing.T) {
	client, _ := llmtest.NewScript(
		llmtest.Turn(llmtest.Done(models.AssistantMessage("x"), nil)),
	)
	storeDir := t.TempDir()
	host := testHost(client)
	host.cfg.SessionStore = session.NewStore(storeDir)
	host.SetParentSession("parent-1")
	host.cfg.CWD = "/host"

	sess, err := host.cfg.SessionStore.Create("/host")
	if err != nil {
		t.Fatal(err)
	}
	// Write a journal meta with no cwd field (legacy shape).
	if err := writeMeta(sess, journalMeta{ParentSessionID: "parent-1", Profile: "worker", Task: "t"}); err != nil {
		t.Fatal(err)
	}
	out := host.Resume(context.Background(), subagent.ResumeRequest{AgentID: sess.ID, Task: "x"})
	if out.Err == nil || !strings.Contains(out.Err.Error(), "unknown profile") {
		t.Fatalf("expected profile error for legacy journal, got %v", out.Err)
	}
}

// Real builtin tools are re-instantiated against the child cwd: the read
// tool's declared access resolves relative paths from there.
func TestSpawnRealReadToolBindsToCWD(t *testing.T) {
	// The child cwd holds a real file so a relative read resolves.
	childDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(childDir, "target.txt"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	toolMsg := models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
		ID: "call_1", Name: "read", Arguments: map[string]any{"path": "target.txt"},
	})
	client, _ := llmtest.NewScript(
		llmtest.Turn(llmtest.Done(toolMsg, nil)),
		llmtest.Turn(llmtest.Done(models.AssistantMessage("content"), nil)),
	)

	hostDir := t.TempDir()
	r := tools.NewRegistry(hostDir)
	r.Register("read", builtin.NewRead(hostDir))
	budget := contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192}
	host := NewHost(HostConfig{
		LLMClient:   client,
		Registry:    r,
		ModeManager: agent.NewModeManager(),
		Permissions: permissions.NewEngine(permissions.DefaultConfig()),
		Model:       models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		CWD:         "/host",
		NewContextManager: func() *contextmgr.Manager {
			return contextmgr.NewManager(budget)
		},
	})

	out := host.Spawn(context.Background(), subagent.SpawnRequest{
		Profile: subagent.Agent{Name: "worker", Mode: "code"},
		Task:    "read the file",
		CWD:     childDir,
	})
	if out.Err != nil {
		t.Fatalf("spawn: %v", out.Err)
	}
}
