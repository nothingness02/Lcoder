package rpcserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lcoder/lcoder/pkg/agentapi"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/host"
	"github.com/lcoder/lcoder/pkg/models"
)

var (
	// errAgentBusy is the stable wire text for "a run is in flight" — used by
	// the fast-fail paths and mapped from host.ErrAgentBusy, so clients can
	// match on it regardless of which layer rejected the command.
	errAgentBusy = errors.New("agent is running")
	// errCoreClosed is the stable wire text mapped from host.ErrCoreClosed.
	errCoreClosed = errors.New("core is closed")
	// errResponseSent marks a dispatch result whose response was already
	// written (startRun replies before launching the run goroutine).
	errResponseSent = errors.New("rpcserver: response already sent")
)

// busyGuarded lists the state-changing commands that fail fast with
// errAgentBusy while any run (ad-hoc or goal pursuit) is in flight. The host
// enforces the same rule (host.ErrAgentBusy); this is the protocol-level
// fast path so the rejection does not depend on the host bottoming out.
var busyGuarded = map[string]bool{
	"set_mode":           true,
	"open_session":       true,
	"new_session":        true,
	"truncate_after":     true,
	"restore_checkpoint": true,
	"goal_start":         true,
	"goal_resume":        true,
}

// wireError maps the host's sentinel errors onto stable protocol texts.
// Everything else passes through unchanged.
func wireError(err error) error {
	switch {
	case errors.Is(err, host.ErrAgentBusy):
		return errAgentBusy
	case errors.Is(err, host.ErrCoreClosed):
		return errCoreClosed
	default:
		return err
	}
}

// Options carries the startup facts the server cannot derive from CoreAPI
// (the initial model, its capabilities) and the model-switch budget resolver.
type Options struct {
	// Model is the model the agent starts with; set_model updates the track.
	Model models.ModelRef
	// Capabilities is the model's declared capability list (may be nil).
	Capabilities []string
	// ResolveBudget computes the context TokenBudget for a set_model target,
	// mirroring the TUI provider panel's derivation (catalog window/maxOutput
	// + config.ResolveContextBudget + Context.StaticRatio). When nil,
	// set_model switches with a zero budget — acceptable only in tests.
	ResolveBudget func(ctx context.Context, ref models.ModelRef) (agentapi.TokenBudget, error)
}

// Server is one JSONL RPC endpoint bound to a CoreAPI and its event bus.
// A single client is supported per process (the protocol carries no
// session routing fields — the documented v1 limitation).
type Server struct {
	core agentapi.CoreAPI
	bus  *events.Bus
	opts Options

	// writeMu serializes every stdout write: the bus event handler, the
	// command-dispatch loop, and approval requests all funnel through it.
	writeMu sync.Mutex
	out     io.Writer

	// running is true while an agent run started by prompt/continue is in
	// flight; a second run command is rejected with "agent is running". Only
	// the run goroutine itself clears it (in its defer, after run returns) —
	// clearing it from the bus handler raced a client that re-prompts on
	// agent_end: the old run's defer would then erase the new run's flag.
	// The host enforces its own single-flight; this CAS is the fast-fail path.
	running atomic.Bool
	wg      sync.WaitGroup // counts background run goroutines

	// runGen numbers accepted runs; startedGen records the newest generation
	// that emitted agent_start on the bus. A run whose Core.Prompt fails
	// before the agent loop starts (e.g. the session append) leaves no
	// agent_start, so the run goroutine closes it with a synthetic agent_end.
	// agent_start of generation N is always delivered (synchronously, inside
	// run) before generation N+1 can be accepted, so the plain stores are
	// race-free.
	runGen     atomic.Int64
	startedGen atomic.Int64

	// model tracks the current model ref (SwitchModel has no getter on
	// CoreAPI), starting from opts.Model and updated by set_model.
	modelMu sync.Mutex
	model   models.ModelRef

	approval *approvalBridge
}

// New builds a server on core and installs its approval bridge as the core's
// UserConfirmation. Callers that own a subagent host should share the bridge
// via Confirmation() so subagent tool calls are approved over the same
// channel.
func New(core agentapi.CoreAPI, bus *events.Bus, opts Options) *Server {
	s := &Server{core: core, bus: bus, opts: opts, model: opts.Model}
	s.approval = newApprovalBridge(s)
	core.SetUserConfirm(s.approval)
	return s
}

// Confirmation returns the approval bridge installed on the core, for wiring
// additional hosts (e.g. the subagent host) to the same client dialog.
func (s *Server) Confirmation() agentapi.UserConfirmation { return s.approval }

// Serve runs the read/dispatch loop until stdin closes (EOF: graceful stop,
// nil error) or ctx is cancelled (signal path; the caller writes the crash
// checkpoint). A scanner error (e.g. an over-long line) is returned.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	s.writeMu.Lock()
	s.out = out
	s.writeMu.Unlock()

	// Synchronous subscription: events must not be dropped (SubscribeAsync
	// drops the oldest under load) and must stay ordered against the run.
	unsub := s.bus.Subscribe(func(_ context.Context, ev events.Event) error {
		data, err := events.MarshalJSON(ev)
		if err != nil {
			return nil // an unserializable event is not worth killing the run
		}
		// Record the run generation that produced agent_start (see runGen);
		// the busy flag is NOT cleared here — only the run goroutine may do
		// that, or a client re-prompting on agent_end could lose its flag to
		// the old run's deferred clear.
		if _, ok := ev.(events.AgentStartEvent); ok {
			s.startedGen.Store(s.runGen.Load())
		}
		return s.write(eventEnvelope{Type: "event", Event: json.RawMessage(data)})
	})
	defer unsub()

	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), maxLineSize)
	type scanResult struct {
		line []byte
		ok   bool
	}
	lines := make(chan scanResult)
	go func() {
		defer close(lines)
		for scanner.Scan() {
			// Scanner reuses its buffer; copy before handing to the loop.
			line := append([]byte(nil), scanner.Bytes()...)
			select {
			case lines <- scanResult{line: line, ok: true}:
			case <-ctx.Done():
				return
			}
		}
	}()

loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case res, open := <-lines:
			if !open {
				break loop // stdin EOF: graceful stop
			}
			s.handleLine(ctx, res.line)
		}
	}

	s.shutdown()
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("rpcserver: read stdin: %w", err)
	}
	return nil
}

// shutdown stops the in-flight run, releases every pending approval, and
// waits briefly for run goroutines so the session mirror can persist the
// final turn before the process exits.
func (s *Server) shutdown() {
	s.core.Abort()
	s.approval.close()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}
}

// write serializes one envelope as a single stdout line.
func (s *Server) write(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.out == nil {
		return errors.New("rpcserver: output not attached")
	}
	_, err = s.out.Write(data)
	return err
}

// reply writes the response for a command, honoring the fire-and-forget rule:
// no id, no response.
func (s *Server) reply(head commandHead, data any, err error) {
	if head.ID == "" {
		return
	}
	resp := response{Type: "response", ID: head.ID, Data: data}
	if err != nil {
		resp.OK = false
		resp.Error = err.Error()
	} else {
		resp.OK = true
	}
	_ = s.write(resp)
}

// protocolError always answers, even for id-less commands: framing and
// dispatch failures are client bugs that must be visible during development.
func (s *Server) protocolError(head commandHead, format string, args ...any) {
	_ = s.write(response{Type: "response", ID: head.ID, OK: false, Error: fmt.Sprintf(format, args...)})
}

// emitRunError surfaces a run failure after acceptance (the prompt response
// already went out) as a normal error event on the wire.
func (s *Server) emitRunError(err error) {
	data, merr := events.MarshalJSON(events.ErrorEvent{
		Base:    events.Base{Type: events.Error},
		Message: err.Error(),
	})
	if merr != nil {
		return
	}
	_ = s.write(eventEnvelope{Type: "event", Event: json.RawMessage(data)})
}

// emitSyntheticAgentEnd closes an accepted run that failed before its
// agent_start (e.g. the session append inside Core.Prompt). Hack: the host
// reports that failure only as Prompt's return value, so without this the
// client would see an orphan error event and a run with no end.
func (s *Server) emitSyntheticAgentEnd() {
	data, merr := events.MarshalJSON(events.AgentEndEvent{
		Base:   events.Base{Type: events.AgentEnd},
		Reason: events.EndReasonError,
	})
	if merr != nil {
		return
	}
	_ = s.write(eventEnvelope{Type: "event", Event: json.RawMessage(data)})
}

// coreRunning reports the authoritative busy state: the host's Running()
// (which also covers a goal pursuit between turns) when the core provides
// it, else the server's own run flag (fake cores in tests).
func (s *Server) coreRunning() bool {
	if r, ok := s.core.(interface{ Running() bool }); ok {
		return r.Running()
	}
	return s.running.Load()
}

func (s *Server) handleLine(ctx context.Context, line []byte) {
	var head commandHead
	if err := json.Unmarshal(line, &head); err != nil {
		s.protocolError(commandHead{}, "invalid JSON: %v", err)
		return
	}
	if head.Type == "" {
		s.protocolError(head, "missing command type")
		return
	}
	if head.Type == "approval_response" {
		s.handleApprovalResponse(line)
		return
	}
	data, err := s.dispatch(ctx, head, line)
	if errors.Is(err, errResponseSent) {
		return // startRun already wrote the accept response
	}
	s.reply(head, data, wireError(err))
}

// dispatch executes one command and returns its response data. Commands
// without return values yield nil data.
func (s *Server) dispatch(ctx context.Context, head commandHead, line []byte) (any, error) {
	// State-changing commands fail fast while busy; the host would reject
	// them too, but the fast path keeps the wire error stable and cheap.
	if busyGuarded[head.Type] && s.coreRunning() {
		return nil, errAgentBusy
	}
	switch head.Type {
	case "prompt":
		var p textCommand
		if err := json.Unmarshal(line, &p); err != nil {
			return nil, err
		}
		if p.Text == "" {
			return nil, errors.New("prompt requires text")
		}
		return nil, s.startRun(ctx, head, func(runCtx context.Context) error {
			return s.core.Prompt(runCtx, models.UserMessage(p.Text))
		})

	case "continue":
		return nil, s.startRun(ctx, head, s.core.Continue)

	case "steer":
		var p textCommand
		if err := json.Unmarshal(line, &p); err != nil {
			return nil, err
		}
		if p.Text == "" {
			return nil, errors.New("steer requires text")
		}
		s.core.Steer(models.UserMessage(p.Text))
		return nil, nil

	case "abort":
		s.core.Abort()
		return nil, nil

	case "set_mode":
		var p setModeCommand
		if err := json.Unmarshal(line, &p); err != nil {
			return nil, err
		}
		if p.Mode == "" {
			return nil, errors.New("set_mode requires mode")
		}
		return nil, s.core.SetMode(p.Mode)

	case "set_model":
		var p setModelCommand
		if err := json.Unmarshal(line, &p); err != nil {
			return nil, err
		}
		if p.ModelID == "" {
			return nil, errors.New("set_model requires model_id")
		}
		ref := models.ModelRef{Provider: p.Provider, ID: p.ModelID}
		var budget contextmgr.TokenBudget
		if s.opts.ResolveBudget != nil {
			b, err := s.opts.ResolveBudget(ctx, ref)
			if err != nil {
				return nil, fmt.Errorf("resolve budget: %w", err)
			}
			budget = b
		}
		s.core.SwitchModel(ref, budget)
		s.modelMu.Lock()
		s.model = ref
		s.modelMu.Unlock()
		return map[string]any{"model": ref}, nil

	case "set_thinking":
		var p setThinkingCommand
		if err := json.Unmarshal(line, &p); err != nil {
			return nil, err
		}
		s.core.SwitchThinking(p.Value)
		return nil, nil

	case "clear_skill_filter":
		s.core.ClearSkillFilter()
		return nil, nil

	case "new_session":
		if err := s.core.NewSession(); err != nil {
			return nil, err
		}
		return map[string]any{"session_id": s.core.SessionID()}, nil

	case "open_session":
		var p openSessionCommand
		if err := json.Unmarshal(line, &p); err != nil {
			return nil, err
		}
		if p.SessionID == "" {
			return nil, errors.New("open_session requires session_id")
		}
		if err := s.core.OpenSession(p.SessionID); err != nil {
			return nil, err
		}
		return map[string]any{"session_id": s.core.SessionID()}, nil

	case "list_sessions":
		infos, err := s.core.ListSessions()
		if err != nil {
			return nil, err
		}
		return map[string]any{"sessions": infos}, nil

	case "rename_session":
		var p renameSessionCommand
		if err := json.Unmarshal(line, &p); err != nil {
			return nil, err
		}
		if p.SessionID == "" {
			return nil, errors.New("rename_session requires session_id")
		}
		return nil, s.core.RenameSession(p.SessionID, p.Title)

	case "truncate_after":
		var p truncateAfterCommand
		if err := json.Unmarshal(line, &p); err != nil {
			return nil, err
		}
		return nil, s.core.TruncateAfter(p.MessageID)

	case "goal_start":
		var p goalStartCommand
		if err := json.Unmarshal(line, &p); err != nil {
			return nil, err
		}
		if p.Objective == "" {
			return nil, errors.New("goal_start requires objective")
		}
		s.core.StartGoal(p.Objective, p.TurnBudget, p.TokenBudget)
		return nil, nil

	case "goal_pause":
		var p goalPauseCommand
		if err := json.Unmarshal(line, &p); err != nil {
			return nil, err
		}
		s.core.PauseGoal(p.Reason)
		return nil, nil

	case "goal_resume":
		s.core.ResumeGoal()
		return nil, nil

	case "goal_cancel":
		s.core.CancelGoal()
		return nil, nil

	case "save_checkpoint":
		id, err := s.core.SaveCheckpoint()
		if err != nil {
			return nil, err
		}
		return map[string]any{"checkpoint_id": id}, nil

	case "restore_checkpoint":
		var p restoreCheckpointCommand
		if err := json.Unmarshal(line, &p); err != nil {
			return nil, err
		}
		if p.CheckpointID == "" {
			return nil, errors.New("restore_checkpoint requires checkpoint_id")
		}
		return nil, s.core.RestoreCheckpoint(p.CheckpointID)

	case "list_checkpoints":
		infos, err := s.core.ListCheckpoints()
		if err != nil {
			return nil, err
		}
		return map[string]any{"checkpoints": infos}, nil

	case "get_state":
		return s.snapshot(), nil

	default:
		return nil, fmt.Errorf("unknown command type %q", head.Type)
	}
}

// startRun guards the single-flight rule and launches the run in the
// background: the command response means "accepted", completion arrives as
// the agent_end event. On acceptance it writes the ok response ITSELF,
// before the run goroutine starts, so the response always precedes every
// event of the run; the caller must not reply again (errResponseSent).
func (s *Server) startRun(ctx context.Context, head commandHead, run func(context.Context) error) error {
	if g := s.core.Goal(); g != nil && g.Status == agentapi.GoalActive {
		return errors.New("a goal pursuit is active; use steer, or goal_pause/goal_cancel first")
	}
	if s.coreRunning() {
		return errAgentBusy
	}
	if !s.running.CompareAndSwap(false, true) {
		return errAgentBusy
	}
	gen := s.runGen.Add(1)
	s.reply(head, nil, nil)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.running.Store(false)
		if err := run(ctx); err != nil && ctx.Err() == nil {
			s.emitRunError(err)
			if s.startedGen.Load() < gen {
				// The run failed before agent_start (e.g. the session
				// append inside Core.Prompt): close it on the wire.
				s.emitSyntheticAgentEnd()
			}
		}
	}()
	return errResponseSent
}

// snapshot builds the get_state response from CoreAPI queries.
func (s *Server) snapshot() stateSnapshot {
	tasks := s.core.Tasks()
	wire := make([]taskWire, 0, len(tasks))
	for _, t := range tasks {
		wire = append(wire, taskWire{Text: t.Text, Status: string(t.Status)})
	}
	msgs := s.core.AllMessages()
	if msgs == nil {
		msgs = []models.AgentMessage{}
	}
	s.modelMu.Lock()
	model := s.model
	s.modelMu.Unlock()
	return stateSnapshot{
		SessionID:    s.core.SessionID(),
		Mode:         s.core.Mode(),
		Thinking:     s.core.Thinking(),
		Model:        model,
		Running:      s.coreRunning(),
		Goal:         wireGoal(s.core.Goal()),
		Tasks:        wire,
		ContextStats: s.core.ContextStats(),
		Capabilities: s.opts.Capabilities,
		Messages:     msgs,
	}
}

// handleApprovalResponse resolves a pending approval; it never produces a
// response envelope (the id belongs to the server-issued approval_request,
// so a response would corrupt the client's correlation table). Unknown ids
// are dropped — the request may already have been cancelled by abort/EOF.
func (s *Server) handleApprovalResponse(line []byte) {
	var cmd approvalResponseCommand
	if err := json.Unmarshal(line, &cmd); err != nil {
		s.protocolError(commandHead{}, "invalid approval_response: %v", err)
		return
	}
	s.approval.resolve(cmd.ID, confirmScopeFromWire(cmd.Result.Scope))
}
