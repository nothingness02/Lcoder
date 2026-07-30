# TUI 与 Kimi Code 的差距分析

## 1. 渲染性能：每 delta 全量重建 vs 组件级增量

### Lcoder

```go
// events.go — 每个流式 delta 都重建整个 viewport
func (m *Model) patchAssistant(content string) {
    m.blocks[i].raw = rendered
    m.components[i] = toComponent(m.blocks[i])
    m.rebuildViewport()  // ← 每次 delta：重算所有 block 布局 + 生成全部 ANSI
}
```

2000 token 响应 ≈ 50 个 delta → 50 次全局重建。每个 delta 只改最后一行文本，却遍历了全部 block。

### Kimi Code (pi-tui)

组件级脏标记——每个组件跟踪自己是否需要重绘。文本增量只触发了当前 `AssistantMessage` 组件的局部刷新，不影响滚动区域内的其他消息。

### 修复方向

- 流式 delta 只更新最后一个 `BlockAssistant`，调用 `components.Update()` 而非 `rebuildViewport()`
- 或加 50ms debounce：多个 delta 合并为一次重建

---

## 2. 消息组件丰富度

| 场景 | Lcoder | Kimi Code |
|------|--------|-----------|
| 助手消息 | `BlockAssistant`（纯文本） | `AssistantMessage`（Markdown 渲染 + thinking 折叠） |
| 工具调用 | `BlockTool`（展开/折叠） | `ToolCall`（语法高亮参数 + 内联结果） |
| 计划模式 | 无特殊渲染 | `PlanBox`（结构化方案展示） |
| Goal | 无 | `GoalPanel` + `GoalMarkers` |
| 子 Agent | 简单进度文本 | `AgentSwarmProgress`（每个子 agent 卡片 + 状态） |
| 思考过程 | 无 | `Thinking`（可折叠推理面板） |
| Token 用量 | 状态栏小字 | `UsagePanel`（独立面板） |
| Bash 执行 | 无特殊渲染 | `ShellExecution`（命令 + 输出 + 耗时） |
| 技能激活 | 无 | `SkillActivation` |
| 只读组 | 无 | `ReadGroup`（合并多个 read 为一个折叠组） |

**根因**：Lcoder 的 block 系统只有 `raw string`，没有结构化渲染。Kimi Code 每种消息类型有独立组件，带样式、折叠、进度条。

---

## 3. 状态栏与头部

### Lcoder

```
[plan] · context: 42% (86.5k/200k) · claude-sonnet-4-5 · $0.12
```

一行文本，左对齐模式名，右对齐上下文统计。

### Kimi Code

```
┌─ Kimi Code · plan · claude-sonnet-4-5 ── session: 3 ── turn: 12 ── 22K ─┐
│                                                                          │
```

顶部有完整边框包裹的表头：产品名、模式、模型、会话编号、轮次、上下文使用。底部有独立状态区显示子 agent 进度。

### 差距

- Lcoder 没有顶部标题栏——用户不知道当前会话是哪个
- Lcoder 模式标签过于简单（只是 `plan` 一个词）
- Kimi Code 的全宽标题栏给人一种"面板"感，Lcoder 的纯文本状态行更像日志输出

---

## 4. 输入区域

### Lcoder

```
┌──────────────────────┐
│ › Type a message…    │  ← charmbracket/textarea，单色边框
└──────────────────────┘
```

- 单色边框，进入处理态时变灰
- 无语法高亮
- 无自动补全 UI（@file 补全在下拉菜单，输入框内不变）

### Kimi Code (pi-tui Editor)

```
┌──────────────────────────────────┐
│ @main.go @handler.go 改一下这个接口  │  ← @mention 在输入框中高亮
│                                    │  ← 多行自动扩展
│ █                                  │  ← 光标闪烁动画
└──────────────────────────────────┘
```

- 自定义编辑器组件，非标准 textarea
- @mention 在输入框内高亮渲染
- 斜杠命令即时补全
- 多行自动扩展 + 滚动
- 粘贴保护（大段粘贴折叠为 placeholder）

### 差距

- Lcoder 的 textarea 是标准 Bubble Tea 组件，功能有限
- Kimi Code 的 Editor 是 pi-tui 的核心组件，大量定制
- @mention 体验差距：Lcoder 在下拉菜单中选，Kimi Code 在输入框中高亮

---

## 5. 流式输出体验

### Lcoder

```
Lcoder: 我来修复这个 bug。先看看代码...
│ read handler.go                              [展开]
│ edit handler.go    ✓ Applied 1 edit
│ 已修复，测试通过。
```

- 文本逐字出现（正常流式体验）
- 但无节流，50 次/秒的 viewport 重建会导致光标闪烁

### Kimi Code

- 文本平滑出现（可能有字符级动画节流）
- thinking 过程可折叠（模型推理过程不占视觉空间）
- 子 agent 进度实时显示在工具块内部

---

## 6. 动画与视觉细节

| 细节 | Lcoder | Kimi Code |
|------|:---:|:---:|
| 启动动画 | ✅ brand swirl（16×16 位图渐变） | — |
| 处理中 spinner | ✅ | ✅ |
| 模式切换过渡 | ❌ | ✅ |
| 压缩指示器 | "压缩中…" | — |
| 子 agent 进度条 | ❌ | ✅ 每个 subagent 独立进度 |
| 工具执行动画 | ❌ | ✅ ShellExecution 有开始/结束标记 |

---

## 7. 无障碍与终端兼容

| 特性 | Lcoder | Kimi Code |
|------|:---:|:---:|
| 自适应亮/暗色 | ✅ `HasDarkBackground()` | ✅ |
| 256 色调色板 | ✅ 6 色语义 | ✅ |
| True Color 支持 | ✅ 品牌渐变 | ✅ |
| Windows Terminal | ⚠️ 部分问题（PowerShell） | ✅ |
| VSCode 终端 | ❌ 已知问题 3 | ✅ |
| 粘贴保护 | ✅ 大段折叠 | ✅ |
| 宽度自适应 | ✅ `tea.WindowSizeMsg` | ✅ |

---

## 优先级排序

| 优先级 | 改进项 | 预期效果 |
|:---:|------|------|
| ★★★ | 流式节流（不每次 delta 重建） | 消除闪烁，大幅提升流畅度 |
| ★★★ | @file 补全性能（加缓存/上限） | 消除卡顿 |
| ★★ | 标题栏（显示会话/模式/轮次） | 用户可感知当前状态 |
| ★★ | 工具调用内联结果（不折叠到工具块外） | 减少滚动，结果更直观 |
| ★ | thinking 折叠面板 | 减少视觉噪音 |
| ★ | 子 agent 进度卡片 | 并行任务可视化 |
| ★ | 输入框内 @mention 高亮 | 美观提升 |
