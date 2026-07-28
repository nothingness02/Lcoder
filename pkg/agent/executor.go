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
	"github.com/lcoder/lcoder/pkg/permissions/bashrisk"
	"github.com/lcoder/lcoder/pkg/skills"
	"github.com/lcoder/lcoder/pkg/task"
	"github.com/lcoder/lcoder/pkg/tools"
)

const switchModeToolName = "switch_mode"

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

	// skillFilter is the active skill's tool restriction (nil = unrestricted).
	// Set when a use_skill call activates a skill whose frontmatter declares
	// allowed_tools; replaced or cleared by the next activation. In-memory
	// only: it is intentionally not part of checkpoints.
	skillFilterMu sync.Mutex
	skillFilter   map[string]bool

	dedupMu sync.Mutex
	dedup   map[string]models.AgentMessage
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
	if permissions != nil {
		permissions.SetGuardPolicies(modeGuardPolicy{ex: ex}, skillGuardPolicy{ex: ex})
	}
	return ex
}

// installGuardPolicies (re)installs the mode/skill guard policies on the
// permission engine. It runs before every evaluation because executors built
// directly (e.g. in tests) bypass newExecutor; the policies are stateless
// adapters over executor state, so re-installing is cheap and always in sync.
func (e *executor) installGuardPolicies() {
	if e.permissions == nil {
		return
	}
	e.permissions.SetGuardPolicies(modeGuardPolicy{ex: e}, skillGuardPolicy{ex: e}, modeTransitionPolicy{ex: e})
}

func (e *executor) execute(ctx context.Context, turn int, assistantMsg models.AgentMessage, calls []models.ToolCallContent, execMode models.ExecutionMode) ([]models.AgentMessage, bool) {
	e.dedupMu.Lock()
	e.dedup = make(map[string]models.AgentMessage)
	e.dedupMu.Unlock()

	sequential := execMode == models.ExecutionSequential
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
			return e.makeToolResultMessage(call, models.NewToolExecutionResultError(reason), true)
		}
		return e.handleSwitchMode(ctx, turn, assistantMsg, call)
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
			return e.makeToolResultMessage(call, models.NewToolExecutionResultError(err.Error()), true)
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
		return e.makeToolResultMessage(call, models.NewToolExecutionResultError(reason), true)
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
			return e.makeToolResultMessage(call, models.NewToolExecutionResultError(err.Error()), true)
		}
		if beforeResult != nil && beforeResult.Block {
			return e.makeToolResultMessage(call, models.NewToolExecutionResultError(beforeResult.Reason), true)
		}
		if beforeResult != nil && beforeResult.ModifiedArgs != nil {
			args = beforeResult.ModifiedArgs
			// Hook-rewritten args must pass the same schema validation as the
			// original arguments.
			if exec, ok := e.registry.Get(call.Name); ok {
				if err := tools.ValidateArgs(exec.Definition(), args); err != nil {
					return e.makeToolResultMessage(call, models.NewToolExecutionResultError(err.Error()), true)
				}
			}
		}
	}

	// Same-turn deduplication for read-only idempotent tools, keyed on the
	// final (post-hook) args. A dedup hit returns before ToolExecutionStart, so
	// duplicate calls never emit a Start without a matching End.
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
	if e.cfg.ModeManager == nil {
		return ModeConfig{Name: e.cfg.Mode}
	}
	return e.cfg.ModeManager.Get(e.cfg.Mode)
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
		e.cfg.Mode = target
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
