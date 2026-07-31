# Hook 机制完善与 HTTP 工具退役 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 落地 `docs/hook-extension-comparison.md` 的四项:接线 shell on_stop/before_compact 死配置、退役 JSON 描述符 HTTP 工具、extension 增加 stop/session_start hook、权限引擎加 hook 插槽。

**Architecture:** shell hook 复用现有 `runShellHook`(exit 0/2 语义);on_stop 经 goal 模式落地的 `ContinuationDecider` 链接入,exit 2 = 续跑 + stderr 经 Steer 反馈给模型(Claude Code Stop hook 语义);before_compact 包装 agentsetup 的内建 summarizer;extension 新 hook 沿用 proto/host/bridge 三层;权限插槽以 `permissions.Policy` 形式由 executor 装在 guard policies 末尾。

**Tech Stack:** Go 1.25,无新依赖。

**关键背景(实施前必读):**
- `pkg/agent/hooks/shell.go` 的 `runShellHook`:exit 0=allow,exit 2=block(stderr=reason),fail-open。
- `ContinuationDecider` 语义(`pkg/agent/loop.go`):首个 (false,_) 或 (_,err) 停;空链=停;内置 veto 只停不续。
- `agentsetup.NewContextManager`(setup.go:156)是 summarizer 唯一装配点,test/prod/eval 共用。
- `permissions.Policy` 接口:`Name()` + `Decide(req) (Decision, string, bool)`;executor 每次 confirm 前 `SetGuardPolicies(mode, skill, modeTransition)` 覆盖安装。
- 项目无向后兼容要求。

---

### Task 1: shell `on_stop` → ContinuationDecider

**Files:**
- Modify: `pkg/agent/hooks/shell.go`(ShellOnStop)
- Modify: `pkg/agent/hooks/from_config.go`(OnStopFromConfig)
- Modify: `cmd/lcoder/wiring.go`(装配进 Config.ContinuationDeciders)
- Test: `pkg/agent/hooks/shell_test.go`(追加)

- [ ] **Step 1: 写失败测试**

`pkg/agent/hooks/shell_test.go` 追加:

```go
// exit 2 = 阻止停止(续跑),stderr 经 steer 回调反馈;exit 0 = 允许停止。
func TestShellOnStopExitSemantics(t *testing.T) {
	tmp := t.TempDir()
	script := filepath.Join(tmp, "hook.sh")
	// 第一次 exit 2,第二次 exit 0(用标记文件区分)。
	if err := os.WriteFile(script, []byte(
		"#!/bin/sh\nif [ -f \""+tmp+"/mark\" ]; then exit 0; else touch \""+tmp+"/mark\"; echo 'keep going' >&2; exit 2; fi\n",
	), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.ShellHookConfig{Enabled: true, Command: "sh " + script, Timeout: 5}

	var steered []string
	decider := ShellOnStop(cfg, "sess", func(reason string) { steered = append(steered, reason) })

	cont, err := decider(context.Background(), agent.StopContext{})
	if err != nil || !cont {
		t.Fatalf("exit 2 must continue, got cont=%v err=%v", cont, err)
	}
	if len(steered) != 1 || !strings.Contains(steered[0], "keep going") {
		t.Fatalf("stderr must be steered to the model, got %v", steered)
	}

	cont, err = decider(context.Background(), agent.StopContext{})
	if err != nil || cont {
		t.Fatalf("exit 0 must stop, got cont=%v err=%v", cont, err)
	}
}

// 未启用/未配置时是直通(允许停止)。
func TestShellOnStopDisabled(t *testing.T) {
	decider := ShellOnStop(config.ShellHookConfig{}, "sess", nil)
	cont, err := decider(context.Background(), agent.StopContext{})
	if err != nil || cont {
		t.Fatalf("disabled hook must stop (pass-through), got cont=%v err=%v", cont, err)
	}
}
```

注意:Windows 上测试环境有 sh(Git Bash);若 CI 无 sh,参照 `shell_test.go` 现有测试的处理方式(跳过或写 .bat 变体)。

Run: `go test ./pkg/agent/hooks -run TestShellOnStop 2>&1 | head -3`
Expected: FAIL(编译错误 `undefined: ShellOnStop`)

- [ ] **Step 2: 实现 ShellOnStop**

`pkg/agent/hooks/shell.go`:`shellHookInput` 加字段 `StopReason string \`json:"stop_reason,omitempty\``,追加:

```go
// ShellOnStop returns a ContinuationDecider that runs a shell command when
// the agent is about to stop (Claude Code Stop hook semantics):
//
//	exit 0 — allow the stop
//	exit 2 — block the stop: the loop continues and stderr is fed back to
//	         the model via the steer callback
//
// A disabled or unconfigured hook passes through as "allow the stop"
// (cont=false), matching the empty-chain default.
func ShellOnStop(cfg config.ShellHookConfig, sessionID string, steer func(reason string)) agent.ContinuationDecider {
	return func(ctx context.Context, stop agent.StopContext) (bool, error) {
		if !cfg.Enabled || cfg.Command == "" {
			return false, nil
		}
		res, err := runShellHook(ctx, cfg, shellHookInput{
			HookEvent:  "on_stop",
			StopReason: string(stop.Reason),
			SessionID:  sessionID,
		})
		if err != nil {
			return false, err
		}
		if res == nil || !res.Block {
			return false, nil // exit 0:允许停止
		}
		if steer != nil {
			steer(res.Reason)
		}
		return true, nil // exit 2:续跑
	}
}
```

注意 `runShellHook` 返回的 `*agent.BeforeToolCallResult`(Block/Reason)正好复用。

- [ ] **Step 3: from_config + cmd 装配**

`pkg/agent/hooks/from_config.go` 加:

```go
// OnStopFromConfig builds a ContinuationDecider from the on_stop shell hook.
func OnStopFromConfig(cfg config.HookConfig, sessionID string, steer func(string)) agent.ContinuationDecider {
	return ShellOnStop(cfg.OnStop, sessionID, steer)
}
```

`cmd/lcoder/wiring.go`(`makeBeforeToolCall` 附近)加:

```go
// makeOnStopDecider wires the on_stop shell hook into the continuation chain.
// The steer callback is bound to the agent after construction via the pointer.
func makeOnStopDecider(hookCfg config.HookConfig, sessionID string, ag **agent.Agent) agent.ContinuationDecider {
	return hooks.OnStopFromConfig(hookCfg, sessionID, func(reason string) {
		if *ag != nil {
			(*ag).Steer(models.UserMessage("[on_stop hook] " + reason))
		}
	})
}
```

`cmd/lcoder/main.go`:agent 构造前声明 `var ag *agent.Agent`,`cfg.ContinuationDeciders = append(cfg.ContinuationDeciders, makeOnStopDecider(cfg.Hooks, sess.ID, &ag))`,构造后 `ag = <new agent>`。(若 main.go 的 agent 是通过 builder/agentsetup 构造,按其现有构造点形态调整,保证 decider 闭包在首次 Prompt 前完成绑定。)

- [ ] **Step 4: 测试 + 全量回归**

Run: `go test ./pkg/agent/hooks -run TestShellOnStop -v 2>&1 | tail -4` → PASS
Run: `go build ./... && go test $(go list ./... | grep -v 'reference/Shannon' | grep -v pkg/skills) -timeout 300s 2>&1 | grep -vE '^(ok|\?)' | head -3`
Expected: 无失败

- [ ] **Step 5: Commit**

```bash
git add pkg/agent/hooks/ cmd/lcoder/
git commit -m "feat(hooks): wire on_stop shell hook as continuation decider

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: shell `before_compact` → SummarizeFunc

**Files:**
- Modify: `pkg/agent/hooks/shell.go`(stdout 捕获 + ShellBeforeCompact)
- Modify: `pkg/agentsetup/setup.go`(包装内建 summarizer)
- Test: `pkg/agent/hooks/shell_test.go`(追加)

- [ ] **Step 1: 写失败测试**

```go
// exit 0 且 stdout 非空 → stdout 作为摘要;否则回退内建 summarizer。
func TestShellBeforeCompactUsesStdout(t *testing.T) {
	tmp := t.TempDir()
	script := filepath.Join(tmp, "sum.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncat >/dev/null\necho 'hook summary'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.ShellHookConfig{Enabled: true, Command: "sh " + script, Timeout: 5}

	fallbackCalls := 0
	fallback := func(context.Context, []models.AgentMessage, string) (string, error) {
		fallbackCalls++
		return "fallback summary", nil
	}
	sum := ShellBeforeCompact(cfg, "sess", fallback)
	got, err := sum(context.Background(), []models.AgentMessage{models.UserMessage("hello")}, "")
	if err != nil || got != "hook summary" {
		t.Fatalf("got %q, %v; want hook summary", got, err)
	}
	if fallbackCalls != 0 {
		t.Fatal("fallback must not run when the hook succeeds")
	}
}

func TestShellBeforeCompactFallsBack(t *testing.T) {
	cfg := config.ShellHookConfig{Enabled: false}
	fallback := func(context.Context, []models.AgentMessage, string) (string, error) {
		return "fallback summary", nil
	}
	sum := ShellBeforeCompact(cfg, "sess", fallback)
	got, _ := sum(context.Background(), []models.AgentMessage{models.UserMessage("hello")}, "")
	if got != "fallback summary" {
		t.Fatalf("got %q, want fallback", got)
	}
}
```

Run: `go test ./pkg/agent/hooks -run TestShellBeforeCompact 2>&1 | head -3`
Expected: FAIL(编译错误 `undefined: ShellBeforeCompact`)

- [ ] **Step 2: 实现**

`pkg/agent/hooks/shell.go`:`shellHookInput` 加 `Conversation string \`json:"conversation,omitempty\``。`runShellHook` 需要 stdout——加底层函数并让原函数包装它:

```go
// runShellHookCapture is runShellHook plus stdout capture (before_compact
// returns its summary on stdout).
func runShellHookCapture(ctx context.Context, cfg config.ShellHookConfig, input shellHookInput) (stdout string, res *agent.BeforeToolCallResult, err error) {
	// 把 runShellHook 的函数体移入此处,return 处带出 stdout.String()
}

func runShellHook(ctx context.Context, cfg config.ShellHookConfig, input shellHookInput) (*agent.BeforeToolCallResult, error) {
	_, res, err := runShellHookCapture(ctx, cfg, input)
	return res, err
}
```

`from_config.go` 或 `shell.go` 加(放 shell.go,imports 加 compaction/contextmgr):

```go
// ShellBeforeCompact wraps the built-in summarizer: when the hook succeeds
// (exit 0, non-empty stdout), stdout replaces the LLM summary. The hook
// receives the serialized conversation (prior summary prepended) on stdin,
// mirroring the extension runtime's session_before_compact payload.
func ShellBeforeCompact(cfg config.ShellHookConfig, sessionID string, fallback contextmgr.SummarizeFunc) contextmgr.SummarizeFunc {
	return func(ctx context.Context, messages []models.AgentMessage, prior string) (string, error) {
		if !cfg.Enabled || cfg.Command == "" {
			return fallback(ctx, messages, prior)
		}
		conversation := compaction.SerializeConversation(messages, 2000)
		if p := strings.TrimSpace(prior); p != "" {
			conversation = "<previous_summary>\n" + p + "\n</previous_summary>\n\n" + conversation
		}
		stdout, _, err := runShellHookCapture(ctx, cfg, shellHookInput{
			HookEvent:    "before_compact",
			Conversation: conversation,
			SessionID:   sessionID,
		})
		if err == nil && strings.TrimSpace(stdout) != "" {
			return strings.TrimSpace(stdout), nil
		}
		return fallback(ctx, messages, prior)
	}
}
```

(imports 需要 `strings`、`github.com/lcoder/lcoder/pkg/compaction`、`github.com/lcoder/lcoder/pkg/contextmgr`。)

- [ ] **Step 3: agentsetup 装配**

`pkg/agentsetup/setup.go` 的 `NewContextManager`,把 `WithSummarizer(...)` 一行改为:

```go
	base := contextmgr.SummarizeFunc(compaction.NewCircuitBreaker(0).Wrap(compaction.NewLLMSummarizer(llmClient, models.ModelRef{Provider: cfg.Provider, ID: cfg.Model})))
	summarizer := hooks.ShellBeforeCompact(cfg.Hooks.BeforeCompact, sessionID, base)
	// ...
	contextmgr.WithSummarizer(summarizer),
```

`NewContextManager` 目前没有 sessionID 参数——核查签名;若无,加参或从 tmplCtx 取(实施时以调用点最少改动为准;`cmd/lcoder/main.go` 与 `test/integration` 的调用点同步更新)。

- [ ] **Step 4: 测试 + 回归**

Run: `go test ./pkg/agent/hooks ./pkg/agentsetup -count=1 2>&1 | tail -3` → PASS
Run: `go build ./... && go test $(go list ./... | grep -v 'reference/Shannon' | grep -v pkg/skills) -timeout 300s 2>&1 | grep -vE '^(ok|\?)' | head -3`

- [ ] **Step 5: Commit**

```bash
git add pkg/agent/hooks/ pkg/agentsetup/ cmd/lcoder/
git commit -m "feat(hooks): wire before_compact shell hook as summarizer wrapper

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: 退役 JSON 描述符 HTTP 工具(tool_extensions)

**Files:**
- Delete: `pkg/tools/extensions.go`、`pkg/tools/extensions_test.go`
- Modify: `pkg/config/config.go`(删 `ToolExtensions` 字段)、`pkg/config/hooks.go`(删 `ToolExtensionConfig`)、`pkg/config/config_validate.go`(删相关校验)、`pkg/config/config_test.go`
- Modify: `cmd/lcoder/main.go`(删 `registry.LoadExtensions` 调用)
- Modify: `configs/lcoder.yaml`(删 tool_extensions 示例注释,补 MCP 指引)
- Modify: `docs/hook-extension-comparison.md`(标记已执行)

范围注意:`http_tools`(`config.HTTPToolConfig` → `tools.NewHTTPExecutable`)**不在**本次范围——它是内联 YAML 工具声明,与 JSON 描述符是两条路;`pkg/tools/http.go` 保留。

- [ ] **Step 1: 删除与编译清理**

按上列文件删除。`registry.LoadExtensions` 方法(在 `pkg/tools/extensions.go` 中)随文件删除;`cmd/lcoder/main.go:193` 的调用删除。

Run: `grep -rn "ToolExtensions\|LoadExtensions\|ToolExtensionConfig" --include="*.go" pkg/ cmd/ test/ | grep -v _test` → 空
Run: `go build ./... && go vet $(go list ./... | grep -v 'reference/Shannon') 2>&1 | head -10`
测试文件的引用同步删除(config_test.go 中 ToolExtensions 解析用例等),直到 vet 干净。

- [ ] **Step 2: configs/lcoder.yaml**

删 `tool_extensions` 段(若存在),在 MCP 段注释补一行:`# 外部工具请使用 mcp_servers;http_tools 仅限简单 GET/POST 端点`。

- [ ] **Step 3: 全量测试**

Run: `go test $(go list ./... | grep -v 'reference/Shannon' | grep -v pkg/skills) -timeout 300s 2>&1 | grep -vE '^(ok|\?)' | head -3`
Expected: 无失败

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "refactor: retire JSON-descriptor tool extensions in favor of MCP

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4: extension `stop` + `session_start` hooks

**Files:**
- Modify: `pkg/extension/proto/proto.go`(方法/能力/参数类型)
- Modify: `pkg/extension/runtime/host.go`(RunStopHooks、RunSessionStartHooks)
- Modify: `pkg/extension/bridge/bridge.go`(StopDecider、SessionStart)
- Modify: `cmd/lcoder/main.go`(装配 decider;agent 构造后触发 session_start)
- Test: `pkg/extension/proto/proto_test.go` 或 bridge 层测试(视 runtime 可测性)

- [ ] **Step 1: proto 扩展**

`pkg/extension/proto/proto.go` 加:

```go
	MethodHookStop         = "hook/stop"
	MethodHookSessionStart = "hook/session_start"

	HookStop         = "stop"
	HookSessionStart = "session_start"

// StopParams asks an extension whether the run may stop.
type StopParams struct {
	Reason string `json:"reason"`
	Turn   int    `json:"turn"`
}

// StopResult says continue=true to block the stop; Reason is fed back to
// the model (Claude Code Stop hook semantics).
type StopResult struct {
	Continue bool   `json:"continue"`
	Reason   string `json:"reason,omitempty"`
}

// SessionStartParams fires once after the agent and session are ready.
type SessionStartParams struct {
	SessionID string `json:"session_id"`
	Resumed   bool   `json:"resumed"`
}

// SessionStartResult carries context to inject at session start.
type SessionStartResult struct {
	Context string `json:"context,omitempty"`
}
```

(manifest 的 caps 声明机制跟随现有 `HookBeforeCompact` 的注册方式——若 caps 来自 manifest JSON 自由文本,则只需常量。)

- [ ] **Step 2: host 方法**

`pkg/extension/runtime/host.go` 仿 `RunBeforeCompactHook`:

```go
// RunStopHooks asks the first declaring extension whether the run may stop.
// No declarer (or hook error) means allow the stop.
func (h *Host) RunStopHooks(ctx context.Context, reason string, turn int) (cont bool, msg string) {
	// 遍历扩展,hasString(ext.caps.Hooks, proto.HookStop) → call MethodHookStop
	// 返回 out.Continue, out.Reason
}

// RunSessionStartHooks fires session_start on all declaring extensions and
// concatenates their context payloads.
func (h *Host) RunSessionStartHooks(ctx context.Context, sessionID string, resumed bool) string
```

- [ ] **Step 3: bridge 方法**

`pkg/extension/bridge/bridge.go` 加:

```go
// StopDecider adapts the stop hook chain to agent.ContinuationDecider.
// continue=true from the extension blocks the stop; Reason is returned via
// the steer callback so the caller can feed it to the model.
func (b *Bridge) StopDecider(steer func(string)) agent.ContinuationDecider {
	return func(ctx context.Context, stop agent.StopContext) (bool, error) {
		cont, msg := b.host.RunStopHooks(ctx, string(stop.Reason), stop.Turn)
		if cont && msg != "" && steer != nil {
			steer(msg)
		}
		return cont, nil
	}
}

// SessionStart runs session_start hooks and returns the combined context.
func (b *Bridge) SessionStart(ctx context.Context, sessionID string, resumed bool) string {
	return b.host.RunSessionStartHooks(ctx, sessionID, resumed)
}
```

- [ ] **Step 4: cmd 装配**

`cmd/lcoder/main.go`:
- `extBridge != nil` 时:`cfg.ContinuationDeciders = append(cfg.ContinuationDeciders, extBridge.StopDecider(steer))`(steer 闭包同 Task 1 的指针绑定法)。
- agent 构造完成后:`if ctx := extBridge.SessionStart(ctx, sess.ID, resumed); ctx != "" { ag.Steer(models.UserMessage("[session_start hook] " + ctx)) }`(`resumed` 取本次是否 restore 了 checkpoint/session)。

- [ ] **Step 5: 测试 + 回归**

测试策略跟随 `pkg/extension` 现有 7 个测试文件的形态(若现有测试用 fake conn/manifest,按同构写 stop/session_start 用例;若 host 不可 fake,则测试 proto 常量与 bridge 映射逻辑,host 方法靠编译与现有集成形态保障)。

Run: `go test ./pkg/extension/... -count=1 2>&1 | tail -3` → PASS
Run: `go build ./... && go test $(go list ./... | grep -v 'reference/Shannon' | grep -v pkg/skills) -timeout 300s 2>&1 | grep -vE '^(ok|\?)' | head -3`

- [ ] **Step 6: Commit**

```bash
git add pkg/extension/ cmd/lcoder/
git commit -m "feat(extension): add stop and session_start hooks

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 5: 权限引擎 hook 插槽(extension 作为 guard policy)

**Files:**
- Modify: `pkg/extension/proto/proto.go`(permission 方法/类型)
- Modify: `pkg/extension/runtime/host.go`(RunPermissionHooks)
- Modify: `pkg/extension/bridge/bridge.go`(PermissionPolicy)
- Modify: `pkg/agent/loop.go`(Config.ExtraGuardPolicies)、`pkg/agent/executor.go`(installGuardPolicies 追加)
- Modify: `cmd/lcoder/main.go`(装配)
- Test: `pkg/agent/executor_permission_hook_test.go`(新)

- [ ] **Step 1: 写失败测试**

```go
package agent

// 扩展 guard policy 参与权限判定:deny 生效,ask 触发确认,无意见放行。
func TestExtraGuardPolicyDenies(t *testing.T) {
	echo := &echoTool{}
	reg := tools.NewRegistry(t.TempDir())
	reg.Register("echo", echo)

	toolMsg := models.NewAgentMessage(models.RoleAssistant, models.ToolCallContent{
		Type: "tool_call", ID: "call_1", Name: "echo", Arguments: map[string]any{"command": "x"},
	})
	client := llmtest.Client(llmtest.Turn(llmtest.Done(toolMsg, nil)))

	denyAll := policyFunc(func(permissions.Request) (permissions.Decision, string, bool) {
		return permissions.Deny, "denied by extension", true
	})
	ag := New(Config{
		SystemPrompt: "x",
		Model:        models.ModelRef{Provider: "openai", ID: "gpt-4o-mini"},
		ExtraGuardPolicies: []permissions.Policy{
			policyFunc(denyAll),
		},
		ShouldStop: func(context.Context, TurnSummary) (bool, error) { return true, nil },
	}, client, reg, permissions.NewEngine(permissions.DefaultConfig()), events.New())

	if err := ag.Prompt(context.Background(), models.UserMessage("go")); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if echo.gotArgs != nil {
		t.Fatal("denied tool must not execute")
	}
}

type policyFunc func(permissions.Request) (permissions.Decision, string, bool)

func (f policyFunc) Name() string { return "test-extension" }
func (f policyFunc) Decide(r permissions.Request) (permissions.Decision, string, bool) {
	return f(r)
}
```

Run: `go test ./pkg/agent -run TestExtraGuardPolicyDenies 2>&1 | head -3`
Expected: FAIL(编译错误 `unknown field ExtraGuardPolicies`)

- [ ] **Step 2: Config + executor**

`pkg/agent/loop.go` Config 加:

```go
	// ExtraGuardPolicies are appended after the built-in mode/skill guard
	// policies (and still ahead of user rules). Extension permission hooks
	// plug in here (opencode's permission.ask equivalent).
	ExtraGuardPolicies []permissions.Policy
```

`pkg/agent/executor.go` 的 `installGuardPolicies`:

```go
	e.permissions.SetGuardPolicies(append([]permissions.Policy{
		modeGuardPolicy{ex: e}, skillGuardPolicy{ex: e}, modeTransitionPolicy{ex: e},
	}, e.cfg.ExtraGuardPolicies...)...)
```

(注意保持现有三个内置 policy 的相对顺序在最前。)

- [ ] **Step 3: proto + host + bridge**

proto 加:

```go
	MethodHookPermission = "hook/permission"
	HookPermission       = "permission"

type PermissionParams struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args"`
}

// PermissionResult: decision 为 allow/deny/ask 时有意见;空 = 放行给下一 policy。
type PermissionResult struct {
	Decision string `json:"decision,omitempty"`
	Reason   string `json:"reason,omitempty"`
}
```

host 加 `RunPermissionHooks(ctx, tool, args) (decision, reason string)`,bridge 加:

```go
// PermissionPolicy adapts the permission hook chain to permissions.Policy.
func (b *Bridge) PermissionPolicy() permissions.Policy { return &permissionPolicy{host: b.host} }

type permissionPolicy struct{ host *runtime.Host }

func (p *permissionPolicy) Name() string { return "extension" }

func (p *permissionPolicy) Decide(req permissions.Request) (permissions.Decision, string, bool) {
	decision, reason := p.host.RunPermissionHooks(context.Background(), req.Tool, req.Args)
	switch decision {
	case "allow":
		return permissions.Allow, reason, true
	case "deny":
		return permissions.Deny, reason, true
	case "ask":
		return permissions.Ask, reason, true
	}
	return "", "", false
}
```

(若 Policy.Decide 需要 ctx,按其真实签名调整。)

- [ ] **Step 4: cmd 装配**

`cmd/lcoder/main.go`:`extBridge != nil` 时 `cfg.ExtraGuardPolicies = append(cfg.ExtraGuardPolicies, extBridge.PermissionPolicy())`。

- [ ] **Step 5: 测试 + 回归 + race**

Run: `go test ./pkg/agent ./pkg/extension/... -count=1 -timeout 180s 2>&1 | tail -3` → PASS
Run: `go build ./... && go test $(go list ./... | grep -v 'reference/Shannon' | grep -v pkg/skills) -timeout 300s 2>&1 | grep -vE '^(ok|\?)' | head -3`
Run: `CGO_ENABLED=1 go test ./pkg/agent -race -count=1 -timeout 180s 2>&1 | tail -2`

- [ ] **Step 6: Commit**

```bash
git add pkg/agent/ pkg/extension/ cmd/lcoder/
git commit -m "feat(permissions): extension hook slot via ExtraGuardPolicies

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 6: 文档与收尾

**Files:**
- Modify: `docs/hook-extension-comparison.md`

- [ ] **Step 1: 更新对比文档**

建议表标注四项已实施;3.1 死配置段落注明已接线;4.1 注明已退役。

- [ ] **Step 2: 最终全量验证**

Run: `go build ./... && go vet $(go list ./... | grep -v 'reference/Shannon') && go test $(go list ./... | grep -v 'reference/Shannon' | grep -v pkg/skills) -count=1 -timeout 300s 2>&1 | grep -vE '^(ok|\?)' | head -3`
Run: `CGO_ENABLED=1 go test ./pkg/agent ./pkg/agent/hooks -race -count=1 -timeout 180s 2>&1 | tail -2`
Expected: 全绿

- [ ] **Step 3: Commit**

```bash
git add docs/hook-extension-comparison.md
git commit -m "docs: mark hook improvements as implemented

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## Self-Review 记录

- **Spec 覆盖**:对比文档 ★★★ 两项(Task 1+2 死配置接线、Task 3 退役)与 ★★ 两项(Task 4 stop/session_start、Task 5 权限插槽)均有任务;★ 级两项按用户决定不在本轮。
- **类型一致性**:`ShellOnStop`/`StopDecider` 返回 `agent.ContinuationDecider`(goal 模式已定义);`runShellHookCapture` 是 `runShellHook` 的 stdout 扩展,原签名保留包装;`permissions.Policy` 复用现有接口;`ExtraGuardPolicies` 在 Config 定义、executor 消费。
- **风险点**:(1) Task 1/4 的 steer 闭包指针绑定依赖 main.go 的 agent 构造顺序,实施时以"首次 Prompt 前完成绑定"验证;(2) Task 2 的 `NewContextManager` 可能缺 sessionID 参数,需加参并同步所有调用点(含 integration 测试——注意该包有既有编译问题,不要扩大战果);(3) shell hook 测试依赖 Windows 上的 sh(Git Bash),与现有 shell_test.go 同假设。
