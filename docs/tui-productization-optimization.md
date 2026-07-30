# Lcoder TUI 产品化优化 Spec

> 来源：`docs/tui-known-issues.md`（4 项缺陷）+ `docs/tui-gap-analysis.md`（与 Kimi Code 差距）。
> 调研参考：`reference/kimi-code`（fd 补全、editor）、`reference/pi`（渲染 flush、editor、终端协商）、`reference/opencode`（细粒度响应式、工具折叠、thinking 摘要）。

## 0. 现状核实（对两份 docs 的修正）

调查代码后，以下论断需要修正，直接影响 spec 范围：

| docs 论断 | 实际现状 |
|---|---|
| gap#2「思考过程：无」 | 已存在：`AssistantComponent.renderThinking` 折叠态显示首行、展开态全量（`components/assistant.go:112`） |
| gap#2「子 Agent：简单进度文本」 | 已存在组显示：`ToolResultComponent.renderSubagentActivity` 含多 child 树形行 + 聚合头（`components/tool_result.go:131`） |
| gap#2「工具调用结果折叠到块外」 | 已有内联预览：`toolPreview(result, 2, 1, width)` 头 2 行 + 尾 1 行 + chip（`tool_result.go:98`） |
| gap#4「Kimi Code 输入框内 @mention 高亮」 | **Kimi TUI 并不做输入框内高亮**，仅对首行 slash token 着色（kimi-code `custom-editor.ts`）。gap 文档此条有误 |
| gap#1「Kimi 组件级脏标记」 | **pi-tui 并无组件级 dirty 标记**；实际是「全量 render + 行级 diff flush + DEC 2026 同步输出包裹」（pi `tui.ts:1463`）。差距在帧调度，不在组件模型 |
| issue#3 根因 | 方向正确但需补充：bubbletea standard renderer 本身已做行级 diff，瓶颈是 Lcoder 层每 delta 全量 `rebuildViewport()`（glamour 重渲染 ≤32KB 尾部 + 全 block 布局 + 大字符串拼接），在 VSCode 终端低吞吐下帧堆积 |

已有的产品化基础（不要推翻重建）：虚拟视口（`virtual_viewport.go` 只渲染可见组件）、glamour 渲染缓存（按 width+content）、流式尾部 32KB 截断（`boundStreamTail`）、粘贴折叠（`paste.go`）、subagent 嵌套显示。

## 1. 目标与非目标

**目标**（按优先级）：

- P0：消除 4 个 known issues（补全光标感知、输入框行高、VSCode 空白、补全卡顿），达到「主流终端日常可用」。
- P1：渲染帧调度产品化（throttle+coalesce）、持久标题栏、工具/thinking 展示收尾。
- P2：输入区体验升级（mention chips、粘贴保护增强），为自研 editor 留好接口。

**非目标**：

- 不自研 editor 组件替换 bubbles/textarea（pi editor 2307 行的成本不值得现在付；@mention 输入框内高亮连 Kimi 都没做，见 §0）。接口设计预留替换可能。
- 不引入 OpenTUI 式响应式框架；保持在 bubbletea 模型内优化。
- 不做 frecency 排序（opencode 依赖服务端 fff，Lcoder 无此层；fd+fuzzy 已足够）。

## 2. 总体架构

```
事件/按键 ──► Model.Update ──► 状态变更 + markDirty()
                                   │
              即时路径（按键/状态切换）：立即 rebuildViewport()
              流式路径（delta/subagent）：frameScheduler 节流消费
                                   │
                                   ▼
                     rebuildViewport()（全量布局+可见渲染，现有）
                                   │
                                   ▼
                     viewport.SetContent → bubbletea 行级 diff flush
```

原则：**帧调度在 Model 层收口**，17 处 `rebuildViewport()` 调用点不各自为政地决定何时渲染。组件模型、虚拟视口、glamour 缓存保持不变。

---

## 3. W1 — 渲染帧调度器（修 issue#3 + gap#1，P0）

### 3.1 设计

新增 `pkg/tui/scheduler.go`：

```go
// frameScheduler 合并高频重绘请求，保证两次 rebuild 之间至少间隔 minInterval。
// throttle + coalesce（pi 的 scheduleRender），不是 debounce——debounce 会让
// 流式尾部长时间不落屏。
type frameScheduler struct {
    dirty       bool
    minInterval time.Duration // 默认 33ms（~30fps）
    lastFlush   time.Time
    timerActive bool
}
```

Model 集成：

- 所有 `rebuildViewport()` 调用点改为两类：
  - `m.requestRender()` — 即时渲染（按键、菜单、面板、focus、resize、turn 边界事件）。内部直接 `rebuildViewport()` 并记录 `lastFlush`。
  - `m.requestStreamRender()` — 流式渲染（`MessageUpdateEvent`、`SubagentActivityEvent`）。置 `dirty=true`；若距 `lastFlush ≥ minInterval` 则立即 rebuild，否则确保一个 `tea.Tick(remaining)` 的 `frameTickMsg` 在途，tick 到达时若 dirty 则 rebuild。
- 终态事件（`MessageEndEvent`、`ToolExecutionEndEvent`、`TurnEndEvent`、`AgentEndEvent`、`ErrorEvent`）走 `requestRender()`——**终态必须即时落屏**，不受节流延迟。
- 单元测试通过直接构造 scheduler 验证节流语义（不依赖真实时钟：注入 `now func() time.Time`）。

### 3.2 VSCode 终端自适应（issue#3）

- 启动时读 `TERM_PROGRAM`：`vscode` → `minInterval = 100ms`（10fps）；其他终端 33ms。可用 `LCODER_TUI_FPS` 环境变量覆盖（诊断/回归用）。
- 每个 turn 开始（`MessageStartEvent`）强制一次 `requestRender()` 全量同步（docs 方案 B），消除「内容正在重建中」的中间帧被跳过后残留空白。
- 保留文档化 workaround（换 cmd.exe / wt.exe），修复后按 §8 矩阵人工验证。

### 3.3 rebuild 本身的开支削减

- `buildVirtualContent` 的离屏组件空白行生成改为 `strings.Repeat("\n", n)` 拼接（当前逐行 append，10k 行历史下每帧 10k 次分配）。
- `layoutComponents` 的 `Height()` 调用对 assistant 组件命中 glamour 缓存后成本已低；不引入组件级 dirty 模型（§0 已论证 pi 也不需要）。
- 扩展 `rebuild_bench_test.go`：基准覆盖 1k/10k blocks + 流式 patch 场景，设定回归阈值（10k blocks 全量 rebuild < 50ms）。

### 3.4 验收标准

- 流式 2000 token 响应期间，`rebuildViewport` 实际执行次数 ≤ token delta 数 × 0.2（探针：测试钩子计数）。
- VSCode PowerShell 终端连续 10 轮问答无永久空白（人工验证清单）。
- 按键到字符回显不经过 scheduler（即时路径），无输入延迟回归。

---

## 4. W2 — @file 补全子系统（修 issue#1 + issue#4，P0）

### 4.1 光标感知 mention（issue#1）

bubbles/textarea v1.0.0 提供所需 API（已核实源码）：`Line() int`（硬行号）、`LineInfo()`（`StartColumn + ColumnOffset` = 行内 rune 列）、`Value()`。

`InputModel` 新增：

```go
// CursorOffset 返回光标在 Value() 中的绝对 rune 偏移。
func (m InputModel) CursorOffset() int
// 实现：offset = Σ(len([]rune(line_i))+1, i<row) + col
```

`filemenu.go` 重写检测逻辑（对齐 kimi `extractAtPrefix` 的反向扫描，替换 `LastIndex`）：

```go
// activeMentionAt 返回光标所在 @mention 的 partial 与 '@' 的绝对偏移。
// 规则：从光标前文本反向扫描到分隔符（空格/Tab/换行）得到 token 起点；
// token 以 '@' 开头则命中。光标在 @word 中间同样命中；光标位于已完成的
// "@word " 之后（token 不含 @）不命中。
func activeMentionAt(text string, cursor int) (partial string, at int, ok bool)
```

`acceptFileMenu` 用返回的 `at` 与 cursor 替换 `[at, cursor)` 区间，不再用 `LastIndex`。`refreshMenu` 改调 `activeMentionAt(val, m.input.CursorOffset())`。方向键移动光标后 `refreshMenu` 已在现有路径上（handleInputKey 尾部统一调用），无需额外接线。

边界用例（测试矩阵）：光标在 `@` 后立即 / 在 `@wo|rd` 中间 / 在 `@word ` 空格后 / 文本含多个 `@` / `hello@world`（@ 前非空白，不触发）/ 多行输入跨行光标 / CJK 字符前的 @（offset 按 rune 计，不可按 byte）。

### 4.2 异步文件索引（issue#4）

新增 `pkg/tui/fileindex.go`：

```go
// FileIndex 缓存 cwd 下的相对路径列表，后台遍历、按键零 IO。
type FileIndex struct {
    // 遍历参数：跳过 .git/node_modules/隐藏目录；MAX_SCAN = 20000 条目上限；
    // context 取消；完成或取消都记录状态。
}

func NewFileIndex(cwd string) *FileIndex
func (ix *FileIndex) EnsureStarted(ctx context.Context)  // 幂等；后台 goroutine walk
func (ix *FileIndex) Ready() bool
func (ix *FileIndex) Matches(partial string, limit int) []string // 纯内存 fuzzy
func (ix *FileIndex) Invalidate()                               // 标记过期，下次 EnsureStarted 重扫
```

语义：

- **触发即预热**：TUI 启动（`NewModel`）即 `EnsureStarted`，不等第一次 `@`——大项目首次 `@` 时索引通常已就绪。
- **TTL 30s + Invalidate**：会话内文件可能变动；每次新 `@` token 开始时若索引年龄 > TTL 则后台重扫（旧索引继续服役，新索引原子替换，无空窗）。
- **未就绪降级**：索引未 Ready 时菜单显示 `indexing…` 占位行（不阻塞输入），就绪后自动替换为结果。
- **匹配**：对缓存列表跑现有 `sahilm/fuzzy`（20k 条目 < 10ms，同步调用无卡顿）；候选上限 50、展示上限 10（保持 `fileMenuMax`）。
- **WalkDir 遍历本身**：带 `ctx` 检查（每次回调首行 `select ctx.Done()`）、`MAX_SCAN` 截断、错误容忍（`err != nil → return nil`）。重扫请求通过 generation counter 丢弃过期结果（替代 kimi 的 AbortController）。

### 4.3 fd 委托（可选加速，P0 内实现，降级安全）

启动时探测：`exec.LookPath("fd")` → `fdfind` → 不可用则纯索引模式。探活用 `fd --version`（对齐 kimi 的 `isExecutableFd`）。fd 模式下每次查询直接 spawn：

```
fd --base-directory <cwd> --max-results 100 --type f --type d \
   --follow --exclude .git --exclude .git/** <query 转 regex>
```

- 每次请求新 `context.WithCancel`，新按键到来即 cancel 旧进程（`cmd.Cancel`/`Process.Kill`），stdout 读取前查 `ctx.Err()`。
- query 含 `/` 时加 `--full-path`（kimi 同款规则）。
- fd 输出与索引模式输出统一进 `Matches` 的排序/截断管线，菜单层无感知。
- fd 失败（非零退出/超时 500ms）自动回退索引模式并记住本次会话不再尝试。

### 4.4 验收标准

- 10 万文件项目（探针：临时目录生成）：连续输入 `@abc` 每个字符的 `refreshMenu` 耗时 p95 < 16ms；无 goroutine 泄漏（`goleak` 或计数遍历 goroutine）。
- 光标在文本中间的 `@` 触发补全（issue#1 全部边界用例过测试）。
- 无 fd 环境行为与有 fd 环境结果一致（菜单项集合相等，顺序可不同）。

---

## 5. W3 — 输入区（修 issue#2 + 体验项，P0/P2）

### 5.1 视觉行高（issue#2，P0）

`desiredHeight()` 改为按软换行估算（textarea 内部 `memoizedWrap` 不可访问，自行按宽度估算）：

```go
func (m InputModel) desiredHeight() int {
    inner := m.textarea.Width()      // 已含 prompt 扣除后的文本宽度？——实现时核实；
                                     // 若 Width() 是总宽，inner = Width() - promptWidth(2)
    lines := 0
    for _, hard := range strings.Split(m.textarea.Value(), "\n") {
        w := lipgloss.Width(hard)    // 处理 CJK 双宽
        n := 1
        if inner > 0 && w > inner {
            n = (w + inner - 1) / inner
        }
        lines += n
    }
    return clamp(lines, inputMinHeight, inputMaxHeight)
}
```

- 与 textarea 实际 word-wrap 可能差 1 行（它按词边界折行且行尾留 2 格），**宁多不少**：估算值取 `ceil` 后，超高被 `inputMaxHeight=6` 截断时由 textarea 内部滚动接管，已有行为。
- `updateSizes` 里宽度变化后已调 `SyncHeight()`，路径完整。
- 测试：纯 ASCII 长行、CJK 长行、混合、空行、恰好等于宽度边界（w == inner 与 w == inner+1）。

### 5.2 Mention chips 实时预览（P2，替代「输入框内高亮」）

§0 已论证 Kimi 也未做输入框内高亮。产品化替代：composer 下方、suggestion 行位置，实时渲染已解析 mention 的 chips 行：

```
› 改一下 @main.go 然后 @config.yml
  main.go  config.yml            ← accent 色，只列已解析的文件
```

- 复用 `parseMentions` + `resolveMention`（已存在），在 `refreshMenu` 同路径更新；空则该行不渲染（不占高度）。
- **只做正向确认，静默处理未解析项**：chips 行只显示匹配到的文件，不为未解析 mention 做红色告警——输入中途与「永远解析不到」在该行上无法区分，告警是伪精确。负向反馈保留唯一出口：提交时 `validateMentions` 拦截（`keys.go` 现有逻辑）。
- 远期自研 editor 时，chips 行数据（mention span 列表）可直接喂给 token 高亮，不浪费。

### 5.3 粘贴保护增强（P2）

现有 `pasteStash`（>1000 runes 折叠为 `[Pasted #N]` 占位符）保留，两项增强：

- 评估 bubbletea v1.3 的 bracketed paste 消息（`tea.PasteMsg`）：若可用，`shouldStash` 从「纯长度启发式」改为「paste 事件且超阈值」，手工敲出的长文不再被折叠。不可升级则维持现状并在文档注明。
- 占位符原子性：当前用户可编辑占位符文本导致 `expand` 失配。增强：提交时 `expand` 失配的占位符按原样文本发送并告警（防御），不做 pi 的 grapheme 级原子删除（textarea 内不可行，列入自研 editor 范围）。

---

## 6. W4 — 持久标题栏（gap#3，P1）

顶部一行常驻标题栏（非启动 banner，banner 仍提交为首个 block）：

```
 Lcoder · plan · claude-sonnet-4-5 · session a1b2c3d4 · turn 12
```

- 渲染：`renderTopBar(m, width)`，accent 色 `·` 分隔，dim 文本；宽度不足时按优先级裁剪（mode > model > session > turn，右侧先丢）。
- 布局：`View()` 改 `JoinVertical(topBar, viewport, bottomRegion)`；`updateSizes` 中 viewport 高度再减 1。
- 数据源：mode（`m.modeLabel()`）、model（`m.model`）、session（`m.session.SessionID()` 前 8 位）、turn（`m.completedTurns`；`loadSession` 重置为 0 是可接受的语义——显示的是本次打开的轮次）。
- 窄终端（< 50 列）整行隐藏，信息仍可从 `/status` 获取。
- 状态栏（底部 context/cost 行）保持不变——头部给「身份」，状态栏给「预算」。

---

## 7. W5 — 消息组件收尾（gap#2/#5，P1）

### 7.1 工具输出折叠：字符预算（opencode `collapseToolOutput`）

`toolPreview` 升级为纯函数 `collapseToolOutput(content string, maxLines, width int)`：行数截断（现状 head 2 + tail 1）之外加字符预算 `maxLines × (width - 6)`，超长单行（minified JSON、base64）按预算截断而非占满整行。折叠提示语保留 `… +N more (ctrl+o to expand)`。

### 7.2 thinking 收尾

折叠态已是单行摘要（`renderThinking` 首行截断 60）；补 opencode 的完成态：thinking 结束（首个非 thinking delta 或 MessageEnd）时记录耗时，折叠态显示 `Thought: <首句> · 12s`。数据：`block` 加 `thinkingSecs float64`，`patchThinking`/`commitAssistant` 路径记录。

### 7.3 流式 markdown 未闭合 fence

流式期间内容含奇数个 ``` fence 时，glamour 把后续全部文本渲染为 code，样式跳变。`patchAssistant` 渲染前检测：fence 计数为奇则在渲染副本尾部补 "\n```"（`streamLive`/commit 用原文，仅影响显示）。`preprocessMath` 的未闭合 `$` 已是「找不到配对就原样输出」，无需改。

### 7.4 验收标准

- 流式输出含未闭合代码块时，终端无大面积 code 样式跳变（探针截图对比）。
- `collapseToolOutput` 纯函数表格测试：行数预算、字符预算、空内容、单行超长。

---

## 8. 终端兼容矩阵（W1/W5 完成后人工验证）

| 终端 | 帧率策略 | 验证项 |
|---|---|---|
| Windows Terminal | 33ms | 流式流畅、无闪烁 |
| cmd.exe | 33ms | 同上 + CJK 宽度 |
| VSCode PowerShell | 100ms | **issue#3：连续 10 轮问答无空白**；流式可感知但低频 |
| VSCode cmd | 100ms | 同上 |
| kitty/iTerm2（如有） | 33ms | 品牌渐变、真彩 |

检测逻辑集中在一个 `termProfile()` 函数，单测覆盖环境变量组合。

---

## 9. 测试策略

- `activeMentionAt`：§4.1 边界矩阵（表格驱动，含 CJK 与多行）。
- `FileIndex`：临时目录构造树（含隐藏目录、软链、超 MAX_SCAN）；TTL 过期重扫；取消语义；并发 `Matches` 安全。
- `desiredHeight`：§5.1 用例矩阵。
- `frameScheduler`：注入假时钟验证 throttle/coalesce/终态即时。
- `collapseToolOutput`、`termProfile`、chips 行渲染：纯函数表格测试。
- 基准：`rebuild_bench_test.go` 扩展（§3.3）。
- 既有测试全部保持通过：`go test $(go list ./... | grep -v 'reference/Shannon')`。
- 不变量探针实测（项目约定）：渲染帧率、补全耗时、goroutine 计数均以运行探针为准，不靠静态推断。

## 10. 里程碑

| 里程碑 | 内容 | 对应问题 | 状态 |
|---|---|---|---|
| M1（P0） | W1 帧调度 + VSCode 自适应；W2 补全子系统（光标感知 + FileIndex + fd）；W3.1 行高 | known-issues #1–#4、gap#1 | ✅ 完成（另修复滚动物化/吸底 `09c158f`、行高滚动 `06c128f`） |
| M2（P1） | W4 标题栏；W5 组件收尾 | gap#3、#2、#5 | ✅ 完成 |
| M3（P2） | W3.2 chips 行；W3.3 粘贴增强；自研 editor 评估报告（mention 高亮、原子 paste marker、IME 光标的真实成本） | gap#4 | 未开始 |

每个里程碑结束：全量测试 + race + §8 适用项人工验证 + 更新 `docs/tui-known-issues.md` 状态。

## 11. 影响文件汇总

| 工作流 | 新建 | 修改 |
|---|---|---|
| W1 | `pkg/tui/scheduler.go`(+test) | `model.go`、`events.go`、`keys.go`、`view.go`、`virtual_viewport.go`、`rebuild_bench_test.go`、`app.go`（TERM_PROGRAM 检测注入） |
| W2 | `pkg/tui/fileindex.go`(+test)、`pkg/tui/fd.go`(+test) | `filemenu.go`、`input.go`（CursorOffset）、`keys.go`（refreshMenu/acceptFileMenu）、`model.go`（预热） |
| W3 | — | `input.go`(+test)、`view.go`（chips 行）、`paste.go` |
| W4 | `pkg/tui/topbar.go`(+test) | `view.go`、`model.go`（updateSizes） |
| W5 | — | `components/tool_format.go`、`components/assistant.go`、`events.go`、`block.go` |
