# 自研 Editor 替换 bubbles/textarea 评估报告

> 背景：TUI 产品化 spec（`tui-productization-optimization.md`）M3 的评估项。
> 结论先行：**现阶段不自研**。mention chips 行已覆盖输入框内高亮 80% 的价值；
> 自研 editor 的真实成本不在首版代码量，而在终端兼容的持续负担。

## 1. 候选能力盘点：哪些必须自研才能得到

| 能力 | 必须自研？ | 现状 |
|---|---|---|
| 输入框内 @mention 高亮 | 是（textarea 不支持分段样式） | 已由 chips 行替代（`437d457`）；Kimi Code TUI 同样未做框内高亮（仅首行 slash token 着色） |
| 原子 paste marker（占位符作为单个 grapheme 参与光标/删除） | 是 | 已有 placeholder 折叠 + 失配告警（`7f4cf32`），可用但非原子 |
| 光标感知补全、视觉行高、软换行 | 否 | 已在 textarea 上实现（`aec8c96`、`531ba83`） |
| IME 候选窗定位 | 是（需嵌入零宽光标标记，pi 的做法） | 无；CJK 输入可用，候选窗位置不精确 |
| slash 命令框内高亮 | 是 | 下拉菜单已覆盖 |

## 2. 成本估算

**参照物**：pi 的自研 editor ≈ 2300 行 TypeScript，外加终端按键协商（Kitty
CSI-u / modifyOtherKeys 回退、VSCode 终端解码补丁、退出前 stdin drain）。
Kimi Code 的 TUI 反而**没有**自研——它用 pi-tui 的 editor 并只做薄封装。

**Go 侧等价物**：1500–2500 行，需要重新实现：

- grapheme 分割（`rivo/uniseg` 可用）与光标移动语义
- 贪心词折行 + 视口滚动 + 高度自适应（我们刚在 textarea 上踩完这三个坑：
  共享 viewport 指针导致的 YOffset 滞留 `06c128f`、wrap 镜像 `531ba83`、
  滚动目标物化 `09c158f`——自研只是把同一类问题换个地方再修一遍）
- 光标闪烁、选区、鼠标
- Windows 终端按键协商与 IME

**持续负担（更重要的成本）**：每支持一种终端怪癖（VSCode CSI-u、Kitty
协议、Konsole Shift+滚轮……）都是一条 case。textarea 由 bubbles 社区分担
这份成本，自研后全部自负。

## 3. 中间路线：fork bubbles/textarea

若未来 mention 框内高亮成为真实需求，优先评估 fork 而非重写：

- 暴露内部 viewport 滚动控制（消除 `SetValue` 重置 hack）
- 在 `View()` 渲染管线插入 token 样式钩子（mention span → accent 样式）
- 预计改动 200–400 行，保留上游全部终端兼容资产
- 风险：跟上游分叉的合并成本；可先向上游提 issue/PR 探路

## 4. 过渡资产（已为自研铺路的现有件）

- `activeMentionAt`：mention span 计算（框内高亮的数据源）
- chips 行渲染路径：mention 解析结果的展示位
- `wrapRows`：已验证的折行镜像（自研 wrap 的对照样例）
- bracketed paste：bubbletea `KeyMsg.Paste` 标志已可用，可将 stash 触发从
  长度启发式细化为事件驱动（非 bracketed 终端仍需长度兜底）

## 5. 结论与触发条件

不自研。重新评估的触发条件（任一满足）：

1. 用户对 mention 框内高亮出现明确、重复的需求反馈
2. IME 候选窗定位问题被实际报告（中文/日文输入场景）
3. bubbles/textarea 停止维护，被迫 fork

触发时优先走 fork 中间路线（§3），以 200–400 行的代价换取框内高亮，
而不是一次性吃下 2000+ 行和全部终端兼容负担。
