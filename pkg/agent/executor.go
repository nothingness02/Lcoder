package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/lcoder/lcoder/internal/paths"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/permissions"
	"github.com/lcoder/lcoder/pkg/permissions/bashrisk"
	"github.com/lcoder/lcoder/pkg/task"
	"github.com/lcoder/lcoder/pkg/tools"
)

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

	dedupMu sync.Mutex
	dedup   map[string]models.AgentMessage
}

// newExecutor creates an executor with an initialized activeDeferred map.
func newExecutor(cfg *Config, mgr *contextmgr.Manager, registry *tools.Registry, permissions *permissions.Engine, emitter *eventEmitter, taskMgr *task.Manager) *executor {
	return &executor{
		cfg:            cfg,
		mgr:            mgr,
		registry:       registry,
		permissions:    permissions,
		emitter:        emitter,
		activeDeferred: make(map[string]bool),
		taskMgr:        taskMgr,
	}
}

func (e *executor) execute(ctx context.Context, turn int, assistantMsg models.AgentMessage, calls []models.ToolCallContent, execMode models.ExecutionMode) ([]models.AgentMessage, bool) {
	e.dedupMu.Lock()
	e.dedup = make(map[string]models.AgentMessage)
	e.dedupMu.Unlock()

	sequential := execMode == models.ExecutionSequential
	if e.cfg.ModeManager != nil {
		mode := e.cfg.ModeManager.Get(e.cfg.Mode)
		if mode.ExecutionMode == "sequential" {
			sequential = true
		}
	}
	if !sequential {
		for _, call := range calls {
			if exec, ok := e.registry.Get(call.Name); ok {
				if exec.Definition().ExecutionMode == models.ExecutionSequential {
					sequential = true
					break
				}
			}
		}
	}

	if sequential {
		return e.executeSequential(ctx, turn, assistantMsg, calls)
	}
	return e.executeParallel(ctx, turn, assistantMsg, calls)
}

func (e *executor) executeSequential(ctx context.Context, turn int, assistantMsg models.AgentMessage, calls []models.ToolCallContent) ([]models.AgentMessage, bool) {
	var results []models.AgentMessage
	allTerminate := true
	for _, call := range calls {
		resultMsg := e.executeOneToolCall(ctx, turn, assistantMsg, call)
		results = append(results, resultMsg)
		if !isToolResultTerminate(resultMsg) {
			allTerminate = false
		}
	}
	return results, allTerminate && len(calls) > 0
}

func (e *executor) executeParallel(ctx context.Context, turn int, assistantMsg models.AgentMessage, calls []models.ToolCallContent) ([]models.AgentMessage, bool) {
	type pair struct {
		call   models.ToolCallContent
		result models.AgentMessage
	}

	var wg sync.WaitGroup
	pairs := make([]pair, len(calls))
	for i, call := range calls {
		wg.Add(1)
		go func(idx int, c models.ToolCallContent) {
			defer wg.Done()
			pairs[idx] = pair{call: c, result: e.executeOneToolCall(ctx, turn, assistantMsg, c)}
		}(i, call)
	}
	wg.Wait()

	var results []models.AgentMessage
	allTerminate := true
	for _, p := range pairs {
		results = append(results, p.result)
		if !isToolResultTerminate(p.result) {
			allTerminate = false
		}
	}
	return results, allTerminate && len(calls) > 0
}

func (e *executor) executeOneToolCall(ctx context.Context, turn int, assistantMsg models.AgentMessage, call models.ToolCallContent) models.AgentMessage {
	// Normalize arguments first so validation sees a non-nil map.
	args := call.Arguments
	if args == nil {
		args = make(map[string]any)
	}

	// tool_search is a meta-tool resolved locally: it never reaches the registry.
	if call.Name == tools.ToolSearchName {
		return e.handleToolSearch(ctx, turn, assistantMsg, call)
	}

	// Pre-execution argument validation. On failure we do NOT emit any tool
	// events: the failed attempt stays invisible in the live TUI, and the error
	// tool_result is fed back so the LLM can self-correct next turn.
	if exec, ok := e.registry.Get(call.Name); ok {
		if err := tools.ValidateArgs(exec.Definition(), args); err != nil {
			return e.makeToolResultMessage(call, models.NewToolExecutionResultError(err.Error()), true)
		}
	}

	// Same-turn deduplication for read-only idempotent tools.
	if isCacheableTool(call.Name) {
		key := dedupKey(call.Name, args)
		e.dedupMu.Lock()
		cached, ok := e.dedup[key]
		e.dedupMu.Unlock()
		if ok {
			return cloneAgentMessage(cached, call.ID)
		}
	}

	// Permission check: engine decision + optional interactive confirmation.
	info := ToolCallInfo{
		AssistantMessage: assistantMsg,
		ToolCall:         call,
		Args:             args,
		Context:          e.mgr.AllMessages(),
	}
	allowed, _, confirmErr := e.confirmToolCall(ctx, turn, info)
	if confirmErr != nil || !allowed {
		reason := "denied"
		if confirmErr != nil {
			reason = confirmErr.Error()
		}
		return e.makeToolResultMessage(call, models.NewToolExecutionResultError(reason), true)
	}

	e.emitter.emit(ctx, events.ToolExecutionStartEvent{
		Base:       events.Base{Type: events.ToolExecutionStart, Turn: turn},
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Args:       call.Arguments,
	})

	// Declarative before-tool hooks (e.g. extensions) run after permission approval.
	if e.cfg.BeforeToolCall != nil {
		beforeResult, err := e.cfg.BeforeToolCall(ctx, ToolCallInfo{
			AssistantMessage: assistantMsg,
			ToolCall:         call,
			Args:             args,
			Context:          e.mgr.AllMessages(),
		})
		if err != nil {
			return e.makeToolResultMessage(call, models.NewToolExecutionResultError(err.Error()), true)
		}
		if beforeResult != nil && beforeResult.Block {
			return e.makeToolResultMessage(call, models.NewToolExecutionResultError(beforeResult.Reason), true)
		}
	}

	result, isError := e.registry.Execute(ctx, call.ID, call.Name, args)

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
		if raw, ok := call.Arguments["todos"]; ok {
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

// baseToolDefinitions returns the tool definitions for the upcoming turn,
// honoring deferred tool loading and any previously promoted deferred tools.
func (e *executor) baseToolDefinitions() []models.ToolDefinition {
	if !e.cfg.DeferredTools {
		return e.registry.Definitions()
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

	return append(active, deferred...)
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

// confirmToolCall evaluates the permission engine and, if required, asks the
// configured UserConfirmation handler. It returns true when the call may proceed.
func (e *executor) confirmToolCall(ctx context.Context, turn int, info ToolCallInfo) (bool, ConfirmScope, error) {
	decision := e.permissions.Decide(info.ToolCall.Name, info.Args)

	// Low-risk bash commands do not need interactive approval even when no rule
	// explicitly allows them.
	if decision == permissions.Ask && info.ToolCall.Name == "bash" {
		cmd, _ := info.Args["command"].(string)
		cwd, _ := os.Getwd()
		report := bashrisk.Classify(cmd, cwd)
		if report.Level == bashrisk.RiskNone || report.Level == bashrisk.RiskLow {
			decision = permissions.Allow
		}
	}

	var blocked bool
	var blockReason string
	allowed := decision == permissions.Allow
	if decision == permissions.Deny {
		blocked = true
		blockReason = "denied by policy"
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
		return true, ScopeOnce, nil
	case permissions.Deny:
		return false, ScopeDeny, nil
	case permissions.Ask:
		if e.cfg.UserConfirm == nil {
			return false, ScopeDeny, nil
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
			return false, ScopeDeny, err
		}
		if res.Allow {
			e.learnRule(info, res.Scope)
		}
		return res.Allow, res.Scope, nil
	default:
		return true, ScopeOnce, nil
	}
}

func (e *executor) learnRule(info ToolCallInfo, scope ConfirmScope) {
	if scope != ScopeProject && scope != ScopeGlobal {
		return
	}

	tool := info.ToolCall.Name
	pattern := "*"
	if tool == "bash" {
		cmd, _ := info.Args["command"].(string)
		pattern = permissions.PatternForCommand(cmd)
	} else if path, ok := info.Args["path"].(string); ok {
		pattern = path
	}

	var target string
	if scope == ScopeProject {
		cwd, _ := os.Getwd()
		target = filepath.Join(cwd, ".lcoder", "permissions.yaml")
		_ = e.permissions.LoadProjectRules(target)
	} else {
		target = paths.LCoderHome("permissions", "global.yaml")
		_ = e.permissions.LoadGlobalLearnedRules(target)
	}
	_ = permissions.SaveRule(target, tool, pattern, permissions.Allow)
}

func isCacheableTool(name string) bool {
	switch name {
	case "read", "ls", "grep", "find":
		return true
	}
	return false
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
