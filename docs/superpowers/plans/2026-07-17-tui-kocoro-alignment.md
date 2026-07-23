# TUI 全面对标 Kocoro 展示效果

> **For agentic workers:** REQUIRED SUB-SKILL: 使用 `superpowers:executing-plans` 或 `superpowers:subagent-driven-development` 按阶段逐步实施。任务使用 checkbox (`- [ ]`) 跟踪。

**Goal:** 参考 `reference/Kocoro/internal/tui` 的设计与展示效果,全面优化 `pkg/tui`。覆盖配色体系、消息/工具渲染、品牌动画、流式手感、状态动画、交互组件、错误展示。**保留 Lcoder 自有 Nord 青品牌身份**(swirl/shimmer/accent 用青色系而非 Kocoro 粉),其余展示效果对齐 Kocoro。

**设计原则:**
1. 保留 Lcoder 青(`#5E81AC`/`#88C0D0` Nord 系)为默认 accent,补全 Kocoro 式语义色体系与多预设切换。
2. swirl/shimmer/渐变一律用 Lcoder 青色梯度,不照搬粉色。
3. 不破坏现有骨架(状态机、block/component、virtual viewport、事件驱动),只增强展示层与交互手感。
4. 不持久化 `/color` 选择(与 Kocoro 一致,重启重置),**避免改动 config 的 test/生产/eval 三场景**(见 memory [[config-align-test-prod-eval]])。若后续要持久化须单独处理。
5. `reference/` 只读,不修改;Kocoro 代码仅作行号参考。

**Architecture:** 所有改动集中在 `pkg/tui/` 及子包 `components/`、`markdown/`。复用现有 `applyAccent`(死代码)、`diff.go`(死代码)、`slash_registry.RegisterCommand`。流式零 pop 在 Lcoder 已基本具备(`patchAssistant`->`SetContent`->`renderedContent` 已用 markdown 渲染在飞文本),本方案做性能补强而非从零实现。

**Tech Stack:** Go 1.25、Bubble Tea、lipgloss、glamour(含 Chroma 语法高亮)、bubbles。

---

## 阶段依赖与实施顺序

```
Phase 0(配色基建) ──> 所有后续阶段依赖
Phase 1(流式性能) ──> 独立
Phase 2(消息渲染) ──> 依赖 Phase 0 语义色
Phase 3(工具/diff) ──> 依赖 Phase 0
Phase 4(header/swirl) ──> 依赖 Phase 0;改动布局
Phase 5(spinner/shimmer) ──> 依赖 Phase 0
Phase 6(交互组件) ──> 依赖 Phase 0
Phase 7(错误/面板/statusline) ──> 依赖 Phase 0
```

**建议分两批 PR:**
- 第一批(展示层基础):Phase 0 → 1 → 2 → 3。风险可控、收益最高,可独立验证。
- 第二批(品牌动画与交互):Phase 4 → 5 → 6 → 7。改动较大,依赖第一批。

---

## Phase 0:配色体系补全与 /color 运行期切换(基础设施)

**Files:**
- Modify: `pkg/tui/components/theme.go` — 补 `ColorWarn`、`ColorInfo` 语义色
- Modify: `pkg/tui/theme.go` — 暴露 `colorWarn`/`colorInfo` + `styleWarn()`/`styleInfo()`;定义 `accentPresets` 列表;扩 `accentPreset` 加 `desc` 字段
- Modify: `pkg/tui/slash_registry.go` — 注册 `/color`(别名 `/theme`)命令
- Create: `pkg/tui/colorpanel.go` — accent preset 选择面板(圆角盒,复用 `cmdpanel` 风格)
- Modify: `pkg/tui/keys.go`、`pkg/tui/view.go` — 接入 color 面板状态与渲染
- Modify: `pkg/tui/theme_test.go`、`pkg/tui/components/theme_test.go` — 覆盖新色与 preset

**参考:** Kocoro `theme.go:48-104`、`app.go:2446-2451`、`picker.go:32-48`

- [ ] **Step 1:** 在 `components/theme.go` 补 `ColorWarn`(Light `130`/Dark `214`)、`ColorInfo`(Light `25`/Dark `39`),及对应 `StyleWarn()`/`StyleInfo()` 构造函数。在 `theme.go` 暴露 `colorWarn`/`colorInfo`/`styleWarn()`/`styleInfo()`。
  - 验证:`theme_test.go` 断言新色 token 存在且为 AdaptiveColor。
- [ ] **Step 2:** 扩 `accentPreset` 加 `desc string` 字段;定义 `accentPresets` 列表:青 `lcoder`(默认 Nord `#5E81AC`/`#88C0D0`)、粉 `kocoro`(`#FF5C8A`/`#C9105A`)、蓝 `ocean`、绿 `forest`、紫 `violet`。`applyAccent` 保持覆盖全局 `colorAccent`。
  - 验证:切换后新渲染立即换色;已提交滚动历史保留原色(与 Kocoro 一致)。
- [ ] **Step 3:** 新增 `colorpanel.go`,渲染 preset 列表(名称 + desc + 当前选中标记),圆角 `colorFaint` 边框,选中项 `colorSelect`。新增 `uiState` 无需——复用类似 `cmdPanel` 的内嵌面板结构挂在 Model 上。
- [ ] **Step 4:** 在 `slash_registry.go` 注册 `/color`(`/theme`),handler 打开 color 面板;`keys.go` 处理面板上下选择与 Enter 应用 `applyAccent` + 关闭面板。
  - 验证:手动 `/color` 打开面板,选择不同 preset,header/边框/spinner 立即换色;Esc 关闭。

---

## Phase 1:流式渲染性能补强(零 pop 已具备)

**Files:**
- Modify: `pkg/tui/components/assistant.go` — `renderedContent` 增量优化
- Modify: `pkg/tui/events.go` — `patchAssistant` 引入 `boundStreamTail` 截尾
- Modify: `pkg/tui/model.go` — 新增 `streamLiveMaxBytes` 常量与截尾逻辑

**参考:** Kocoro `app.go:1589-1608`(boundStreamTail)、`app.go:2099-2124`(零 pop)、`app.go:2204-2217`(sha256 缓存)

**现状澄清:** Lcoder 流式已用 `markdown.RenderMarkdown` 渲染在飞文本(`assistant.go:97-106`),回合结束 `commitAssistant` 用同一渲染路径,视觉零 pop 基本已实现。本阶段只补性能,不改渲染语义。

- [ ] **Step 1:** 新增 `streamLiveMaxBytes = 32768`,在 `patchAssistant` 中对超长在飞 content 按行边界截尾(`boundStreamTail`),保证每帧 markdown 重渲染 O(32KiB)。
  - 验证:超长输出(>6k 词)流式不卡顿;回合结束 `commitAssistant` 用完整 content 渲染(不截尾)。
- [ ] **Step 2:** `renderedContent` 缓存已按 `(width, content)`;补充:resize 同宽度命中时 O(1)(现状已具备,确认无回归)。
  - 验证:`rebuild_bench_test.go` 无性能回归。
- [ ] **Step 3:** 确认每 delta 即时刷新(现状 `patchAssistant` 已调 `rebuildViewport`,保持);不引入额外 tick 延迟。
  - 验证:流式逐字可见,无 100ms 延迟。

---

## Phase 2:消息渲染增强

**Files:**
- Modify: `pkg/tui/markdown/renderer.go` — 自定义 StyleProfile + Chroma 语法高亮主题
- Modify: `pkg/tui/components/user.go` — user 消息气泡(自适应明暗背景)
- Modify: `pkg/tui/components/assistant.go` — thinking 块改进
- Create: `pkg/tui/markdown/sources.go` — OSC8 可点击超链接 Sources 段(可选,优先级中)
- Modify: `pkg/tui/markdown/renderer_test.go`

**参考:** Kocoro `markdown.go:109-238`(compactStyle)、`markdown.go:21-92`(OSC8 Sources)、`app.go:1823-1833`(user 气泡)

- [ ] **Step 1:** `renderer.go` 加自定义 `compactStyle`(Document margin=0、列表 `LevelIndent:2`、Item `• `、H1 加粗斜体下划线、H2-H6 加粗、引用 `│ ` 缩进、行内代码高亮)。**用 Lcoder 青色系**,深色用自定义 profile,浅色走 glamour `LightStyleConfig`。
  - 验证:`renderer_test.go` 断言关键元素(代码块、列表、引用)渲染输出;明暗背景均 readable。
- [ ] **Step 2:** Chroma 语法高亮主题:Keyword/NameFunction/LiteralString/Comment/GenericInserted/Deleted 配色(青色系,diff 着色用 success/error 语义色)。
  - 验证:代码块语法高亮正确;diff 代码块 +/- 着色。
- [ ] **Step 3:** `components/user.go` 改为自适应明暗背景气泡:`› text`,`Padding(0,1)`,全宽;Light `#102A43` 文/`#DCE8F5` 底,Dark `#E6EEF8` 文/`#243447` 底。
  - 验证:`user_test.go` 明暗均可读;视觉权重提升。
- [ ] **Step 4:** `assistant.go:renderThinking` 改进:折叠态显示首行摘要 + "Thinking…" 而非纯静态;展开态按段落缩进,`colorDim`。
  - 验证:`assistant_test.go` 折叠/展开均正确。
- [ ] **Step 5(可选):** `sources.go` 识别文档末尾 `## Sources/References/参考文献` 段,剥离 glamour 主体,重新渲染为 OSC8 超链接列表(标题可见、URL 隐藏可点击)。
  - 验证:含 Sources 的 assistant 输出,链接可点击。

---

## Phase 3:工具展示增强 + diff 着色接入

**Files:**
- Modify: `pkg/tui/components/tool_format.go` — head/tail 截断、diff 接入、spinner 同步、长回复截断
- Modify: `pkg/tui/diff.go` — 确认/调整 `RenderDiff` 接入签名
- Modify: `pkg/tools/builtin/edit.go`、`write.go`(若需要) — 确认结果格式,必要时追加 diff 输出
- Modify: `pkg/tui/toolformat.go` — 多工具汇总(已有,确认)
- Modify: `pkg/tui/components/tool_format_test.go`

**参考:** Kocoro `toolformat.go:11-61`(keyArg)、`toolformat.go:154-178`(head/tail)、`toolformat.go:213-226`(长回复截断)、`app.go:1739-1753`(spinner 同步)

**前置确认(实现时第一步):** `edit`/`write` 工具结果是否含 unified diff?grep 当前无 "diff" 字样。若工具仅返回应用后内容或成功消息,需在工具层追加 old/new diff 输出,或在 TUI 层无法构造(因为 TUI 不持有 old 内容)。**风险点:可能需要改 builtin 工具返回 diff。**

- [ ] **Step 1:** 确认 edit/write 结果格式。若非 unified diff,在 `pkg/tools/builtin/edit.go`/`write.go` 返回值追加 `@@ ... @@` 格式的 unified diff 片段(或新增 `Diff` 字段)。对齐 memory [[config-align-test-prod-eval]]:工具输出格式变更须同步 test fixture。
  - 验证:edit/write 工具结果含可解析 diff;`pkg/tools/builtin` 测试通过。
- [ ] **Step 2:** `formatExpandedToolResult` 接入 diff:当 `toolName ∈ {edit,write,patch}` 且 content 含 diff 标志(行首 `+`/`-`/`@`)时,调 `ParseDiff`+`RenderDiff` 渲染(复用现有死代码)。
  - 验证:`tool_format_test.go` 断言 edit 工具展开视图为着色 diff。
- [ ] **Step 3:** `formatExpandedToolResult` 加 `truncateHeadTail`(head 8 行 / tail 4 行,中间 `… +N lines`),保留行结构(不 `strings.Fields` 拍平)。
  - 验证:长输出(stack trace/diff/grep)可读,中间截断有提示。
- [ ] **Step 4:** LLM 长回复截断:`maxResponseDisplayLines = 40`,超出提示 `... (N more lines - /copy for full text)`。
  - 验证:>40 行输出有截断提示。
- [ ] **Step 5:** `runningGlyph` 改为接收全局 spinner 帧参数(通过 `formatCompactToolResult`/`formatExpandedToolResult` 传入当前帧 index),而非 wall-clock 计算。
  - 验证:运行中工具行的 spinner 与全局 spinner 同步。
- [ ] **Step 6:** 扩展 `toolFriendlyLabels` 覆盖更多内置工具(grep/find 已有;补 todo_write、tool_search 等),未知工具保留 `name(args)`。

---

## Phase 4:持久 header + 品牌 swirl 启动动画

**Files:**
- Modify: `pkg/tui/header.go`、`pkg/tui/logo.go` — 持久 header 双列盒 + swirl 动画
- Create: `pkg/tui/brand.go` — Lcoder 品牌 swirl 位图 + 青色渐变渲染
- Modify: `pkg/tui/view.go`、`pkg/tui/model.go` — 主视图顶部加常驻 header;`updateSizes` 扣减 header 高度
- Modify: `pkg/tui/keys.go` — header tick 动画调度
- Modify: `pkg/tui/header_test.go`

**参考:** Kocoro `header.go:65-161`(双列盒)、`kocoro.go:15-85`(swirl 位图+渐变+半块渲染)、`header.go:53-63`(构建时间)

**品牌身份决策:** swirl 用 Lcoder 青(Nord 青蓝梯度 `#5E81AC`->`#88C0D0`->`#EBCB8B` 或纯青系),不照搬粉色 `#F40752->#F9AB8F`。品牌位图基于现有 `logo.go` 的 "L" 标志光栅化为 16×16,或设计新位图。

- [ ] **Step 1:** `brand.go` 定义 Lcoder 16×16 位图(从现有 logo 离线光栅化),青色对角渐变 shimmer(每帧 phase 偏移),半块渲染(`▀`/`▄`),`revealRows` 渐入(从上往下"画"出)。参考 `kocoro.go:36-85`。
  - 验证:`header_test.go` 断言动画 12 帧逐帧输出无错位;CJK 安全宽度。
- [ ] **Step 2:** `header.go` 持久 header 双列盒:左 swirl(启动动画期)/品牌标记(运行期),右列 model + cwd + session + 状态 + `built HH:MM`(二进制 mtime)。品牌色边框,顶边嵌入标题 `─ Lcoder CLI ─`。
  - 验证:运行期 header 常驻顶部;model/cwd 可见;构建时间正确。
- [ ] **Step 3:** `view.go` View() 在 `viewport` 之上拼接持久 header;`model.go:updateSizes` 将 `viewport.Height` 再扣减 header 高度。
  - 验证:resize 后 header + viewport + bottom 不溢出;`resize_test.go` 通过。
- [ ] **Step 4:** `keys.go` header tick:启动期 12 帧 ×80ms 动画;任意键跳过;运行期 header 静态(或 swirl 低频呼吸)。
  - 验证:启动动画流畅;跳过正常。

---

## Phase 5:状态动画(spinner + shimmer + composer 处理期可用)

**Files:**
- Modify: `pkg/tui/spinner.go` — 双速 spinner + 颜色渐变
- Create: `pkg/tui/shimmer.go` — 高斯 shimmer 状态文字(Lcoder 青)
- Modify: `pkg/tui/view.go` — processing 状态行用 shimmer
- Modify: `pkg/tui/keys.go`、`pkg/tui/events.go` — composer 处理期可用(follow-up 注入)
- Modify: `pkg/tui/spinner_test.go`

**参考:** Kocoro `app.go:2220-2272`(双速+shimmer)、`app.go:1729-1753`(composer 可用)、`app.go:1871-1885`(follow-up 注入)

**前置确认:** Lcoder 现有 steering/follow-up queue 机制(`pkg/agent` stateHolder)。若已支持运行中注入,Phase 5 只需 UI 侧保持输入框可用;若不支持,需 agent 侧改动(扩大范围)。

- [ ] **Step 1:** `spinner.go` 双速:100ms 推进 braille glyph + 颜色渐变(青色系 ANSI 256),5s 推进短语(现有 `spinnerPhrases` 保留)。
  - 验证:`spinner_test.go` 断言帧序列与颜色。
- [ ] **Step 2:** `shimmer.go` `renderWaveText`:状态短语每字符亮度按高斯衰减(`sigma=2.2`)从移动中心扩散,青色 RGB 插值(`#5E81AC`->`#88C0D0`)"呼吸"发光。`period = n+6` 留尾隙。
  - 验证:processing 状态行短语呼吸式发光,非静态。
- [ ] **Step 3:** `view.go:statusLineView` processing 态用 shimmer 渲染短语 + 右侧 `esc to interrupt · model Ns`。
  - 验证:运行中状态行有动效。
- [ ] **Step 4:** composer 处理期可用:确认/接入 steering queue,运行中输入框保持品牌色边框可用,Enter 注入 follow-up 到运行中 loop(不新启 turn)。
  - 验证:运行中输入文字 + Enter,消息作为 follow-up 注入;不阻塞。

---

## Phase 6:交互组件(ghost text + 菜单高亮 + paste/compact/cost)

**Files:**
- Modify: `pkg/tui/suggestion.go` — ghost text 接真实 LLM fork
- Modify: `pkg/tui/menu.go` — 模糊子序列 + 逐字符高亮
- Modify: `pkg/tui/paste.go` — 确认/补 paste 截断
- Create: `pkg/tui/compact.go` — `formatCompactResult`(压缩前后 token)
- Modify: `pkg/tui/view.go` — cost 友好显示
- Modify: `pkg/tui/model.go` — suggestion 代际戳防过期

**参考:** Kocoro `suggestion.go:18-81`、`app.go:3210-3228`(模糊子序列)、`app.go:3297-3314`(逐字符高亮)、`compact.go:122-132`、`cost.go:7-12`

**前置确认:** `paste.go` 是否已有 bracketed paste 截断(报告未细述);LLM client 是否可注入 suggestion 生成(用于 fork)。若 LLM fork 成本高,ghost text 可降级为本地启发式增强。

- [ ] **Step 1:** `suggestion.go` 接真实补全源:回合结束(`AgentEndEvent`)后异步 fork 一次 LLM 生成后续建议,仅在空输入框显示灰字 `↳ <建议>  Tab`;代际戳 `suggestionGen` 防过期。
  - 验证:回合结束后出现建议;Tab 接受填入;过期建议不弹出。
- [ ] **Step 2:** `menu.go` 加前缀匹配优先 + ≥3 字符模糊子序列回退(容忍缩写);命中字符逐个 accent 加粗高亮。`dropListSize` 固定行数防布局抖动。
  - 验证:`menu_test.go` 缩写命中;高亮正确。
- [ ] **Step 3:** `compact.go` `formatCompactResult`:`Context compressed: ~12,345 -> ~1,200 tokens` + 摘要(截 200 rune),`colorDim`。接入 `CompactionCommittedEvent`。
  - 验证:压缩后显示前后 token 对比。
- [ ] **Step 4:** `view.go:fmtCost` 改为 `>$0.01` 两位、`<$0.01` 四位精度。
  - 验证:微小成本不读成 `$0.00`。
- [ ] **Step 5:** 确认 `paste.go` 截断阈值(1000 rune),超长 paste 存 stash + 插入 `[Pasted text #N (M chars)]` 占位符。
  - 验证:`paste_test.go` 超长 paste 不淹没输入。

---

## Phase 7:错误展示 + providerpanel 统一 + statusline + system 日志分级

**Files:**
- Modify: `pkg/tui/events.go` — 错误独立展示(不混入对话流)
- Modify: `pkg/tui/providerpanel.go` — 圆角盒风格统一
- Modify: `pkg/tui/statusline.go`、`view.go` — statusline 信息增强
- Modify: `pkg/tui/components/system_log.go` — 严重级别区分
- Modify: `pkg/tui/confirm.go` — 确认 warn 色应用

**参考:** Kocoro `app.go:1323-1341`(友好错误+软提示)、`doctor.go:106-121`

- [ ] **Step 1:** `events.go:ErrorEvent` 不再 `addSystem(styleError(...))` 混入对话流,改为独立 error 块(置顶或固定区)+ 友好信息映射;`ErrMaxIterReached` 软提示 `colorDim` 斜体,不红字。
  - 验证:错误独立显示,不混入消息流;`events_test.go` 更新。
- [ ] **Step 2:** `providerpanel.go` 改为圆角 `colorFaint` 边框盒 + 选中项 `colorSelect` 标记,与 `cmdpanel`/`menu` 风格一致。
  - 验证:`providerpanel_test.go` 视觉快照;风格统一。
- [ ] **Step 3:** `statusline.go` 主视图状态行增强:确认 contextmgr 是否暴露 token 预算%;若可获取,加 context 预算% 指示;否则保持 mode+model+cost。
  - 验证:statusline 信息密度提升;无 contextmgr 依赖回归。
- [ ] **Step 4:** `components/system_log.go` 区分 error/warn/info 级别:error 用 `styleError`、warn 用 `styleWarn`、info 用 `styleDim`,而非全部 dim italic。
  - 验证:`system_log_test.go` 各级别着色正确。
- [ ] **Step 5:** 确认 `confirm.go` 审批提示用 `colorWarn`(Phase 0 新增),而非 `colorError`。
  - 验证:`confirm_test.go` 更新。

---

## 风险与需确认项

1. **Phase 3 edit/write diff:** 工具结果格式可能不含 unified diff,需改 builtin 工具输出。涉及工具层变更,须同步 test fixture([[config-align-test-prod-eval]])。
2. **Phase 5 composer 可用:** 依赖 agent steering queue 现状;若未支持运行中注入,需 agent 侧改动,范围扩大。
3. **Phase 6 ghost text:** LLM fork 增加成本与延迟;可降级为本地启发式。
4. **Phase 4 swirl 位图:** 需设计 Lcoder 品牌 16×16 位图(基于现有 logo 光栅化)。
5. **整体:** 不动 config 持久化,避免 test/生产/eval 三场景对齐问题。
6. **测试:** `go test $(go list ./... | grep -v 'reference/Shannon')` 与 `go vet` 同 exclusion;每 Phase 完成后跑全量 + 相关单测。

## 验证总标准

- 每 Phase 完成后:`go build ./...` + `go vet` + `go test ./pkg/tui/... -count=1 -race` 通过。
- 第一批(Phase 0-3)完成后:手动跑 TUI,验证配色切换、流式、消息渲染、工具 diff 四项核心展示效果。
- 第二批(Phase 4-7)完成后:验证品牌动画、状态 shimmer、ghost text、错误展示。
- 全程不修改 `reference/`。
