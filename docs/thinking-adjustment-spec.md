# Spec: TUI 运行时调节 LLM 思考强度 + 持久化

> 本文描述在 TUI 内运行时调节 LLM 思考强度（`off/on/low/medium/high`）的设计方案。
> 目标：会话级即时生效 + 可选持久化到 `config.yaml`，无需改配置重启。
> 参考：kimi-code 的 `/model <alias> [effort]` 双态设计（`onSelect` 持久化 / `onSessionOnlySelect` 仅会话）。

---

## 1. 需求理解

用户需要在 TUI 中运行时调节 LLM 思考强度，覆盖三个诉求：

1. **会话级即时生效**：`/thinking low` 下一 turn 生效，不重启。
2. **持久化**：显式 `--persist` 写 `config.yaml`，下次启动保持。
3. **模型感知**：档位受模型 `efforts` 声明约束；AlwaysThinking 模型不可关；未知档位有清晰提示。

## 2. 关键发现（现状）

### 2.1 thinking 当前仅启动时一次性解析

```
config.yaml: thinking: "low"                    ← 唯一入口 (config.go:82)
启动 main.go:310:
  llmClient.ResolveThinking(prov, model, cfg.Thinking)   ← 校验一次
  → contextmgr.WithThinking(thinking)           ← 只写一次 (manager.go:181)
每 turn: BuildTurnRequest → TurnRequest.Thinking = m.thinking   ← 只读
```

**缺口**：
- `contextmgr.Manager` 有 `SetBudget()`（运行时改预算）但**无 `SetThinking()`**
- `Agent.Runner` 接口无 thinking 相关方法
- TUI 无 thinking 调节入口

### 2.2 可复用的现有机制

| 机制 | 位置 | 用途 |
|---|---|---|
| `ResolveThinking(prov, model, want)` | `pkg/llm/engine/engine.go:183` | 档位校验 + AlwaysThinking 保护，**落盘前必须经过它** |
| `SaveProviderSelection` | `pkg/config/credentials.go:116` | config.yaml 读-改-写模板（0600 权限），`SaveThinking` 仿照 |
| `WithMode` 克隆保留 thinking | `pkg/contextmgr/manager.go:505` | 模式切换不丢运行时 thinking ✅ |
| slash 命令注册 | `pkg/tui/slash_registry.go:init()` | `/thinking` 入口 |
| 状态栏插槽 | `pkg/tui/view.go:statusLineView` | 显示当前档位 |
| `m.llmClient` | `pkg/tui/model.go` / `providerpanel.go` | TUI 已持有 client，可调 `ResolveThinking` |

### 2.3 thinking 取值语义（`engine.go:183-215` ResolveThinking）

| 值 | 含义 | 映射 |
|---|---|---|
| `""`（未配置） | **主动选默认档位**（kimi-code defaultThinkingEffortFor）：effort 型模型取 efforts 中间档；toggle 型（无 efforts）→ `on`；未知模型 → 不发送，交 provider 默认 | openai: `reasoning_effort=中间档` / anthropic: 默认关 |
| `"off"` | 关闭（AlwaysThinking 模型拒绝） | openai: 不发 / anthropic: `disabled` |
| `"on"` | 开启，默认强度 | anthropic: budget=maxTok/2 |
| `low/medium/high` | 模型声明档位（efforts） | openai/responses: `reasoning_effort` |

### 2.4 kimi-code 参考（已调查）

- `thinking.ts` 四级解析：显式请求 > enabled=false → off > config.effort > 模型默认；**always_thinking 永不 resolve 到 off**
- `performModelSwitch(host, alias, effort, persist)` 双态：`persist=true` 写会话持久化、`false` 仅本次
- **缓存警告**：`EFFORT_SWITCH_CACHE_WARNING`——有对话历史时提示"切换 effort 会使 prompt cache 失效，长会话重计费"

## 3. 实现方案

### Step 1：contextmgr 加 `SetThinking`（底层，纯加法）

**文件**：`pkg/contextmgr/manager.go`，仿 `SetBudget`（manager.go:209）：

```go
// SetThinking replaces the resolved thinking value carried on turn requests.
// The value must already be validated by engine.ResolveThinking.
func (m *Manager) SetThinking(v string) { m.thinking = v }

// Thinking returns the current resolved thinking value ("" = send nothing).
func (m *Manager) Thinking() string { return m.thinking }
```

### Step 2：Agent 加 `SwitchThinking` / `Thinking`（穿透 Runner 接口）

**文件**：`pkg/agent/loop.go` + `pkg/agent/runner.go`

```go
// Runner 接口新增：
SwitchThinking(thinking string)
Thinking() string   // 读当前档位，状态栏用

// loop.go 实现：
func (a *Agent) SwitchThinking(thinking string) {
    if a.mgr != nil { a.mgr.SetThinking(thinking) }
}
func (a *Agent) Thinking() string {
    if a.mgr != nil { return a.mgr.Thinking() }
    return ""
}
```

### Step 3：`SaveThinking` 持久化（复用 SaveProviderSelection 模式）

**文件**：`pkg/config/credentials.go`

```go
// SaveThinking persists the resolved thinking value to config.yaml,
// preserving every other key. The value must be post-ResolveThinking
// (never a raw user input that a model rejects).
func SaveThinking(thinking string) error { /* 读-改 raw["thinking"]-写 同 SaveProviderSelection */ }
```

### Step 4：`/thinking` 命令 + 横向分段选择器（核心交互）

**文件**：`pkg/tui/slash_registry.go` + `pkg/tui/effort_selector.go`（新建组件）+ `pkg/tui/keys.go`（键盘路由）

双入口设计，对齐 kimi-code 的 EffortSelectorComponent：

```
/thinking                  → 弹横向分段选择器（当前值高亮，←/→ 切换，Enter 持久化，Alt+S 仅会话，Esc 取消）
/thinking low              → 快捷命令：会话级生效（SwitchThinking）
/thinking low --persist    → 快捷命令：会话级 + SaveThinking 写 config.yaml
/thinking off              → 快捷命令：关闭（AlwaysThinking 模型被 ResolveThinking 拒绝并提示）
```

#### 4.1 新组件 effortSelector（`pkg/tui/effort_selector.go`）

仿 kimi-code EffortSelectorComponent，渲染进现有 `bottomRegion()` 的 cmdPanel 插槽（零布局改动）：

```go
type effortSelector struct {
    efforts     []string // 模型支持的档位（含 off）
    activeIndex int
    current     string   // 当前生效档位（高亮）
    warning     string   // 缓存失效警告（有对话历史时设置）
}

// ←/→ 步进、Enter 持久化提交、Alt+S 仅会话、Esc 取消
// 渲染：一行 segments，激活的用 [ low ] 高亮（对标 kimi-code）
```

渲染形态：

```
Select thinking effort
←→ switch · Enter persist · Alt+S session-only · Esc cancel

  off    low  [ medium ]  high

Note: switching effort may invalidate the prompt cache.
```

#### 4.2 键盘路由（`pkg/tui/keys.go` `handleInputKey`）

在 cmdPanel 分支旁新增 `effortSelector` 分支：`←/→` 步进、`Enter` 提交、`Alt+S` 仅会话、`Esc` 取消。与现有 cmdPanel 路由模式（keys.go:280-310）同构。

#### 4.3 Handler 逻辑（`pkg/tui/commands.go`）

1. **空参数** → 读 `llmClient.ResolveThinking` 的 efforts，构建 `effortSelector` 弹选择器
2. **有参数** → `llmClient.ResolveThinking(provider, model, arg)` 校验：
   - `resolved` 非空 → `m.agent.SwitchThinking(resolved)`
   - `--persist` → `config.SaveThinking(resolved)`
   - `warning` 非空 → 一并显示（如"模型 X 的 thinking 不可关闭"）
   - **缓存警告**：`m.completedTurns > 0` 时提示"切换思考强度可能使 prompt cache 失效"（对标 kimi-code 的 EFFORT_SWITCH_CACHE_WARNING）

选择器提交路径复用相同校验：`Enter`（持久化）→ SwitchThinking + SaveThinking；`Alt+S`（会话级）→ 仅 SwitchThinking。

### Step 5：状态栏显示当前档位（信息可见）

**文件**：`pkg/tui/view.go` `modeLabel()`

```go
if t := m.agent.Thinking(); t != "" { label += " · think:" + t }
```

顶栏/状态栏已有插槽，零布局改动。

### Step 6（可选，后续迭代）：provider 面板集成

`providerpanel.go` 在 `provStepKey` 后加 `provStepThinking`：选完模型后选档位（读该模型 efforts），选中后 `SwitchThinking` + 持久化。改动面大（分步/键盘路由/持久化），**不在本 spec 首版**。

## 4. 风险与注意事项

| 风险 | 说明 | 缓解 |
|---|---|---|
| **持久化值未校验** | 写入模型不支持的档位（如 AlwaysThinking 的 off），下次启动被拒回退 on | **落盘值必须经过 `ResolveThinking`**；spec 强制 |
| **缓存失效** | thinking 参数位置可能影响 provider 缓存前缀 | 有对话历史时弹警告（kimi-code 同款）；`--persist` 更应提示 |
| **WithMode 克隆** | 模式切换丢 thinking？ | 已验证保留（manager.go:505）✅ |
| **子 agent** | `buildChild` 用全新 contextmgr，不继承父 thinking | 本 spec 不做；后续若需在 `HostConfig` 透传 |
| **运行时切换中** | 流式输出中改档位 | 下一 turn 生效（thinking 在 BuildTurnRequest 读），当前流不受影响——**天然安全** |
| **配置覆盖冲突** | TUI 向导 `SaveProviderSelection` 与 `SaveThinking` 并发写 | 都是全量读-改-写，同文件串行调用，风险低 |

## 5. 测试计划

1. `contextmgr`：`SetThinking`/`Thinking` 读写
2. `agent`：`SwitchThinking` 穿透 manager；`WithMode` 克隆保留新值
3. `config`：`SaveThinking` 写入 config.yaml 且保留其他键（仿 SaveProviderSelection 测试）
4. `tui`：`/thinking` 无参显示、有参会话级、`--persist` 写盘、AlwaysThinking 拒绝
5. 状态栏：`think:low` 显示

## 6. 验证

- `go build ./...`
- `go test ./pkg/contextmgr/ ./pkg/agent/ ./pkg/config/ ./pkg/tui/`

---

**一句话总结**：复用 lcoder 已有的三块基础设施（`ResolveThinking` 校验、`SaveProviderSelection` 写盘模式、slash 命令/状态栏插槽），加一个底层 setter（Step 1-2）+ 一个命令（Step 4）即可实现会话级与持久化双态调节，全程零布局改动，对标 kimi-code 的双态设计。
