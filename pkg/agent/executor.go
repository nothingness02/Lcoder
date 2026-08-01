package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/lcoder/lcoder/internal/paths"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/tools/builtin"
	"github.com/lcoder/lcoder/pkg/skills"
	"github.com/lcoder/lcoder/pkg/task"
	"github.com/lcoder/lcoder/pkg/tools"
)

const switchModeToolName = "switch_mode"

// swarmExclusivityMessage is the corrective feedback for a mixed batch.
const swarmExclusivityMessage = "subagent swarm mode (prompt_template + items) must be the ONLY tool call in your response. " +
	"This call was not executed. Call it alone, wait for its result, then continue with other tools."

func switchModeDefinition() models.ToolDefinition {
	return models.ToolDefinition{
		Name:        switchModeToolName,
		Description: "Switch the agent to a different mode for subsequent turns. Use this to move from planning to implementation or back.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"mode": map[string]any{
					"type":        "string",
					"description": "Target mode name, e.g. code or plan",
				},
			},
			"required": []string{"mode"},
		},
	}
}

// executor owns tool-call execution: validation, permission checks, registry
// dispatch, hooks, and deferred tool promotion.
type executor struct {
	cfg         *Config
	mgr         *contextmgr.Manager
	registry    *tools.Registry
	permissions *permissions.Engine
	emitter     *eventEmitter

	mu             sync.Mutex
	activeDeferred map[string]bool
	taskMgr        *task.Manager
	goals          *goalHolder

	// skillFilter is the active skill's tool restriction (nil = unrestricted).
	// Set when a use_skill call activates a skill whose frontmatter declares
	// allowed_tools; replaced or cleared by the next activation. In-memory
	// only: it is intentionally not part of checkpoints.
	skillFilterMu sync.Mutex
	skillFilter   map[string]bool

	dedupMu sync.Mutex
	dedup   map[string]models.AgentMessage

	// learnMu serializes rule learning: SaveRule is read-modify-write on a
	// YAML file and must not interleave across parallel approvals.
	learnMu sync.Mutex

	// batchLen is the size of the tool-call batch currently being executed;
	// it backs the swarm-exclusivity veto (parallel/chain subagent calls
	// must be the only call in a response).
	batchLen int

	// modeMu guards cfg.Mode: handleSwitchMode writes it while the mode
	// guard and the TUI read it from other goroutines.
	modeMu sync.RWMutex
}

// newExecutor creates an executor with an initialized activeDeferred map.
func newExecutor(cfg *Config, mgr *contextmgr.Manager, registry *tools.Registry, permissions *permissions.Engine, emitter *eventEmitter, taskMgr *task.Manager) *executor {
	ex := &executor{
		cfg:            cfg,
		mgr:            mgr,
		registry:       registry,
		permissions:    permissions,
		emitter:        emitter,
		activeDeferred: make(map[string]bool),
		taskMgr:        taskMgr,
	}
	// Mode/skill surface constraints run as guard policies ahead of the
	// built-in permission chain.
	ex.installGuardPolicies()
	return ex
}

// installGuardPolicies (re)installs the mode/skill guard policies and the
// extension hook policies on the permission engine. It runs before every
// evaluation because executors built directly (e.g. in tests) bypass
// newExecutor; the policies are stateless adapters over executor state, so
// re-installing is cheap and always in sync. Built-in guards run first;
// extension policies run mid-chain after deny and session approvals.
func (e *executor) installGuardPolicies() {
	if e.permissions == nil {
		return
	}
	e.permissions.SetGuardPolicies(modeGuardPolicy{ex: e}, skillGuardPolicy{ex: e}, modeTransitionPolicy{ex: e})
	e.permissions.SetHookPolicies(e.cfg.ExtraGuardPolicies...)
}

// preparedToolCall is the output of the serial prepare phase. Exactly one of
// resolved/run is set: resolved for calls short-circuited before execution
// (meta tools, validation, path guard, permission, hook block), run for
// calls approved and ready to execute.
type preparedToolCall struct {
	call     models.ToolCallContent
	args     map[string]any
	accesses []tools.ToolAccess
	// alsoWaitFor, when >= 0, adds a non-resource ordering edge: this call is
	// a same-batch duplicate of that runnable index and must await it so its
	// dedup lookup is a guaranteed hit (kimi-code v2's toolDedupe).
	alsoWaitFor int
	resolved    models.AgentMessage
	run         func(ctx context.Context) models.AgentMessage
}

func (e *executor) execute(ctx context.Context, turn int, assistantMsg models.AgentMessage, calls []models.ToolCallContent) ([]models.AgentMessage, bool) {
	e.dedupMu.Lock()
	e.dedup = make(map[string]models.AgentMessage)
	e.dedupMu.Unlock()
	e.batchLen = len(calls)

	// Phase 1: serial preparation in provider order. Validation, the path
	// guard, interactive permission prompts, and before-hooks all run here so
	// they cannot interleave; only pure execution overlaps in phase 2.
	prepared := make([]preparedToolCall, len(calls))
	firstRunnableByKey := make(map[string]int)
	for i, call := range calls {
		prepared[i] = e.prepareToolCall(ctx, turn, assistantMsg, call)
		p := &prepared[i]
		if p.run == nil || !isCacheableTool(p.call.Name) {
			continue
		}
		key := dedupKey(p.call.Name, p.args)
		if j, seen := firstRunnableByKey[key]; seen {
			p.alsoWaitFor = j
		} else {
			firstRunnableByKey[key] = i
		}
	}

	return e.runScheduled(ctx, prepared)
}

// finalizeBatch reports the ordered results and whether every call asked to
// terminate the turn.
func finalizeBatch(results []models.AgentMessage) ([]models.AgentMessage, bool) {
	allTerminate := len(results) > 0
	for _, r := range results {
		if !isToolResultTerminate(r) {
			allTerminate = false
		}
	}
	return results, allTerminate
}

func (e *executor) runScheduled(ctx context.Context, prepared []preparedToolCall) ([]models.AgentMessage, bool) {
	accesses := make([][]tools.ToolAccess, len(prepared))
	for i, p := range prepared {
		accesses[i] = p.accesses
	}
	sched := newBatchScheduler(accesses)
	for i, p := range prepared {
		if p.alsoWaitFor >= 0 {
			sched.addWait(i, p.alsoWaitFor)
		}
	}

	results := make([]models.AgentMessage, len(prepared))
	var wg sync.WaitGroup
	for i, p := range prepared {
		if p.run == nil {
			// Short-circuited calls never execute: no side effects and
			// nothing for later calls to wait on.
			results[i] = p.resolved
			sched.finish(i)
			continue
		}
		wg.Add(1)
		go func(idx int, pc preparedToolCall) {
			defer wg.Done()
			defer sched.finish(idx)
			if err := sched.wait(ctx, idx); err != nil {
				results[idx] = e.makeToolResultMessage(pc.call,
					models.NewToolExecutionResultError("canceled before execution: "+err.Error()), true)
				return
			}
			results[idx] = pc.run(ctx)
		}(i, p)
	}
	wg.Wait()
	return finalizeBatch(results)
}

// prepareToolCall runs everything that must stay serial and ordered:
// meta-tool handling, validation, the path security guard, the permission
// chain (possibly interactive), and the before-hook. On any short-circuit
// the final tool_result is produced here and no ToolExecutionStart event is
// ever emitted. Approved calls return a runnable closure plus the resource
// accesses used for scheduling, computed from the final (post-hook) args.
func (e *executor) prepareToolCall(ctx context.Context, turn int, assistantMsg models.AgentMessage, call models.ToolCallContent) preparedToolCall {
	// Normalize arguments first so validation sees a non-nil map.
	args := call.Arguments
	if args == nil {
		args = make(map[string]any)
	}

	shortCircuit := func(result models.ToolExecutionResult, isError bool) preparedToolCall {
		return preparedToolCall{call: call, alsoWaitFor: -1, resolved: e.makeToolResultMessage(call, result, isError)}
	}

	// tool_search is a meta-tool resolved locally: it never reaches the registry.
	if call.Name == tools.ToolSearchName {
		return preparedToolCall{call: call, alsoWaitFor: -1, resolved: e.handleToolSearch(ctx, turn, assistantMsg, call)}
	}

	// switch_mode is a meta-tool that changes the agent mode for the next
	// turn. It still goes through the permission chain first: the
	// mode-transition guard may require user approval to leave a mode with
	// require_approval_to_exit set.
	if call.Name == switchModeToolName {
		info := ToolCallInfo{
			AssistantMessage: assistantMsg,
			ToolCall:         call,
			Args:             args,
			Context:          e.mgr.AllMessages(),
		}
		allowed, _, denyReason, confirmErr := e.confirmToolCall(ctx, turn, info)
		if confirmErr != nil || !allowed {
			reason := denyReason
			if confirmErr != nil {
				reason = confirmErr.Error()
			}
			if reason == "" {
				reason = "denied"
			}
			return shortCircuit(models.NewToolExecutionResultError(reason), true)
		}
		return preparedToolCall{call: call, alsoWaitFor: -1, resolved: e.handleSwitchMode(ctx, turn, assistantMsg, call)}
	}

	// Swarm exclusivity: a swarm subagent call (prompt_template + items) is
	// itself the concurrency unit and must be the only tool call in the
	// response (kimi-code's AgentSwarmExclusiveDeny). Mixed batches break
	// ordering assumptions, shared concurrency budgets, and result
	// attribution, so the call is refused with a corrective message; other
	// calls proceed.
	if e.batchLen > 1 && call.Name == "subagent" {
		if _, swarm := args["items"]; swarm {
			return shortCircuit(models.NewToolExecutionResultError(swarmExclusivityMessage), true)
		}
	}

	// Mode/skill tool-surface restrictions are enforced inside the permission
	// chain (guard policies, see guard_policies.go) rather than by filtering
	// the tool schemas: schema filtering would rebuild the tools array on
	// every switch, and tools are the first layer of the provider cache
	// prefix, so the whole conversation would be re-billed as fresh input.

	// Pre-execution argument validation. On failure we do NOT emit any tool
	// events: the failed attempt stays invisible in the live TUI, and the error
	// tool_result is fed back so the LLM can self-correct next turn.
	if exec, ok := e.registry.Get(call.Name); ok {
		if err := tools.ValidateArgs(exec.Definition(), args); err != nil {
			return shortCircuit(models.NewToolExecutionResultError(err.Error()), true)
		}
	}

	// Path security guard: resolves and validates file paths BEFORE the
	// permission check (mirrors Kimi Code's resolveExecution phase). Sensitive
	// files and relative-path workspace escapes are denied here so the model
	// receives an actionable error without ever triggering a user approval
	// prompt. The guard only validates; it does not rewrite args — each tool
	// still resolves its path via resolveInCwd as before. The operation for
	// error messages comes from the tool's own access declaration (the single
	// source of truth for what a tool does with its path argument).
	if rawPath, ok := args["path"].(string); ok && rawPath != "" {
		toolOp := builtin.OpRead
		if exec, ok := e.registry.Get(call.Name); ok {
			toolOp = pathOpForDeclaredAccess(exec, args)
		}
		cwd, _ := os.Getwd()
		if _, err := builtin.ResolvePathAccess(rawPath, cwd, toolOp); err != nil {
			return shortCircuit(models.NewToolExecutionResultError(err.Error()), true)
		}
	}

	// Permission check: engine decision + optional interactive confirmation.
	info := ToolCallInfo{
		AssistantMessage: assistantMsg,
		ToolCall:         call,
		Args:             args,
		Context:          e.mgr.AllMessages(),
	}
	allowed, _, denyReason, confirmErr := e.confirmToolCall(ctx, turn, info)
	if confirmErr != nil || !allowed {
		reason := denyReason
		if confirmErr != nil {
			reason = confirmErr.Error()
		}
		if reason == "" {
			reason = "denied"
		}
		return shortCircuit(models.NewToolExecutionResultError(reason), true)
	}

	// Declarative before-tool hooks (e.g. extensions) run after permission approval.
	if e.cfg.BeforeToolCall != nil {
		beforeResult, err := e.cfg.BeforeToolCall(ctx, ToolCallInfo{
			AssistantMessage: assistantMsg,
			ToolCall:         call,
			Args:             args,
			Context:          e.mgr.AllMessages(),
		})
		if err != nil {
			return shortCircuit(models.NewToolExecutionResultError(err.Error()), true)
		}
		if beforeResult != nil && beforeResult.Block {
			return shortCircuit(models.NewToolExecutionResultError(beforeResult.Reason), true)
		}
		if beforeResult != nil && beforeResult.ModifiedArgs != nil {
			args = beforeResult.ModifiedArgs
			// Hook-rewritten args must pass the same schema validation as the
			// original arguments.
			if exec, ok := e.registry.Get(call.Name); ok {
				if err := tools.ValidateArgs(exec.Definition(), args); err != nil {
					return shortCircuit(models.NewToolExecutionResultError(err.Error()), true)
				}
			}
		}
	}

	// Resource accesses for the scheduler, from the final args. Tools that do
	// not declare accesses default to OpAll (serial with everything).
	accesses := []tools.ToolAccess{{Op: tools.OpAll}}
	if exec, ok := e.registry.Get(call.Name); ok {
		if declarer, ok := exec.(tools.AccessDeclarer); ok {
			if declared := declarer.DeclareAccesses(args); len(declared) > 0 {
				accesses = declared
			}
		}
	}

	return preparedToolCall{
		call:        call,
		args:        args,
		accesses:    accesses,
		alsoWaitFor: -1,
		run: func(runCtx context.Context) models.AgentMessage {
			return e.runToolCall(runCtx, turn, assistantMsg, call, args)
		},
	}
}

// runToolCall is the concurrent phase: dedup, execution, after-hook, and
// events. It only runs after prepareToolCall approved the call and the batch
// scheduler unblocked it.
func (e *executor) runToolCall(ctx context.Context, turn int, assistantMsg models.AgentMessage, call models.ToolCallContent, args map[string]any) models.AgentMessage {
	// Same-turn deduplication for read-only idempotent tools, keyed on the
	// final (post-hook) args. A dedup hit returns before ToolExecutionStart, so
	// duplicate calls never emit a Start without a matching End. The scheduler
	// orders same-batch duplicates after the original (alsoWaitFor edge), so
	// this lookup is deterministic even under concurrent execution.
	if isCacheableTool(call.Name) {
		key := dedupKey(call.Name, args)
		e.dedupMu.Lock()
		cached, ok := e.dedup[key]
		e.dedupMu.Unlock()
		if ok {
			return cloneAgentMessage(cached, call.ID)
		}
	}

	e.emitter.emit(ctx, events.ToolExecutionStartEvent{
		Base:       events.Base{Type: events.ToolExecutionStart, Turn: turn},
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Args:       call.Arguments,
	})

	result, isError := e.registry.Execute(ctx, call.ID, call.Name, args)

	// A successful use_skill call publishes the activated skill's restriction
	// via result details; adopt it for subsequent calls.
	if call.Name == skills.UseSkillToolName && !isError {
		e.updateSkillFilter(result.Details)
	}

	// Run after hook.
	if e.cfg.AfterToolCall != nil {
		afterResult, err := e.cfg.AfterToolCall(ctx, ToolCallResultInfo{
			AssistantMessage: assistantMsg,
			ToolCall:         call,
			Args:             args,
			Result:           result,
			IsError:          isError,
			Context:          e.mgr.AllMessages(),
		})
		if err != nil {
			result = models.NewToolExecutionResultError(err.Error())
			isError = true
		} else if afterResult != nil {
			if len(afterResult.Content) > 0 {
				result.Content = afterResult.Content
			}
			if afterResult.Details != nil {
				result.Details = afterResult.Details
			}
			if afterResult.IsError != nil {
				isError = *afterResult.IsError
			}
			result.Terminate = afterResult.Terminate
		}
	}

	// Reconcile task list when the model updates its plan.
	if call.Name == task.ToolName && e.taskMgr != nil && !isError {
		if raw, ok := args["todos"]; ok {
			if parsed, parseErr := task.Parse(raw); parseErr == nil {
				reconciled, warnings, _ := e.taskMgr.ReplaceAll(parsed)
				result = appendWarnings(result, warnings)
				e.emitter.emit(ctx, events.TaskListUpdatedEvent{
					Base:  events.Base{Type: events.TaskListUpdated, Turn: turn},
					Tasks: reconciled,
				})
			}
		}
	}

	// Apply a model-requested goal transition (update_goal is inert; the
	// executor owns the record, same as task reconciliation).
	if call.Name == builtin.UpdateGoalToolName && e.goals != nil && !isError {
		status, _ := args["status"].(string)
		reason, _ := args["reason"].(string)
		newStatus, applyErr := e.goals.applyUpdate(status, reason)
		if applyErr != nil {
			result = models.NewToolExecutionResultError(applyErr.Error())
			isError = true
		} else {
			g := e.goals.get()
			result = models.NewToolExecutionResultText("Goal marked " + string(newStatus))
			e.emitter.emit(ctx, events.GoalUpdatedEvent{
				Base:     events.Base{Type: events.GoalUpdated, Turn: turn},
				Objective: g.Objective,
				Status:   string(newStatus),
				Reason:   g.BlockReason,
			})
		}
	}

	e.emitter.emit(ctx, events.ToolExecutionEndEvent{
		Base:       events.Base{Type: events.ToolExecutionEnd, Turn: turn},
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Result:     result,
		IsError:    isError,
	})

	msg := e.makeToolResultMessage(call, result, isError)

	if isCacheableTool(call.Name) {
		key := dedupKey(call.Name, args)
		e.dedupMu.Lock()
		e.dedup[key] = msg
		e.dedupMu.Unlock()
	}

	e.emitter.emit(ctx, events.MessageStartEvent{
		Base:    events.Base{Type: events.MessageStart, Turn: turn},
		Message: msg,
	})
	e.emitter.emit(ctx, events.MessageEndEvent{
		Base:    events.Base{Type: events.MessageEnd, Turn: turn},
		Message: msg,
	})

	return msg
}

// skillAllows reports whether the active skill's tool restriction permits the
// named tool. A nil filter means unrestricted.
func (e *executor) skillAllows(name string) bool {
	e.skillFilterMu.Lock()
	defer e.skillFilterMu.Unlock()
	if e.skillFilter == nil {
		return true
	}
	return e.skillFilter[name]
}

// currentMode returns the active mode's config.
func (e *executor) currentMode() ModeConfig {
	e.modeMu.RLock()
	defer e.modeMu.RUnlock()
	if e.cfg.ModeManager == nil {
		return ModeConfig{Name: e.cfg.Mode}
	}
	return e.cfg.ModeManager.Get(e.cfg.Mode)
}

// currentModeName returns the active mode name, lock-protected for readers
// outside the run loop (e.g. the TUI status bar).
func (e *executor) currentModeName() string {
	e.modeMu.RLock()
	defer e.modeMu.RUnlock()
	return e.cfg.Mode
}

// modeDenies reports whether the active mode forbids a tool. The reason names
// the escape hatch so the model can recover from the tool_result rather than
// retrying the same blocked call.
func (e *executor) modeDenies(name string) (string, bool) {
	mode := e.currentMode()
	blocked := false
	if len(mode.AllowedTools) > 0 && !matchToolName(name, patternSet(mode.AllowedTools)) {
		blocked = true
	}
	if len(mode.DeniedTools) > 0 && matchToolName(name, patternSet(mode.DeniedTools)) {
		blocked = true
	}
	if !blocked {
		return "", false
	}
	return "tool " + name + " is not available in " + mode.Name + " mode. Call " +
		switchModeToolName + ` with a mode that permits it (e.g. mode="code") before retrying.`, true
}

func patternSet(patterns []string) map[string]bool {
	set := make(map[string]bool, len(patterns))
	for _, p := range patterns {
		set[p] = true
	}
	return set
}

// skillFilterNames returns the sorted allowed-tool list for error messages.
func (e *executor) skillFilterNames() []string {
	e.skillFilterMu.Lock()
	defer e.skillFilterMu.Unlock()
	names := make([]string, 0, len(e.skillFilter))
	for n := range e.skillFilter {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// updateSkillFilter adopts the restriction published by a successful use_skill
// call. An empty (or nil) list lifts the restriction; a missing key leaves the
// current filter untouched.
func (e *executor) updateSkillFilter(details map[string]any) {
	raw, ok := details[skills.AllowedToolsDetailsKey]
	if !ok {
		return
	}
	names, _ := raw.([]string)
	e.skillFilterMu.Lock()
	defer e.skillFilterMu.Unlock()
	if len(names) == 0 {
		e.skillFilter = nil
		return
	}
	e.skillFilter = make(map[string]bool, len(names))
	for _, n := range names {
		e.skillFilter[n] = true
	}
}

func (e *executor) makeToolResultMessage(call models.ToolCallContent, result models.ToolExecutionResult, isError bool) models.AgentMessage {
	details := result.Details
	if details == nil {
		details = make(map[string]any)
	}
	details["terminate"] = result.Terminate
	return models.NewAgentMessage(models.RoleToolResult, models.ToolResultContent{
		ToolCallID: call.ID,
		Name:       call.Name,
		Content:    result.Content,
		IsError:    isError,
		Details:    details,
	})
}

// appendWarnings appends warning lines to the text content of a tool execution result.
func appendWarnings(result models.ToolExecutionResult, warnings []string) models.ToolExecutionResult {
	if len(warnings) == 0 {
		return result
	}
	var sb strings.Builder
	sb.WriteString(result.Text())
	if sb.Len() > 0 {
		sb.WriteString("\n\n")
	}
	sb.WriteString("Warnings:\n")
	for _, w := range warnings {
		sb.WriteString("- ")
		sb.WriteString(w)
		sb.WriteString("\n")
	}
	result.Content = []models.ContentPart{models.TextContent{Text: sb.String()}}
	return result
}

func isToolResultTerminate(msg models.AgentMessage) bool {
	if len(msg.Content) == 0 {
		return false
	}
	result, ok := msg.Content[0].(models.ToolResultContent)
	if !ok {
		return false
	}
	if result.Details == nil {
		return false
	}
	v, ok := result.Details["terminate"].(bool)
	return ok && v
}

func (e *executor) handleToolSearch(ctx context.Context, turn int, assistantMsg models.AgentMessage, call models.ToolCallContent) models.AgentMessage {
	query := ""
	if q, ok := call.Arguments["query"].(string); ok {
		query = q
	}

	e.emitter.emit(ctx, events.ToolExecutionStartEvent{
		Base:       events.Base{Type: events.ToolExecutionStart, Turn: turn},
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Args:       call.Arguments,
	})

	hits := e.registry.SearchTools(query)
	for _, d := range hits {
		e.activateDeferredTool(d.Name)
	}
	result := models.NewToolExecutionResultText(toolSearchResultText(hits))

	e.emitter.emit(ctx, events.ToolExecutionEndEvent{
		Base:       events.Base{Type: events.ToolExecutionEnd, Turn: turn},
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Result:     result,
		IsError:    false,
	})

	return e.makeToolResultMessage(call, result, false)
}

func toolSearchResultText(hits []models.ToolDefinition) string {
	if len(hits) == 0 {
		return "No tools matched. Try a broader keyword."
	}
	schemas, err := json.Marshal(hits)
	if err != nil {
		var parts []string
		for _, d := range hits {
			parts = append(parts, d.Name)
		}
		return "Matched tools: " + strings.Join(parts, ", ") + ". You may now call them directly."
	}
	return "Matched tools (full schemas below):\n" + string(schemas) + "\nYou may now call them directly."
}

func (e *executor) handleSwitchMode(ctx context.Context, turn int, assistantMsg models.AgentMessage, call models.ToolCallContent) models.AgentMessage {
	e.emitter.emit(ctx, events.ToolExecutionStartEvent{
		Base:       events.Base{Type: events.ToolExecutionStart, Turn: turn},
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Args:       call.Arguments,
	})

	args := call.Arguments
	if args == nil {
		args = make(map[string]any)
	}

	target, _ := args["mode"].(string)
	var result models.ToolExecutionResult
	isError := false
	if target == "" {
		result = models.NewToolExecutionResultError("missing mode argument")
		isError = true
	} else if e.cfg.ModeManager == nil || e.cfg.ModeManager.Get(target).Name != target {
		result = models.NewToolExecutionResultError("unknown mode: " + target)
		isError = true
	} else {
		e.modeMu.Lock()
		e.cfg.Mode = target
		e.modeMu.Unlock()
		result = models.NewToolExecutionResultText("Switched to " + target + " mode")
	}

	e.emitter.emit(ctx, events.ToolExecutionEndEvent{
		Base:       events.Base{Type: events.ToolExecutionEnd, Turn: turn},
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Result:     result,
		IsError:    isError,
	})

	return e.makeToolResultMessage(call, result, isError)
}

// baseToolDefinitions returns the tool definitions for the upcoming turn,
// honoring deferred tool loading and any previously promoted deferred tools.
func (e *executor) baseToolDefinitions() []models.ToolDefinition {
	if !e.cfg.DeferredTools {
		return append(e.registry.Definitions(), switchModeDefinition())
	}
	core := e.cfg.CoreTools
	if len(core) == 0 {
		core = DefaultCoreTools
	}
	active, deferred := e.registry.DeferredDefinitions(core...)

	// Promote any deferred tools that have been loaded via tool_search.
	promoted := e.activeDeferredNames()
	for _, name := range promoted {
		for i, stub := range deferred {
			if stub.Name == name {
				if exec, ok := e.registry.Get(name); ok {
					deferred[i] = exec.Definition()
				}
				break
			}
		}
	}

	return append(append(active, deferred...), switchModeDefinition())
}

func (e *executor) activateDeferredTool(name string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.activeDeferred == nil {
		e.activeDeferred = make(map[string]bool)
	}
	e.activeDeferred[name] = true
}

func (e *executor) activeDeferredNames() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, 0, len(e.activeDeferred))
	for k := range e.activeDeferred {
		out = append(out, k)
	}
	return out
}

// confirmToolCall evaluates the permission chain and, if required, asks the
// configured UserConfirmation handler. It returns whether the call may
// proceed, the confirm scope, and the reason when the call is blocked (fed
// back to the model).
func (e *executor) confirmToolCall(ctx context.Context, turn int, info ToolCallInfo) (bool, ConfirmScope, string, error) {
	// A nil engine means no permission system was configured: allow.
	if e.permissions == nil {
		return true, ScopeOnce, "", nil
	}
	e.installGuardPolicies()
	decision, policy, policyReason := e.permissions.DecideWithSource(info.ToolCall.Name, info.Args)

	var blocked bool
	var blockReason string
	allowed := decision == permissions.Allow
	if decision == permissions.Deny {
		blocked = true
		blockReason = policyReason
		if blockReason == "" {
			blockReason = "denied by policy (" + policy + ")"
		}
	}

	decisionLabel := string(decision)
	if e.permissions.UnsafeMode() && decision == permissions.Allow {
		decisionLabel = "unsafe-allow"
	}

	e.emitter.emit(ctx, events.AuditEvent{
		Base:        events.Base{Type: events.Audit, Turn: turn},
		ToolCallID:  info.ToolCall.ID,
		ToolName:    info.ToolCall.Name,
		Args:        info.Args,
		Decision:    decisionLabel,
		Allowed:     allowed,
		Blocked:     blocked,
		BlockReason: blockReason,
	})

	switch decision {
	case permissions.Allow:
		return true, ScopeOnce, "", nil
	case permissions.Deny:
		return false, ScopeDeny, blockReason, nil
	case permissions.Ask:
		if e.cfg.UserConfirm == nil {
			return false, ScopeDeny, "approval required but no confirmation handler is configured", nil
		}
		res, err := e.cfg.UserConfirm.ConfirmWithScope(ctx, info)
		askBlockReason := ""
		if err != nil {
			askBlockReason = err.Error()
		}
		e.emitter.emit(ctx, events.AuditEvent{
			Base:        events.Base{Type: events.Audit, Turn: turn},
			ToolCallID:  info.ToolCall.ID,
			ToolName:    info.ToolCall.Name,
			Args:        info.Args,
			Decision:    "ask",
			Allowed:     err == nil && res.Allow,
			Blocked:     err != nil || !res.Allow,
			BlockReason: askBlockReason,
		})
		if err != nil {
			return false, ScopeDeny, "", err
		}
		if res.Allow {
			e.learnRule(info, res.Scope)
		}
		if !res.Allow {
			return false, ScopeDeny, "denied by user", nil
		}
		return true, res.Scope, "", nil
	default:
		return true, ScopeOnce, "", nil
	}
}

// learnRule records an approval according to its scope. Session scope stores
// an exact-match rule in memory (never persisted); project/global scopes
// persist a generalized pattern to the YAML rule files. switch_mode is never
// learned: a remembered approval must not be able to bypass mode-transition
// approval (require_approval_to_exit).
func (e *executor) learnRule(info ToolCallInfo, scope ConfirmScope) {
	tool := info.ToolCall.Name
	if tool == switchModeToolName {
		return
	}
	if scope == ScopeSession {
		e.permissions.AddSessionRule(tool, info.Args)
		return
	}
	if scope != ScopeProject && scope != ScopeGlobal {
		return
	}

	pattern := "*"
	if tool == "bash" {
		cmd, _ := info.Args["command"].(string)
		// Learned bash rules are verbatim (literal-match only): approving
		// "rm -rf /tmp/x" must never generalize into "rm *" and silently
		// allow the next "rm -rf /etc" (kimi-code's literalRulePattern).
		pattern = permissions.LiteralCommandPattern(cmd)
	} else if path, ok := info.Args["path"].(string); ok {
		pattern = path
	}

	e.learnMu.Lock()
	defer e.learnMu.Unlock()

	var target string
	var reload func(string) error
	if scope == ScopeProject {
		cwd, _ := os.Getwd()
		target = filepath.Join(cwd, ".lcoder", "permissions.yaml")
		reload = e.permissions.LoadProjectRules
	} else {
		target = paths.LCoderHome("permissions", "global.yaml")
		reload = e.permissions.LoadGlobalLearnedRules
	}
	// Persist first, then reload: the just-learned rule takes effect in this
	// session immediately, not on the next approval.
	if err := permissions.SaveRule(target, tool, pattern, permissions.Allow); err != nil {
		return
	}
	_ = reload(target)
}

func isCacheableTool(name string) bool {
	switch name {
	case "read", "ls", "grep", "find":
		return true
	}
	return false
}

// pathOpForDeclaredAccess derives the path-guard operation (used for error
// messages) from the tool's own access declaration — the single source of
// truth for what a tool does with its path argument.
func pathOpForDeclaredAccess(exec tools.Executable, args map[string]any) builtin.PathOperation {
	declarer, ok := exec.(tools.AccessDeclarer)
	if !ok {
		return builtin.OpRead
	}
	accesses := declarer.DeclareAccesses(args)
	if len(accesses) == 0 {
		return builtin.OpRead
	}
	switch accesses[0].Op {
	case tools.OpWrite, tools.OpReadWrite:
		return builtin.OpWrite
	case tools.OpSearch:
		return builtin.OpSearch
	default:
		return builtin.OpRead
	}
}

func dedupKey(name string, args map[string]any) string {
	return name + "|" + tools.NormalizeArgs(args)
}

func cloneAgentMessage(msg models.AgentMessage, newToolCallID string) models.AgentMessage {
	cloned := msg
	cloned.ID = uuid.New().String()[:12]
	cloned.Content = make([]models.ContentPart, len(msg.Content))
	copy(cloned.Content, msg.Content)
	cloned.Metadata = make(map[string]any)
	for k, v := range msg.Metadata {
		cloned.Metadata[k] = v
	}
	if len(cloned.Content) > 0 {
		if tr, ok := cloned.Content[0].(models.ToolResultContent); ok {
			tr.ToolCallID = newToolCallID
			details := make(map[string]any)
			for k, v := range tr.Details {
				details[k] = v
			}
			details["deduplicated"] = true
			tr.Details = details
			cloned.Content[0] = tr
		}
	}
	return cloned
}
