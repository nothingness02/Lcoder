# go-tui 迁移评估

## 一、go-tui 是什么

`github.com/grindlemire/go-tui` — 声明式 Go 终端 UI 框架。

**核心创新**：用 `.gsx` 模板（HTML-like 语法 + Tailwind 风格 class）声明 UI，编译生成类型安全的 Go 代码，运行时用 flexbox 布局引擎渲染。

```go
// chat.gsx
@component Chat() {
    <box flexDirection="column">
        <TextArea ref={textareaRef} />
        <box flexGrow="1" border="rounded">
            {messages}
        </box>
    </box>
}
```

**规模**：56K 行源码 + 69K 行测试 = **125K 行**。Pre-1.0 但非常活跃。

## 二、go-tui 的优点

### 1. 声明式 UI（最大优势）

| 维度 | Bubble Tea | go-tui |
|------|-----------|--------|
| 界面定义 | 命令式：`View()` 手拼字符串 | 声明式：`.gsx` 模板 |
| 布局 | `lipgloss` 手动计算 | **flexbox 自动布局** |
| 组件复用 | struct + 接口手动组织 | 组件语法 + 参数传递 |
| 状态绑定 | 手动 diff 更新 | `State[T]` 响应式 |

### 2. 响应式状态

```go
// go-tui: 状态变化自动触发重渲染
tui.NewState(false)
state.Set(true)  // ← 自动通知依赖该状态的组件重绘
```

Bubble Tea 需要手动在 `Update()` 里判断消息类型，再手动更新视图。

### 3. 内置能力（对标 Lcoder 需求）

| 能力 | go-tui | Lcoder 现状 |
|------|:---:|:---:|
| Markdown 渲染 | ✅ `24-markdown` | ✅ glamour |
| TextArea | ✅ `textarea.gsx` | ✅ bubbles/textarea |
| 流式输出 | ✅ `16-streaming` + `StreamWriter` | ❌ 手写 |
| 动画 | ✅ `20-animation` | ⚠️ 手写 |
| 内联模式 | ✅ `15-inline-mode` | ❌ |
| AI 聊天示例 | ✅ `ai-chat`（完整聊天 UI） | — |
| 目录树 | ✅ `21-directory-tree` | ⚠️ 手写 |

### 4. 渲染效率（解决当前卡顿）

go-tui 有 `buffer.go`（2D cell 网格）+ `buffer_diff_test.go` —— **增量 diff 渲染**，只输出变化的 cell。这正好解决 Lcoder 当前"每次 delta 全量重建 viewport"的卡顿问题。

## 三、go-tui 的劣势/风险

| 风险 | 说明 | 严重度 |
|------|------|:---:|
| **Pre-1.0** | README 明确标注 "APIs may evolve"，无稳定承诺 | 🔴 |
| 单作者/社区小 | grindlemire 个人项目，无 Charm 那样的大社区 | 🔴 |
| 依赖少 | 只依赖 x/sys + x/tools，很多功能自研 | ⚠️ |
| 学习成本 | .gsx 语法 + 组件系统，团队要学新框架 | ⚠️ |
| 文档少 | 只有 design.md + examples，无完整 API 文档 | ⚠️ |
| 无成熟案例 | 无生产级大项目使用 | ⚠️ |

## 四、Lcoder 当前 TUI 复杂度

```
Lcoder TUI: 7,586 行代码 + 5,896 行测试，97 个文件

核心文件:
  keys.go          1,151 行  ← 快捷键/事件分发
  events.go          455 行  ← 事件总线 → UI 状态
  model.go           387 行  ← Bubble Tea Model
  providerpanel.go   316 行  ← 面板
  view.go            264 行  ← 布局
  input.go           206 行  ← 输入
  confirm.go         195 行  ← 审批弹框
  fileindex.go       182 行  ← @文件索引
  ...
```

涉及的功能点：
- 事件总线订阅（TurnStart/End、MessageStart/End、ToolExecution、Compaction）
- 流式文本渲染（patchAssistant 每 delta）
- 权限审批弹框
- 会话选择器
- 扩展面板
- @文件补全（fuzzy + WalkDir）
- 工具调用展开/折叠
- 子 agent 活动镜像
- 模式切换
- markdown 渲染（glamour）

## 五、迁移工期估算

| 阶段 | 内容 | 工期 |
|------|------|:---:|
| **0. 学习期** | 学 .gsx 语法、组件、状态、refs | 2-3 天 |
| **1. 骨架** | App 循环 + 主布局（header/chat/input/status） | 3-4 天 |
| **2. 消息渲染** | 用户/助手块、markdown、工具调用展开 | 3-5 天 |
| **3. 流式** | StreamWriter 接入事件总线、delta 渲染 | 2-3 天 |
| **4. 输入** | TextArea + @文件补全 + 斜杠命令 | 3-4 天 |
| **5. 面板** | 审批、会话选择、扩展、provider | 3-4 天 |
| **6. 子agent** | 活动镜像、进度显示 | 2-3 天 |
| **7. 打磨** | 动画、配色、性能调优 | 2-3 天 |
| **8. 测试** | 迁移 97 个文件的测试 | 3-5 天 |
| **合计** | | **3-4 周** |

**单人全职：约 3-4 周。** 熟练后可能 2-3 周，保守估计 4 周。

## 六、关键决策：值得迁移吗？

### 支持迁移的理由
1. **解决当前卡顿**：go-tui 的 buffer diff 是原生优势，Bubble Tea 需要手写节流
2. **声明式布局**：比手拼 lipgloss 字符串可维护得多
3. **流式 StreamWriter**：Lcoder 手写的流式逻辑可以简化
4. **ai-chat 示例**：官方就有完整聊天 UI 参考

### 反对迁移的理由
1. **Pre-1.0 风险**：API 会变，迁移后可能跟着 upstream 改
2. **小社区**：遇 bug 难找方案
3. **当前 Lcoder TUI 已可用**：只是卡顿和细节问题，可通过优化修复
4. **4 周工期**：占用大量开发时间，收益不确定

### 折中方案（推荐）

**不要全面重写。** 先用 go-tui 的思路优化现有 Bubble Tea 实现：

| 问题 | 用 go-tui 思路解决 |
|------|-------------------|
| 卡顿 | 加 buffer diff / 节流（不改框架） |
| 布局繁琐 | 抽公共布局函数，减少手拼 |
| 流式 | 封装 StreamWriter 概念 |
| @补全 | 已规划缓存+上限 |

**如果未来 go-tui 稳定到 1.0 且有生产案例，再考虑迁移。** 当前投入 4 周重写一个可用但有小毛病的 TUI 不划算。

## 七、结论

| 维度 | 评估 |
|------|------|
| go-tui 表现力 | ⭐⭐⭐⭐⭐ 声明式+flexbox+响应式，显著优于 Bubble Tea |
| go-tui 成熟度 | ⭐⭐ Pre-1.0，社区小 |
| 迁移工期 | 3-4 周（单人全职） |
| 建议 | **暂不迁移**，先用其思路优化现有实现 |

## 八、复评（2026-08）：维持不迁移，记录可优化方向

### 复评结论

- go-tui 仍为 Pre-1.0（v0.13.x），单作者、无生产案例，三条核心风险均未消除。
- 折中方案已大部分落地：帧调度（`pkg/tui/scheduler.go`，throttle+coalesce，30fps / VSCode 10fps）、块级渲染缓存（`components/assistant.go`）、glamour renderer 缓存、虚拟视口 + sticky bottom、性能回归测试（`rebuild_bench_test.go`）。
- go-tui 的两大杀手锏中，buffer diff 已被"帧调度+块缓存"等效解决；净收益只剩声明式 .gsx 布局一项。
- 迁移成本上升：TUI 已增至 116 个文件（7,586 行代码 + 5,896 行测试），与事件总线、权限审批、checkpoint、@文件索引、goal 模式、子 agent 镜像深度耦合，重估工期 4-6 周。
- **当前 TUI 的问题是打磨问题，不是框架问题。**

### 可优化方向（按优先级）

| # | 方向 | 思路来源 | 工作量 | 说明 |
|---|------|---------|:---:|------|
| 1 | **流式增量重建** | go-tui cell diff | 数天 | `rebuildViewport`（`pkg/tui/view.go:19`）每帧对所有 block 跑 `layoutComponents` + `buildVirtualContent`；流式期间只有末尾 block 在变，可物化前缀内容、每帧只重渲染尾部 |
| 2 | **表现力增强** | go-tui examples | 1-2 周 | 内联/紧凑模式（对标 `15-inline-mode`）、工具执行进度动画、更细的主题系统。表现力短板在设计投入而非框架能力 |
| 3 | **布局代码减负** | go-tui 声明式 | 持续 | 继续抽公共布局函数（`panelFrame`、`statusLine` 模式），向声明式靠拢但不引入模板编译链 |
| 4 | **复评触发点** | — | — | go-tui 发布 1.0、或出现知名生产项目采用时，重新评估迁移 |

### 已落地（2026-08）

- **复制文本**：`Ctrl+Y` 复制聚焦块（或最后一条回复）到剪贴板——Unix 走 OSC 52（SSH/tmux 友好），Windows 走 Win32 剪贴板 API（conhost 也覆盖），见 `pkg/tui/clipboard*.go`、`copy.go`。
- **历史回看**：`PgUp`/`PgDn` 翻页、`Ctrl+Home`/`Ctrl+End` 跳到最早/最新消息（输入态与流式中均可用），视口右缘滚动位置指示条，见 `view.go scrollbarView`。
- **退出后 scrollback**：TUI 退出 alt screen 后把本次会话 transcript 以纯文本打印到 stdout，进入终端原生 scrollback，见 `pkg/tui/transcript.go`。
- **目录 mention**：`@dir` 可补全、可校验、可提交（`mention.go`/`fileindex.go`/`fd.go`）。
- 结论：原生终端体验的高频诉求（复制、快速回看）已在 alt screen 架构内覆盖；仅剩"会话中拖终端自带滚动条"需要 inline 渲染（3-5 周），暂不投入。
