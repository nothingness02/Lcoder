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
