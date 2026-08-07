package rpcserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lcoder/lcoder/pkg/agentapi"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/host"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/task"
	"github.com/lcoder/lcoder/pkg/testutil"
)

// rpcFixture runs a Server over in-memory pipes and offers line-oriented
// send/receive helpers.
type rpcFixture struct {
	srv    *Server
	cmdIn  *io.PipeWriter
	lines  chan string
	errCh  chan error
	cancel context.CancelFunc
}

func newFixture(t *testing.T, core agentapi.CoreAPI, bus *events.Bus, opts Options) *rpcFixture {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	srv := New(core, bus, opts)
	f := &rpcFixture{srv: srv, cmdIn: inW, lines: make(chan string, 512), errCh: make(chan error, 1), cancel: cancel}
	go func() { f.errCh <- srv.Serve(ctx, inR, outW) }()
	// Serve attaches the output writer at the top of its goroutine; wait for
	// it so events/approvals emitted immediately after New are not dropped.
	for range 1000 {
		srv.writeMu.Lock()
		attached := srv.out != nil
		srv.writeMu.Unlock()
		if attached {
			break
		}
		time.Sleep(time.Millisecond)
	}
	go func() {
		r := bufio.NewReader(outR)
		for {
			line, err := r.ReadString('\n')
			if line != "" {
				f.lines <- line
			}
			if err != nil {
				close(f.lines)
				return
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		_ = inW.Close()
	})
	return f
}

// send marshals one command and writes it as a single line.
func (f *rpcFixture) send(t *testing.T, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	f.sendRaw(t, string(data))
}

func (f *rpcFixture) sendRaw(t *testing.T, line string) {
	t.Helper()
	if _, err := io.WriteString(f.cmdIn, line+"\n"); err != nil {
		t.Fatalf("write command: %v", err)
	}
}

// next reads the next output line and decodes it.
func (f *rpcFixture) next(t *testing.T) map[string]any {
	t.Helper()
	select {
	case line, ok := <-f.lines:
		if !ok {
			t.Fatal("output closed before a line was available")
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("output line is not JSON: %q: %v", line, err)
		}
		return m
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for an output line")
		return nil
	}
}

// nextOfType skips lines until one carries the wanted top-level type.
func (f *rpcFixture) nextOfType(t *testing.T, typ string) map[string]any {
	t.Helper()
	for {
		m := f.next(t)
		if m["type"] == typ {
			return m
		}
	}
}

// closeStdin signals EOF and waits for Serve to return.
func (f *rpcFixture) closeStdin(t *testing.T) {
	t.Helper()
	_ = f.cmdIn.Close()
	select {
	case err := <-f.errCh:
		if err != nil {
			t.Fatalf("Serve returned error on EOF: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after stdin EOF")
	}
}

func fakeCore() *testutil.FakeAgent {
	return &testutil.FakeAgent{SessionIDVal: "sess-1"}
}

func TestFramingMultipleCommandsPerWrite(t *testing.T) {
	f := newFixture(t, fakeCore(), events.New(), Options{})
	// Two commands in a single write must be split on '\n'.
	f.sendRaw(t, `{"id":"a","type":"abort"}`+"\n"+`{"id":"b","type":"clear_skill_filter"}`)
	for _, id := range []string{"a", "b"} {
		m := f.next(t)
		if m["type"] != "response" || m["id"] != id || m["ok"] != true {
			t.Fatalf("unexpected line: %v", m)
		}
	}
	f.closeStdin(t)
}

func TestFramingMalformedJSON(t *testing.T) {
	f := newFixture(t, fakeCore(), events.New(), Options{})
	f.sendRaw(t, `{not json`)
	m := f.next(t)
	if m["type"] != "response" || m["ok"] != false {
		t.Fatalf("malformed JSON must produce an error response, got %v", m)
	}
	if _, hasID := m["id"]; hasID {
		t.Fatalf("a malformed line has no correlation id: %v", m)
	}
	if !strings.Contains(m["error"].(string), "invalid JSON") {
		t.Fatalf("error text = %v", m["error"])
	}
	f.closeStdin(t)
}

func TestUnknownCommandType(t *testing.T) {
	f := newFixture(t, fakeCore(), events.New(), Options{})
	f.send(t, map[string]any{"id": "u1", "type": "bogus"})
	m := f.next(t)
	if m["ok"] != false || m["id"] != "u1" {
		t.Fatalf("unknown type response = %v", m)
	}
	if !strings.Contains(m["error"].(string), "unknown command type") {
		t.Fatalf("error text = %v", m["error"])
	}
	f.closeStdin(t)
}

// A valid command without an id is executed but produces no response: the
// next line on the wire must belong to the following (id-carrying) command.
func TestFireAndForgetProducesNoResponse(t *testing.T) {
	core := fakeCore()
	f := newFixture(t, core, events.New(), Options{})
	f.send(t, map[string]any{"type": "set_thinking", "value": "high"})
	f.send(t, map[string]any{"id": "after", "type": "get_state"})
	m := f.next(t)
	if m["id"] != "after" {
		t.Fatalf("the id-less command must not emit a response, got %v", m)
	}
	if core.SwitchedThinking != "high" {
		t.Fatalf("the id-less command was not executed: SwitchedThinking = %q", core.SwitchedThinking)
	}
	f.closeStdin(t)
}

func TestCommandDispatch(t *testing.T) {
	core := fakeCore()
	core.SessionMsgs = map[string][]models.AgentMessage{"s2": {
		{ID: "m1", Role: models.RoleUser, Content: []models.ContentPart{models.TextContent{Text: "old"}}},
	}}
	core.SessionsList = []agentapi.SessionInfo{{ID: "s2", Title: "t"}}
	core.CheckpointIDs = []string{"cp-1"}
	core.ModeName = "code"
	f := newFixture(t, core, events.New(), Options{})

	cases := []struct {
		id  string
		cmd map[string]any
	}{
		{"c1", map[string]any{"type": "set_mode", "mode": "plan"}},
		{"c2", map[string]any{"type": "set_model", "provider": "openai", "model_id": "gpt-5"}},
		{"c3", map[string]any{"type": "steer", "text": "hold on"}},
		{"c4", map[string]any{"type": "abort"}},
		{"c5", map[string]any{"type": "open_session", "session_id": "s2"}},
		{"c6", map[string]any{"type": "truncate_after", "message_id": "m1"}},
		{"c7", map[string]any{"type": "new_session"}},
		{"c8", map[string]any{"type": "rename_session", "session_id": "s2", "title": "renamed"}},
		{"c9", map[string]any{"type": "goal_start", "objective": "fix it", "turn_budget": 3, "token_budget": 1000}},
		{"c10", map[string]any{"type": "goal_pause", "reason": "pause"}},
		{"c11", map[string]any{"type": "goal_resume"}},
		{"c12", map[string]any{"type": "goal_cancel"}},
		{"c13", map[string]any{"type": "clear_skill_filter"}},
	}
	for _, c := range cases {
		c.cmd["id"] = c.id
		f.send(t, c.cmd)
		m := f.nextOfType(t, "response")
		if m["id"] != c.id || m["ok"] != true {
			t.Fatalf("%s: response = %v", c.id, m)
		}
	}

	if core.ModeName != "plan" {
		t.Fatalf("SetMode not applied: %q", core.ModeName)
	}
	if core.SwitchedModel.ID != "gpt-5" || core.SwitchedModel.Provider != "openai" {
		t.Fatalf("SwitchModel = %+v", core.SwitchedModel)
	}
	if core.SessionIDVal == "sess-1" || core.NewSessionCount != 1 {
		t.Fatalf("session switch/new not applied: id=%q new=%d", core.SessionIDVal, core.NewSessionCount)
	}
	if core.RenamedSessions["s2"] != "renamed" {
		t.Fatalf("RenameSession = %v", core.RenamedSessions)
	}
	if len(core.TruncateAfterCalls) != 1 || core.TruncateAfterCalls[0] != "m1" {
		t.Fatalf("TruncateAfterCalls = %v", core.TruncateAfterCalls)
	}
	if g := core.Goal(); g != nil {
		t.Fatalf("goal should be cancelled, got %+v", g)
	}

	// Responses carrying data.
	f.send(t, map[string]any{"id": "d1", "type": "list_sessions"})
	if m := f.next(t); m["ok"] != true || m["data"].(map[string]any)["sessions"] == nil {
		t.Fatalf("list_sessions response = %v", m)
	}
	f.send(t, map[string]any{"id": "d2", "type": "save_checkpoint"})
	m := f.next(t)
	if m["data"].(map[string]any)["checkpoint_id"] != core.SessionIDVal {
		t.Fatalf("save_checkpoint data = %v", m["data"])
	}
	f.send(t, map[string]any{"id": "d3", "type": "restore_checkpoint", "checkpoint_id": "cp-1"})
	if m := f.next(t); m["ok"] != true {
		t.Fatalf("restore_checkpoint response = %v", m)
	}
	if core.RestoredCheckpoint != "cp-1" {
		t.Fatalf("RestoredCheckpoint = %q", core.RestoredCheckpoint)
	}
	f.send(t, map[string]any{"id": "d4", "type": "list_checkpoints"})
	if m := f.next(t); m["ok"] != true {
		t.Fatalf("list_checkpoints response = %v", m)
	}

	// Error propagation: unknown session id.
	f.send(t, map[string]any{"id": "e1", "type": "open_session", "session_id": "nope"})
	if m := f.next(t); m["ok"] != false || !strings.Contains(m["error"].(string), "nope") {
		t.Fatalf("open_session error response = %v", m)
	}
	f.closeStdin(t)
}

func TestGetStateSnapshot(t *testing.T) {
	core := fakeCore()
	core.ThinkingVal = "on"
	core.ContextStatsVal = agentapi.ContextStats{Total: 42, BudgetMax: 128000}
	core.Messages = []models.AgentMessage{models.UserMessage("hi")}
	core.TasksVal = []task.Task{{Text: "do x", Status: task.StatusPending}}
	core.GoalVal = &agentapi.GoalState{Objective: "obj", Status: agentapi.GoalPaused, TurnBudget: 5, TurnsUsed: 2}
	core.UsageSummaryVal = agentapi.UsageSummary{Turns: 2, TotalCost: 0.0042, PromptTokens: 1530, CompletionTokens: 220}
	f := newFixture(t, core, events.New(), Options{
		Model:        models.ModelRef{Provider: "openai", ID: "gpt-4o"},
		Capabilities: []string{"tools"},
	})

	f.send(t, map[string]any{"id": "s", "type": "get_state"})
	m := f.next(t)
	if m["ok"] != true {
		t.Fatalf("get_state response = %v", m)
	}
	data := m["data"].(map[string]any)
	if data["session_id"] != "sess-1" || data["mode"] != "code" || data["thinking"] != "on" {
		t.Fatalf("snapshot identity fields = %v", data)
	}
	model := data["model"].(map[string]any)
	if model["provider"] != "openai" || model["id"] != "gpt-4o" {
		t.Fatalf("snapshot model = %v", model)
	}
	if data["running"] != false {
		t.Fatalf("snapshot running = %v", data["running"])
	}
	goal := data["goal"].(map[string]any)
	if goal["objective"] != "obj" || goal["status"] != "paused" || goal["turn_budget"].(float64) != 5 {
		t.Fatalf("snapshot goal = %v", goal)
	}
	tasks := data["tasks"].([]any)
	if len(tasks) != 1 || tasks[0].(map[string]any)["text"] != "do x" {
		t.Fatalf("snapshot tasks = %v", tasks)
	}
	if data["context_stats"].(map[string]any)["total"].(float64) != 42 {
		t.Fatalf("snapshot context_stats = %v", data["context_stats"])
	}
	usage := data["usage_summary"].(map[string]any)
	if usage["turns"].(float64) != 2 || usage["total_cost"].(float64) != 0.0042 ||
		usage["prompt_tokens"].(float64) != 1530 || usage["completion_tokens"].(float64) != 220 {
		t.Fatalf("snapshot usage_summary = %v", usage)
	}
	msgs := data["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("snapshot messages = %v", msgs)
	}
	if caps := data["capabilities"].([]any); len(caps) != 1 || caps[0] != "tools" {
		t.Fatalf("snapshot capabilities = %v", caps)
	}
	f.closeStdin(t)
}

// blockingCore blocks inside Prompt until released, so the single-flight
// rule can be exercised deterministically.
type blockingCore struct {
	testutil.FakeAgent
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingCore) Prompt(ctx context.Context, msg models.AgentMessage) error {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestPromptBusyRejectsSecondRun(t *testing.T) {
	core := &blockingCore{entered: make(chan struct{}), release: make(chan struct{})}
	f := newFixture(t, core, events.New(), Options{})

	f.send(t, map[string]any{"id": "p1", "type": "prompt", "text": "go"})
	if m := f.next(t); m["ok"] != true || m["id"] != "p1" {
		t.Fatalf("prompt accept response = %v", m)
	}
	select {
	case <-core.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("run goroutine never entered Prompt")
	}

	f.send(t, map[string]any{"id": "p2", "type": "prompt", "text": "again"})
	m := f.next(t)
	if m["ok"] != false || !strings.Contains(m["error"].(string), "agent is running") {
		t.Fatalf("second prompt must be rejected as busy, got %v", m)
	}

	// steer stays available while running.
	f.send(t, map[string]any{"id": "s1", "type": "steer", "text": "hint"})
	if m := f.next(t); m["ok"] != true {
		t.Fatalf("steer while running = %v", m)
	}

	// get_state reports running=true mid-run.
	f.send(t, map[string]any{"id": "st", "type": "get_state"})
	if m := f.next(t); m["data"].(map[string]any)["running"] != true {
		t.Fatalf("running mid-run = %v", m["data"])
	}

	close(core.release)
	// Wait for the run goroutine to clear the busy flag before re-prompting.
	deadline := time.Now().Add(5 * time.Second)
	for f.srv.running.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if f.srv.running.Load() {
		t.Fatal("busy flag was not cleared after the run finished")
	}
	f.send(t, map[string]any{"id": "p3", "type": "prompt", "text": "after"})
	if m := f.nextOfType(t, "response"); m["ok"] != true {
		t.Fatalf("prompt after the run finished = %v", m)
	}
	f.closeStdin(t)
}

// An active goal pursuit rejects prompt/continue (a mid-pursuit Prompt would
// race the goal driver's own run).
func TestPromptRejectedDuringActiveGoal(t *testing.T) {
	core := fakeCore()
	core.StartGoal("pursue", 0, 0)
	f := newFixture(t, core, events.New(), Options{})
	f.send(t, map[string]any{"id": "p", "type": "prompt", "text": "hi"})
	m := f.next(t)
	if m["ok"] != false || !strings.Contains(m["error"].(string), "goal") {
		t.Fatalf("prompt during goal = %v", m)
	}
	f.closeStdin(t)
}

// Approval bridge unit path: ConfirmWithScope emits an approval_request and
// unblocks on the client's approval_response.
func TestApprovalRoundTrip(t *testing.T) {
	f := newFixture(t, fakeCore(), events.New(), Options{})

	info := agentapi.ToolCallInfo{
		ToolCall: models.ToolCallContent{Type: "tool_call", ID: "tc1", Name: "bash"},
		Args:     map[string]any{"command": "rm -rf /tmp/x"},
	}
	type result struct {
		res agentapi.ConfirmResult
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		res, err := f.srv.Confirmation().ConfirmWithScope(context.Background(), info)
		resCh <- result{res, err}
	}()

	req := f.nextOfType(t, "approval_request")
	if req["id"] != "srv-1" {
		t.Fatalf("approval request id = %v", req["id"])
	}
	payload := req["request"].(map[string]any)
	if payload["tool_call_id"] != "tc1" || payload["tool_name"] != "bash" || payload["command"] != "rm -rf /tmp/x" {
		t.Fatalf("approval payload = %v", payload)
	}

	f.send(t, map[string]any{"type": "approval_response", "id": "srv-1", "result": map[string]any{"scope": "session"}})
	select {
	case r := <-resCh:
		if r.err != nil || !r.res.Allow || r.res.Scope != agentapi.ScopeSession {
			t.Fatalf("confirm result = %+v err=%v", r.res, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ConfirmWithScope did not return after approval_response")
	}

	// Deny path, and a malformed scope must deny too.
	go func() {
		res, err := f.srv.Confirmation().ConfirmWithScope(context.Background(), info)
		resCh <- result{res, err}
	}()
	req = f.nextOfType(t, "approval_request")
	f.send(t, map[string]any{"type": "approval_response", "id": req["id"], "result": map[string]any{"scope": "garbage"}})
	select {
	case r := <-resCh:
		if r.err != nil || r.res.Allow || r.res.Scope != agentapi.ScopeDeny {
			t.Fatalf("malformed scope must deny, got %+v err=%v", r.res, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second ConfirmWithScope did not return")
	}
	f.closeStdin(t)
}

// Cancelling the run ctx (abort) releases a pending confirmation with
// cancellation semantics.
func TestApprovalReleasedOnContextCancel(t *testing.T) {
	f := newFixture(t, fakeCore(), events.New(), Options{})
	ctx, cancel := context.WithCancel(context.Background())
	resCh := make(chan error, 1)
	go func() {
		_, err := f.srv.Confirmation().ConfirmWithScope(ctx, agentapi.ToolCallInfo{
			ToolCall: models.ToolCallContent{ID: "tc9", Name: "bash"},
		})
		resCh <- err
	}()
	f.nextOfType(t, "approval_request")
	cancel()
	select {
	case err := <-resCh:
		if err == nil {
			t.Fatal("cancelled confirm must return an error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("confirm was not released by ctx cancel")
	}
	f.closeStdin(t)
}

// stdin EOF while an approval is pending releases it with a disconnect error.
func TestApprovalReleasedOnClientDisconnect(t *testing.T) {
	f := newFixture(t, fakeCore(), events.New(), Options{})
	resCh := make(chan error, 1)
	go func() {
		_, err := f.srv.Confirmation().ConfirmWithScope(context.Background(), agentapi.ToolCallInfo{
			ToolCall: models.ToolCallContent{ID: "tc10", Name: "bash"},
		})
		resCh <- err
	}()
	f.nextOfType(t, "approval_request")
	f.closeStdin(t)
	select {
	case err := <-resCh:
		if err == nil {
			t.Fatal("disconnected confirm must return an error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("confirm was not released by client disconnect")
	}
}

// Events published on the bus stream as event envelopes that round-trip
// through events.UnmarshalJSON.
func TestBusEventsStreamAsEnvelopes(t *testing.T) {
	bus := events.New()
	f := newFixture(t, fakeCore(), bus, Options{})
	if err := bus.Emit(context.Background(), events.AgentStartEvent{Base: events.Base{Type: events.AgentStart, Turn: 1}}); err != nil {
		t.Fatal(err)
	}
	m := f.nextOfType(t, "event")
	raw, err := json.Marshal(m["event"])
	if err != nil {
		t.Fatal(err)
	}
	ev, err := events.UnmarshalJSON(raw)
	if err != nil {
		t.Fatalf("event envelope did not round-trip: %v", err)
	}
	if ev.EventType() != events.AgentStart {
		t.Fatalf("event type = %v", ev.EventType())
	}
	f.closeStdin(t)
}

// A ResolveBudget failure surfaces as a command error and leaves the model
// unchanged.
func TestSetModelBudgetError(t *testing.T) {
	core := fakeCore()
	f := newFixture(t, core, events.New(), Options{
		Model: models.ModelRef{Provider: "openai", ID: "gpt-4o"},
		ResolveBudget: func(context.Context, models.ModelRef) (agentapi.TokenBudget, error) {
			return contextmgr.TokenBudget{}, errTestBudget
		},
	})
	f.send(t, map[string]any{"id": "m", "type": "set_model", "provider": "openai", "model_id": "gpt-5"})
	m := f.next(t)
	if m["ok"] != false || !strings.Contains(m["error"].(string), "budget") {
		t.Fatalf("set_model budget failure = %v", m)
	}
	if core.SwitchedModel.ID != "" {
		t.Fatalf("SwitchModel must not run on budget failure: %+v", core.SwitchedModel)
	}
	f.closeStdin(t)
}

var errTestBudget = &testError{"no budget"}

type testError struct{ s string }

func (e *testError) Error() string { return e.s }

// ---------------------------------------------------------------------------
// Regression tests for the run-lifecycle review findings
// ---------------------------------------------------------------------------

// endMidRunCore emits agent_end on the bus while its run is still blocked,
// simulating a client that re-prompts the moment agent_end arrives.
type endMidRunCore struct {
	testutil.FakeAgent
	bus     *events.Bus
	release chan struct{}
	once    sync.Once
}

func (c *endMidRunCore) Prompt(ctx context.Context, _ models.AgentMessage) error {
	c.once.Do(func() {
		_ = c.bus.Emit(ctx, events.AgentEndEvent{
			Base:   events.Base{Type: events.AgentEnd},
			Reason: events.EndReasonCompleted,
		})
	})
	select {
	case <-c.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// B1: the bus handler must not clear the busy flag — a re-prompt racing the
// old run's unwind is rejected until the run goroutine actually returns.
func TestRePromptOnAgentEndRejectedWhileRunUnwinds(t *testing.T) {
	bus := events.New()
	core := &endMidRunCore{bus: bus, release: make(chan struct{})}
	core.SessionIDVal = "sess-1"
	f := newFixture(t, core, bus, Options{})

	f.send(t, map[string]any{"id": "p1", "type": "prompt", "text": "go"})
	if m := f.next(t); m["ok"] != true {
		t.Fatalf("p1 response = %v", m)
	}
	// The run emitted agent_end but is still blocked; the client reacts now.
	if m := f.nextOfType(t, "event"); !isEvent(m, "agent_end") {
		t.Fatalf("expected the mid-run agent_end, got %v", m)
	}
	f.send(t, map[string]any{"id": "p2", "type": "prompt", "text": "again"})
	if m := f.next(t); m["ok"] != false || m["error"] != "agent is running" {
		t.Fatalf("re-prompt while the old run unwinds = %v", m)
	}

	close(core.release)
	deadline := time.Now().Add(5 * time.Second)
	for f.srv.running.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	f.send(t, map[string]any{"id": "p3", "type": "prompt", "text": "after"})
	if m := f.nextOfType(t, "response"); m["ok"] != true {
		t.Fatalf("prompt after the run fully returned = %v", m)
	}
	f.closeStdin(t)
}

// startEmittingCore emits agent_start (then finishes) inside Prompt.
type startEmittingCore struct {
	testutil.FakeAgent
	bus *events.Bus
}

func (c *startEmittingCore) Prompt(ctx context.Context, _ models.AgentMessage) error {
	return c.bus.Emit(ctx, events.AgentStartEvent{Base: events.Base{Type: events.AgentStart, Turn: 1}})
}

// B4: the accept response is written before the run goroutine starts, so it
// always precedes every event of the run on the wire.
func TestPromptResponsePrecedesRunEvents(t *testing.T) {
	bus := events.New()
	core := &startEmittingCore{bus: bus}
	core.SessionIDVal = "sess-1"
	f := newFixture(t, core, bus, Options{})

	f.send(t, map[string]any{"id": "p", "type": "prompt", "text": "go"})
	first := f.next(t)
	if first["type"] != "response" || first["id"] != "p" || first["ok"] != true {
		t.Fatalf("first line must be the accept response, got %v", first)
	}
	second := f.next(t)
	if !isEvent(second, "agent_start") {
		t.Fatalf("second line must be the run's agent_start, got %v", second)
	}
	f.closeStdin(t)
}

// failPromptCore fails inside Prompt before any event (the session-append
// failure path of host.Core.Prompt).
type failPromptCore struct {
	testutil.FakeAgent
	err error
}

func (c *failPromptCore) Prompt(context.Context, models.AgentMessage) error { return c.err }

// B5: an accepted run that fails before agent_start is closed on the wire
// with an error event followed by a synthetic agent_end.
func TestPreStartFailureEmitsSyntheticAgentEnd(t *testing.T) {
	core := &failPromptCore{err: errors.New("session append: disk full")}
	core.SessionIDVal = "sess-1"
	f := newFixture(t, core, events.New(), Options{})

	f.send(t, map[string]any{"id": "p", "type": "prompt", "text": "go"})
	if m := f.next(t); m["type"] != "response" || m["ok"] != true {
		t.Fatalf("accept response = %v", m)
	}
	m := f.next(t)
	if !isEvent(m, "error") {
		t.Fatalf("expected the run error event, got %v", m)
	}
	if msg := m["event"].(map[string]any)["message"]; !strings.Contains(msg.(string), "disk full") {
		t.Fatalf("error event message = %v", msg)
	}
	m = f.next(t)
	if !isEvent(m, "agent_end") {
		t.Fatalf("expected the synthetic agent_end, got %v", m)
	}
	if reason := m["event"].(map[string]any)["reason"]; reason != "error" {
		t.Fatalf("synthetic agent_end reason = %v", reason)
	}
	f.closeStdin(t)
}

// runningCore exposes the host-style Running() method.
type runningCore struct {
	testutil.FakeAgent
	running atomic.Bool
}

func (r *runningCore) Running() bool { return r.running.Load() }

// B2+B3: get_state.running comes from the core's Running() (covering goal
// pursuits), and state-changing commands fail fast while it reports busy.
func TestStateCommandsRejectedWhileCoreRunning(t *testing.T) {
	core := &runningCore{}
	core.SessionIDVal = "sess-1"
	core.running.Store(true)
	f := newFixture(t, core, events.New(), Options{})

	f.send(t, map[string]any{"id": "st", "type": "get_state"})
	if m := f.next(t); m["data"].(map[string]any)["running"] != true {
		t.Fatalf("running from core = %v", m["data"])
	}

	cmds := []map[string]any{
		{"type": "set_mode", "mode": "plan"},
		{"type": "open_session", "session_id": "s2"},
		{"type": "new_session"},
		{"type": "truncate_after", "message_id": "m1"},
		{"type": "restore_checkpoint", "checkpoint_id": "cp"},
		{"type": "goal_start", "objective": "x"},
		{"type": "goal_resume"},
	}
	for i, cmd := range cmds {
		id := fmt.Sprintf("g%d", i)
		cmd["id"] = id
		f.send(t, cmd)
		m := f.next(t)
		if m["ok"] != false || m["error"] != "agent is running" {
			t.Fatalf("%v while running = %v", cmd["type"], m)
		}
	}
	if core.ModeName != "" || core.NewSessionCount != 0 || len(core.TruncateAfterCalls) != 0 ||
		core.RestoredCheckpoint != "" || core.GoalVal != nil {
		t.Fatalf("a guarded command reached the core: mode=%q new=%d trunc=%v restore=%q goal=%v",
			core.ModeName, core.NewSessionCount, core.TruncateAfterCalls, core.RestoredCheckpoint, core.GoalVal)
	}

	// prompt/continue reject through the same busy state.
	f.send(t, map[string]any{"id": "p", "type": "prompt", "text": "go"})
	if m := f.next(t); m["ok"] != false || m["error"] != "agent is running" {
		t.Fatalf("prompt while core running = %v", m)
	}

	core.running.Store(false)
	f.send(t, map[string]any{"id": "ok1", "type": "set_mode", "mode": "plan"})
	if m := f.next(t); m["ok"] != true {
		t.Fatalf("set_mode after the core went idle = %v", m)
	}
	f.closeStdin(t)
}

// B3: the host's sentinel errors map onto stable wire texts instead of
// leaking internal wording.
func TestHostSentinelErrorsMapToStableWireText(t *testing.T) {
	core := fakeCore()
	core.BusyErr = fmt.Errorf("host: %w", host.ErrAgentBusy)
	core.RestoreErr = fmt.Errorf("host: %w", host.ErrCoreClosed)
	f := newFixture(t, core, events.New(), Options{})

	f.send(t, map[string]any{"id": "e1", "type": "open_session", "session_id": "s2"})
	if m := f.next(t); m["ok"] != false || m["error"] != "agent is running" {
		t.Fatalf("busy mapping = %v", m)
	}
	// RestoreCheckpoint checks BusyErr first; clear it to reach RestoreErr.
	core.BusyErr = nil
	f.send(t, map[string]any{"id": "e2", "type": "restore_checkpoint", "checkpoint_id": "cp"})
	m := f.next(t)
	if m["ok"] != false || m["error"] != "core is closed" {
		t.Fatalf("closed mapping = %v", m)
	}
	if strings.Contains(m["error"].(string), "host:") {
		t.Fatalf("internal error wording leaked onto the wire: %v", m["error"])
	}
	f.closeStdin(t)
}
