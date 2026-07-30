# TUI 已知问题

## 1. @file 补全在文本中间失效

**现象**：输入 `"改一下 @main.go 然后 @config.yml"` 时，只有最后一个 `@` 触发文件补全菜单。

**根因**：`filemenu.go:activeMention()` 使用 `strings.LastIndex(val, "@")` 只识别最后一个 `@`。

```go
// pkg/tui/filemenu.go:18
func activeMention(val string) (string, bool) {
    at := strings.LastIndex(val, "@")  // ← 只找最后一个
    ...
}
```

附带问题：`@` 前面必须是空格/Tab/换行，`hello@world` 不识别（设计如此，但可能不符合部分用户预期）。

**修复方向**：
- 根据 textarea 的光标位置查找离光标最近的 `@`，而非 `LastIndex`
- 光标在 `@word` 中间时触发该 `@` 的补全
- 光标在 `@word ` 之后（已补全完成）时不再触发

**影响文件**：`pkg/tui/filemenu.go`、`pkg/tui/keys.go`

---

## 2. 长文本不自动扩展输入行高度

**现象**：输入超过终端宽度的单行文本时，前面的文字消失，只显示末尾。输入框高度不自动增长。

**根因**：`input.go:desiredHeight()` 只按换行符 (`\n`) 计数，不按视觉软换行计数。

```go
// pkg/tui/input.go:55
func (m InputModel) desiredHeight() int {
    lines := strings.Count(m.textarea.Value(), "\n") + 1  // ← 只数 \n
    if lines < inputMinHeight { lines = inputMinHeight }
    if lines > inputMaxHeight { lines = inputMaxHeight }
    return lines
}
```

`bubbles/textarea` 内部支持软换行（word wrap），但 `SetHeight(N)` 限制了显示行数。当 `desiredHeight()` 返回 1 而文本已视觉换行为 3 行时，只渲染第 1 行对应的区域。

**修复方向**：
- 用 `lipgloss.Width()` 或文本宽度计算实际视觉行数
- 或者监听 `textarea.Width()` 和文本长度，估算换行数
- 公式：`visualLines = sum(ceil(runeWidth(line) / textareaWidth) for each line)`

**影响文件**：`pkg/tui/input.go`

---

## 3. VSCode PowerShell 终端：新问答时历史对话空白

**现象**：在 VSCode 的 PowerShell 集成终端中运行 Lcoder TUI，当新的一轮问答开始时，之前的聊天记录暂时显示为空白。过了一会儿可能恢复，也可能永久空白直到重新调整终端尺寸。

**根因**：VSCode PowerShell 终端的 ANSI 转义序列渲染延迟。Lcoder 的 `rebuildViewport()` 在流式输出期间频繁重建 viewport 内容：

```go
// pkg/tui/view.go:13
func (m *Model) rebuildViewport() {
    layouts := layoutComponents(...)
    content := buildVirtualContent(...)
    m.viewport.SetContent(content)  // ← 全量替换
}
```

每次 `SetContent` 都会触发终端全量重绘。VSCode 的 PowerShell 终端在处理快速连续的大量 ANSI 序列时，存在已知的渲染延迟和缓存不一致。具体表现为：
- `lipgloss` 样式的背景色/边框会产生复杂 ANSI 序列
- PowerShell 终端对这些序列的解析比 `cmd.exe` 或 `wt.exe` 慢
- 多个序列堆积时终端可能跳过中间帧，直接渲染最后一帧——而最后一帧可能是空白（因为内容正在重建中）

**修复方向**：
- 方案 A：在 VSCode 终端检测 (`$TERM_PROGRAM == "vscode"`) 时降低刷新频率
- 方案 B：每个新 Turn 开始时强制触发一次全量 viewport 同步
- 方案 C：使用 `lipgloss` 的 `Render` 一次性渲染所有内容，避免多次 `SetContent`

**影响文件**：`pkg/tui/view.go`、`pkg/tui/model.go`

**临时 workaround**：在 VSCode 中将终端改为 `cmd.exe` 或使用 Windows Terminal (`wt.exe`)。
