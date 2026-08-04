package rpcserver

import (
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/host"
	"github.com/lcoder/lcoder/pkg/llm"
	"github.com/lcoder/lcoder/pkg/llm/llmtest"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/session"
	"github.com/lcoder/lcoder/pkg/tools"
	"github.com/lcoder/lcoder/pkg/tools/builtin"
)

// newRealFixture assembles a host.Core over a real agent served by the
// scripted LLM client (same pattern as pkg/host's tests), then serves it
// over in-memory pipes.
func newRealFixture(t *testing.T, client *llm.Client, perms *permissions.Engine) *rpcFixture {
	t.Helper()
	if perms == nil {
		perms = permissions.NewEngine(permissions.DefaultConfig())
	}
	reg := tools.NewRegistry(t.TempDir())
	for _, factory := range builtin.Factories() {
		reg.RegisterBuiltin(factory)
	}
	mgr := contextmgr.NewManager(
		contextmgr.TokenBudget{MaxTotal: 128000, TargetTotal: 120000, ReserveOutput: 8192, MaxOutput: 16384},
	)
	store := session.NewStore(t.TempDir())
	sess, err := store.Create("rpc-test-cwd")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	bus := events.New()
	ag := agent.New(agent.Config{
		SystemPrompt:   "x",
		Model:          models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ContextManager: mgr,
		SessionID:      sess.ID,
	}, client, reg, perms, bus)
	core := host.NewCore(ag, bus, store, sess, "rpc-test-cwd", nil)
	t.Cleanup(core.Close)
	return newFixture(t, core, bus, Options{Model: models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"}})
}

func textMsg(text string) models.AgentMessage {
	return models.NewAgentMessage(models.RoleAssistant, models.TextContent{Text: text})
}

func bashCallMsg(id, command string) models.AgentMessage {
	return models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
		Type: "tool_call", ID: id, Name: "bash",
		Arguments: map[string]any{"command": command},
	})
}

// collect reads until pred matches and returns every line seen (in order).
func (f *rpcFixture) collect(t *testing.T, pred func(map[string]any) bool) []map[string]any {
	t.Helper()
	var seen []map[string]any
	for {
		m := f.next(t)
		seen = append(seen, m)
		if pred(m) {
			return seen
		}
	}
}

func isEvent(m map[string]any, typ string) bool {
	if m["type"] != "event" {
		return false
	}
	ev, ok := m["event"].(map[string]any)
	return ok && ev["type"] == typ
}

// Full chain: prompt command → accepted response → event stream
// (agent_start … message_* … agent_end) in order.
func TestPromptFullChainEmitsEventSequence(t *testing.T) {
	client := llmtest.Client(llmtest.Turn(llmtest.Done(textMsg("turn reply"), nil)))
	f := newRealFixture(t, client, nil)

	f.send(t, map[string]any{"id": "p", "type": "prompt", "text": "hi"})

	// The accept response precedes every event of the run.
	first := f.next(t)
	if first["type"] != "response" || first["id"] != "p" || first["ok"] != true {
		t.Fatalf("first line must be the accept response, got %v", first)
	}

	lines := f.collect(t, func(m map[string]any) bool { return isEvent(m, "agent_end") })
	var order []string
	for _, m := range lines {
		if ev, ok := m["event"].(map[string]any); ok {
			order = append(order, ev["type"].(string))
		}
	}
	if len(order) < 2 || order[0] != "agent_start" || order[len(order)-1] != "agent_end" {
		t.Fatalf("event order = %v, want agent_start … agent_end", order)
	}
	// The assistant text must appear in a message_end event.
	foundText := false
	for _, m := range lines {
		if !isEvent(m, "message_end") {
			continue
		}
		msg := m["event"].(map[string]any)["message"].(map[string]any)
		for _, part := range msg["content"].([]any) {
			if p, ok := part.(map[string]any); ok && p["text"] == "turn reply" {
				foundText = true
			}
		}
	}
	if !foundText {
		t.Fatal("no message_end event carried the assistant text")
	}

	// The run is over: the transcript is queryable via get_state. running may
	// stay true for a brief moment after agent_end (only the run goroutine
	// clears it), so poll until it flips.
	deadline := time.Now().Add(5 * time.Second)
	var data map[string]any
	for {
		f.send(t, map[string]any{"id": "s", "type": "get_state"})
		m := f.nextOfType(t, "response")
		data = m["data"].(map[string]any)
		if data["running"] == false {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("running after agent_end = %v", data["running"])
		}
	}
	if msgs := data["messages"].([]any); len(msgs) < 2 {
		t.Fatalf("transcript after run = %d messages, want ≥ 2", len(msgs))
	}
	f.closeStdin(t)
}

// Approval over the wire, allow path: an Ask-gated bash call emits an
// approval_request; scope=once lets the tool run and the run completes.
func TestApprovalAllowPath(t *testing.T) {
	perms := permissions.NewEngineFromRules([]permissions.Rule{
		{Tool: "bash", Pattern: "*", Decision: permissions.Ask},
	})
	client := llmtest.Client(
		llmtest.Turn(llmtest.Done(bashCallMsg("b1", "echo rpc-approval-ok"), nil)),
		llmtest.Turn(llmtest.Done(textMsg("tool done"), nil)),
	)
	f := newRealFixture(t, client, perms)

	f.send(t, map[string]any{"id": "p", "type": "prompt", "text": "run it"})
	f.next(t) // accept response

	req := f.nextOfType(t, "approval_request")
	payload := req["request"].(map[string]any)
	if payload["tool_name"] != "bash" || payload["command"] != "echo rpc-approval-ok" {
		t.Fatalf("approval payload = %v", payload)
	}
	f.send(t, map[string]any{"type": "approval_response", "id": req["id"], "result": map[string]any{"scope": "once"}})

	lines := f.collect(t, func(m map[string]any) bool { return isEvent(m, "agent_end") })
	sawToolEnd := false
	for _, m := range lines {
		if isEvent(m, "tool_execution_end") {
			sawToolEnd = true
		}
	}
	if !sawToolEnd {
		t.Fatal("approved tool call never executed")
	}
	f.closeStdin(t)
}

// Deny path: scope=deny blocks the tool; the run still completes and the
// audit event records the block.
func TestApprovalDenyPath(t *testing.T) {
	perms := permissions.NewEngineFromRules([]permissions.Rule{
		{Tool: "bash", Pattern: "*", Decision: permissions.Ask},
	})
	client := llmtest.Client(
		llmtest.Turn(llmtest.Done(bashCallMsg("b1", "echo should-not-run"), nil)),
		llmtest.Turn(llmtest.Done(textMsg("aborted tool"), nil)),
	)
	f := newRealFixture(t, client, perms)

	f.send(t, map[string]any{"id": "p", "type": "prompt", "text": "run it"})
	f.next(t)

	req := f.nextOfType(t, "approval_request")
	f.send(t, map[string]any{"type": "approval_response", "id": req["id"], "result": map[string]any{"scope": "deny"}})

	lines := f.collect(t, func(m map[string]any) bool { return isEvent(m, "agent_end") })
	sawBlockedAudit, sawToolEnd := false, false
	for _, m := range lines {
		if isEvent(m, "audit") {
			ev := m["event"].(map[string]any)
			if ev["blocked"] == true {
				sawBlockedAudit = true
			}
		}
		if isEvent(m, "tool_execution_end") {
			sawToolEnd = true
		}
	}
	if !sawBlockedAudit {
		t.Fatal("no blocked audit event after deny")
	}
	if sawToolEnd {
		t.Fatal("denied tool call must not execute")
	}
	f.closeStdin(t)
}

// Abort while an approval is pending releases the confirmation (run ctx
// cancellation) and ends the run.
func TestAbortReleasesPendingApproval(t *testing.T) {
	perms := permissions.NewEngineFromRules([]permissions.Rule{
		{Tool: "bash", Pattern: "*", Decision: permissions.Ask},
	})
	client := llmtest.Client(
		llmtest.Turn(llmtest.Done(bashCallMsg("b1", "echo abort-me"), nil)),
		llmtest.Turn(llmtest.Done(textMsg("never"), nil)),
	)
	f := newRealFixture(t, client, perms)

	f.send(t, map[string]any{"id": "p", "type": "prompt", "text": "run it"})
	f.next(t)
	f.nextOfType(t, "approval_request")

	// Never answer the approval; abort instead. The released confirmation may
	// emit an audit event before the abort response lands, so skip to it.
	f.send(t, map[string]any{"id": "a", "type": "abort"})
	for {
		m := f.next(t)
		if m["type"] == "response" && m["id"] == "a" {
			if m["ok"] != true {
				t.Fatalf("abort response = %v", m)
			}
			break
		}
	}
	lines := f.collect(t, func(m map[string]any) bool { return isEvent(m, "agent_end") })
	var reason string
	for _, m := range lines {
		if isEvent(m, "agent_end") {
			reason, _ = m["event"].(map[string]any)["reason"].(string)
		}
	}
	if reason != string(events.EndReasonInterrupted) && reason != string(events.EndReasonError) {
		t.Fatalf("agent_end reason = %q, want interrupted/error", reason)
	}

	// The server is still usable afterwards.
	f.send(t, map[string]any{"id": "s", "type": "get_state"})
	if m := f.nextOfType(t, "response"); m["ok"] != true {
		t.Fatalf("get_state after abort = %v", m)
	}
	f.closeStdin(t)
}

// The continue command drives a run without a new user message.
func TestContinueCommand(t *testing.T) {
	client, adapter := llmtest.NewScript(llmtest.Turn(llmtest.Done(textMsg("continued"), nil)))
	f := newRealFixture(t, client, nil)

	f.send(t, map[string]any{"id": "c", "type": "continue"})
	if m := f.next(t); m["ok"] != true {
		t.Fatalf("continue response = %v", m)
	}
	f.collect(t, func(m map[string]any) bool { return isEvent(m, "agent_end") })
	if adapter.CallCount() != 1 {
		t.Fatalf("LLM calls = %d, want 1", adapter.CallCount())
	}
	f.closeStdin(t)
}

// B2/B3: get_state.running reflects a goal pursuit (the driver holds the run
// slot between turns too), prompt is rejected for the pursuit's whole
// lifetime, and state-changing commands fail fast while it is in flight.
func TestGetStateRunningDuringGoalPursuit(t *testing.T) {
	// One turn script, repeated forever once exhausted: the pursuit never
	// settles on its own and keeps the driver alive until goal_cancel.
	turn := llmtest.Turn(llmtest.Start(), llmtest.Text("working"), llmtest.Done(textMsg("working"), nil))
	client := llmtest.Client(turn)
	f := newRealFixture(t, client, nil)

	f.send(t, map[string]any{"id": "g", "type": "goal_start", "objective": "keep going"})
	if m := f.nextOfType(t, "response"); m["id"] != "g" || m["ok"] != true {
		t.Fatalf("goal_start response = %v", m)
	}

	f.send(t, map[string]any{"id": "st", "type": "get_state"})
	if m := f.nextOfType(t, "response"); m["data"].(map[string]any)["running"] != true {
		t.Fatalf("running during pursuit = %v", m["data"])
	}

	f.send(t, map[string]any{"id": "p", "type": "prompt", "text": "hi"})
	if m := f.nextOfType(t, "response"); m["ok"] != false {
		t.Fatalf("prompt during pursuit = %v", m)
	}

	f.send(t, map[string]any{"id": "sm", "type": "set_mode", "mode": "plan"})
	if m := f.nextOfType(t, "response"); m["ok"] != false || m["error"] != "agent is running" {
		t.Fatalf("set_mode during pursuit = %v", m)
	}

	// Cancel: the driver exits at the next run boundary and running clears.
	f.send(t, map[string]any{"id": "gc", "type": "goal_cancel"})
	if m := f.nextOfType(t, "response"); m["ok"] != true {
		t.Fatalf("goal_cancel response = %v", m)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		f.send(t, map[string]any{"id": "poll", "type": "get_state"})
		m := f.nextOfType(t, "response")
		if m["data"].(map[string]any)["running"] == false {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("running never cleared after goal_cancel")
		}
	}
	f.closeStdin(t)
}
