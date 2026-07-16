# TUI 组件化重构设计（Phase A）

> **Goal:** 把 `pkg/tui` 从“字符串拼接 + 全局渲染”重构为“组件化 + 块级虚拟化”，在 Bubble Tea 框架内解决 Markdown 渲染、长输出滚动、diff/工具结果展示和视觉表现力的痛点，同时为未来 Webview/GUI 预览模式预埋接口。

## 背景

当前 `pkg/tui` 的渲染层存在以下问题：

1. **Markdown / 代码块渲染不可控**：`block.render()` 直接调用 `renderMarkdownCached()`， glamour 输出的是整段 ANSI 字符串，无法对单个代码块做折叠、复制、独立高亮缓存。
2. **长输出滚动卡顿**：`rebuildViewport()` 每次把全部历史 block 重新渲染成字符串塞给 viewport，对话越长越慢。
3. **diff / 文件树 / 工具结果展示简陋**：这些场景目前靠字符串拼接，没有独立组件和交互状态。
4. **视觉层次弱**：色板、间距、圆角分散在多个文件中，缺乏统一的设计系统。

本设计参考 `reference/pi` 和 `reference/opencode` 的组件化思路，但**不引入 TypeScript 运行时**，在纯 Go + Bubble Tea 内完成第一阶段重构。

## 设计决策

- **不切换语言栈**：保留 Bubble Tea + lipgloss，避免引入 Node/Bun 运行时和 IPC 复杂度。
- **组件接口分层**：基础接口 `BlockComponent` 只负责渲染；需要局部交互的组件额外实现 `UpdatableComponent`。
- **块级虚拟化**：viewport 只渲染可见区域内的组件，基于每个组件的 `Height()` 计算可见窗口。
- **Markdown 子组件化**：把 glamour 的输出封装在 `CodeBlockNode` 等子组件内部，支持独立缓存和折叠。
- **为 Webview 预埋**：组件接口与具体渲染介质无关，未来可通过 `WebRenderer` 复用同一套组件树。

## 总体架构

```
pkg/tui/
  component.go              // BlockComponent / UpdatableComponent 接口
  components/
    user.go                 // UserComponent
    assistant.go            // AssistantComponent + MarkdownNode 子组件树
    tool_result.go          // ToolResultComponent
    system_log.go           // SystemLogComponent
  markdown/
    renderer.go             // Markdown 解析 -> MarkdownNode 树
    code_block.go           // CodeBlockNode（语法高亮、折叠、缓存）
    text.go                 // TextNode
    list.go                 // ListNode
    diff.go                 // DiffNode（预留）
  viewport.go               // 虚拟化 viewport 算法
  theme.go                  // 统一设计系统（颜色、间距、圆角）
```

## 核心接口

### BlockComponent

```go
// BlockComponent 是会话视图中一个可渲染单元。
// 它不感知主 Model 的业务状态，只负责自己的渲染尺寸和输出。
// BlockKind 是会话块类型，由内部 int 导出为可读类型，
// 便于未来 Web 渲染器等外部包识别组件类别。
type BlockKind int

const (
    BlockUser BlockKind = iota
    BlockAssistant
    BlockTool
    BlockSystem
)

type BlockComponent interface {
    ID() string
    Kind() BlockKind
    Height(width int, expanded bool) int
    Render(width int, expanded bool) string
}
```

### UpdatableComponent

```go
// UpdatableComponent 用于需要局部交互的组件。
// 简单展示组件无需实现此接口。
type UpdatableComponent interface {
    BlockComponent
    Update(msg tea.Msg) (BlockComponent, tea.Cmd)
}
```

### 消息分发封装

```go
// ComponentMsg 把 tea.Msg 路由到指定 ID 的组件。
type ComponentMsg struct {
    ID  string
    Msg tea.Msg
}
```

主 `Model.Update` 只做两件事：

1. 处理全局消息（窗口尺寸、输入、slash 命令等）。
2. 遇到 `ComponentMsg` 时，通过类型断言找到对应组件并调用其 `Update`。

```go
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case ComponentMsg:
        for i, comp := range m.components {
            if comp.ID() != msg.ID {
                continue
            }
            if upd, ok := comp.(UpdatableComponent); ok {
                newComp, cmd := upd.Update(msg.Msg)
                m.components[i] = newComp
                return m, cmd
            }
        }
    // ... 其他全局消息
    }
}
```

## 组件拆分清单

| 现有 `block.kind` | 新组件 | 职责 | 是否实现 `UpdatableComponent` |
|---|---|---|---|
| `BlockUser` | `UserComponent` | 用户消息条、@file 附件 | 否 |
| `BlockAssistant` | `AssistantComponent` | thinking 折叠、Markdown 子组件树、token/cost 脚注 | 是 |
| `BlockTool` | `ToolResultComponent` | compact/expanded 视图、耗时、错误态、工具输出折叠 | 是 |
| `BlockSystem` | `SystemLogComponent` | 系统提示行 | 否 |

### AssistantComponent 内部结构

```go
type AssistantComponent struct {
    id       string
    thinking string
    content  string          // 原始 markdown 文本
    nodes    []MarkdownNode  // 解析后的子组件树
    usage    *blockUsage
    expanded bool            // Ctrl+O 展开 thinking
}
```

`Render()` 流程：

1. 若 `expanded` 为 true，渲染完整 thinking；否则渲染单行预览。
2. 渲染 `MarkdownNode` 树（每个子节点自行处理缓存和折叠）。
3. 渲染 token/cost 脚注。

### ToolResultComponent 内部结构

```go
type ToolResultComponent struct {
    id        string
    toolName  string
    toolArgs  string
    result    string
    err       bool
    running   bool
    start     time.Time
    elapsed   time.Duration
    expanded  bool
}
```

`Render()` 根据 `expanded` 返回 compact 或 expanded 视图。

## Markdown 子组件化

把 glamour 的整段输出拆成可独立渲染的节点树：

```go
type MarkdownNode interface {
    Height(width int) int
    Render(width int) string
}
```

### 节点类型

| 节点 | 说明 |
|---|---|
| `TextNode` | 普通段落 |
| `CodeBlockNode` | 代码块，带语言标签、语法高亮、独立折叠 |
| `ListNode` | 有序/无序列表 |
| `DiffNode` | 预留，用于 git diff 样式渲染 |

### CodeBlockNode 缓存策略

- 以 `(lang, width, content)` 为 key 缓存 glamour 的 ANSI 输出。
- 缓存由 `CodeBlockNode` 自身持有，不放在全局 `mdContentCache`。
- 语言检测失败时回退到纯文本，避免 glamour 重新解析。

```go
type CodeBlockNode struct {
    lang    string
    content string
    cache   map[string]string // key: width
}

func (n *CodeBlockNode) Render(width int) string {
    key := strconv.Itoa(width)
    if out, ok := n.cache[key]; ok {
        return out
    }
    out := renderCodeBlock(n.lang, n.content, width)
    n.cache[key] = out
    return out
}
```

## 消息流（Message Flow）

```
键盘/鼠标事件
    │
    ▼
Model.Update
    │
    ├─ 全局消息（resize、input、slash）→ 主 Model 处理
    │
    └─ ComponentMsg{id, msg}
           │
           ▼
       找到对应 BlockComponent
           │
           ▼
       类型断言为 UpdatableComponent
           │
           ▼
       调用 Update，返回新组件 + Cmd
```

示例：用户按 `Ctrl+O` 展开某个 assistant block 的 thinking。

1. 主 `Model` 捕获 `KeyMsg`。
2. 根据当前焦点 block 构造 `ComponentMsg{ID: blockID, Msg: ToggleExpandedMsg{}}`。
3. `AssistantComponent.Update` 切换 `expanded` 状态。
4. `rebuildViewport` 重新计算可见区并渲染。

## 虚拟化 Viewport 算法

这是解决长输出卡顿的核心。

### 数据结构

```go
type virtualViewport struct {
    components []BlockComponent
    scrollY    int       // 当前滚动偏移（行）
    width      int
    height     int
    expanded   bool      // 全局 expanded 模式（Ctrl+O）
}
```

### 计算流程

1. **预计算高度**：遍历全部组件，调用 `Height(width, expanded)`，得到每个组件的起始行 `offsets`。
2. **确定可见窗口**：
   - `startLine = scrollY`
   - `endLine = scrollY + height`
3. **选择可见组件**：找到第一个 `offset + height >= startLine` 的组件，直到 `offset > endLine`。
4. **渲染可见组件**：只渲染选中的组件，并拼接成 viewport content。
5. **Buffer 优化**：上下各保留 1-2 个组件作为缓冲，减少快速滚动时的空白闪烁。

### 高度变化处理

组件高度变化（如展开/折叠）会改变后续所有组件的 `offset`。因此在 `Update` 后需要重新计算 `offsets`，但**不需要重新渲染不可见组件**。

```go
func (vv *virtualViewport) layout() []int {
    offsets := make([]int, len(vv.components))
    y := 0
    for i, comp := range vv.components {
        offsets[i] = y
        y += comp.Height(vv.width, vv.expanded)
    }
    return offsets
}
```

### 滚动到底部

流式输出时，`scrollY` 跟随总高度；viewport 渲染时仍然只渲染可见区。

## 缓存与 ANSI 策略

### Render 输出规范

- `Render()` 返回的字符串**允许包含 ANSI 转义码**（颜色、加粗等），这是 lipgloss 和 glamour 的工作方式。
- 组件自身负责缓存原始 ANSI 输出，避免重复计算。
- 组件不应在 `Render()` 中产生副作用（如写文件、发事件）。

### 缓存归属

| 数据 | 缓存位置 | key |
|---|---|---|
| glamour ANSI 输出 | `CodeBlockNode.cache` | `width` |
| lipgloss 样式字符串 | 各组件内部 | 视组件而定 |
| Markdown AST | `AssistantComponent` | `content + width` |

### 宽度变化

当终端宽度变化时，组件的 `Render()` 缓存 key 会变化（包含 width），因此会自然失效并重新渲染。

## 迁移计划

### 阶段 1：接口与基础组件（1-2 周）

1. 定义 `BlockComponent` 和 `UpdatableComponent` 接口。
2. 引入 `ComponentMsg` 分发机制。
3. 把 `SystemLogComponent` 和 `UserComponent` 迁移过去（最简单，验证接口）。

### 阶段 2：Assistant / Tool 组件（2-3 周）

1. 实现 `MarkdownNode` 树和 `CodeBlockNode`。
2. 迁移 `AssistantComponent`，替换 glamour 整段渲染。
3. 迁移 `ToolResultComponent`，支持折叠/展开。

### 阶段 3：虚拟化 Viewport（1-2 周）

1. 实现 `virtualViewport`。
2. 替换 `rebuildViewport()` 的全量渲染。
3. 优化滚动性能并补 benchmark。

### 阶段 4：主题与设计系统（1 周）

1. 统一 `theme.go` 的色板、间距、圆角。
2. 为所有组件应用新的设计 token。

## 测试策略

- **单元测试**：每个组件独立测试 `Render()` 和 `Height()`。
- **快照测试**：对 `AssistantComponent` 的 Markdown 输出做快照，防止视觉回归。
- **性能测试**：构造 1000 条消息的会话，测试 `rebuildViewport` 的耗时。
- **交互测试**：模拟 `ComponentMsg`，验证折叠/展开状态切换。

## 通往 Phase C（Webview）的预埋

组件化完成后，未来增加 Web 渲染器只需：

```go
type WebRenderer interface {
    Render(comp BlockComponent) WebNode
}
```

每个组件可以额外实现：

```go
type WebRenderable interface {
    BlockComponent
    WebRender() WebNode
}
```

Go 后端继续通过 `events.Bus` 推送事件，Web 前端订阅同一套事件流即可。`BlockComponent` 不依赖终端，因此逻辑可以在 Web 中复用。

## 非目标

- 本阶段不引入 TypeScript / Node / Bun 运行时。
- 不改动 `pkg/agent`、`pkg/events`、`pkg/session` 等核心逻辑。
- 不实现图片、图表等终端无法承载的媒体类型。

## 开放问题

1. `Height()` 是否需要预计算所有子节点？对于复杂 Markdown 可能会有额外开销，需要 benchmark 验证。
2. 是否保留全局 `toolsExpanded`（Ctrl+O）模式，还是让每个组件自己管理 expanded？建议保留全局模式，但组件可覆盖。
3. 是否需要鼠标支持（点击代码块折叠）？Bubble Tea 的鼠标支持有限，可先在键盘交互上验证接口。
