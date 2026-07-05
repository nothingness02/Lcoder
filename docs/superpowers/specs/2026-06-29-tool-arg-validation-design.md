# 工具执行前参数校验 设计文档

> 注:本项目 `docs/` 已 gitignore,本文档仅本地留存,不纳入版本控制。

## 目标

为所有工具调用增加**执行前的参数校验**:在工具真正运行之前,依据其 `Definition().Parameters`(JSON Schema)检查 LLM 传入的参数(必填字段、顶层字段类型)。校验不通过时,不向 TUI 发出任何工具事件,而是把一条 LLM 友好的纠正提示作为错误 `tool_result` 反馈回上下文,由 LLM 在下一轮用正确参数重试。

**核心收益:** 用户在 TUI 实时只看到最终成功的工具块,LLM 的参数失误不再制造可见的红色失败块。

## 背景与现状

- 参数校验目前是"散装"的:每个工具在自己的 `Execute` 内手写类型断言(如 `pkg/tools/builtin/edit.go` 的 `"missing path"`、`"missing edits"`、`"oldText not found or not unique"`),返回 Go `error`。
- `pkg/tools/registry.go` 的 `Registry.Execute` 把该 error 包成 `models.NewToolResultError(...)`(`isError=true`),作为 `tool_result` 进上下文,下一轮 LLM 自然重试。
- 但 `pkg/agent/toolexec.go` 的 `executeOneToolCall` **第一行**就 `Emit(ToolExecutionStartEvent)`,TUI(`pkg/tui/events.go` 的 `handleEvent`)随即建一个 `blockTool`,结束时 `ToolExecutionEndEvent` 把它标红(`toolErr=true`)。结果:用户看到一个失败块 + 下一轮一个成功块。
- `Definition().Parameters` 这份 JSON Schema 当前**只发给 LLM,从不用于校验进来的 args**。

## 架构

两个改动单元,职责分离:

1. **`pkg/tools/validate.go`(新增,纯函数,无 LLM 依赖)** —— 校验逻辑本体。
2. **`pkg/agent/toolexec.go`(修改 `executeOneToolCall`)** —— 在第一个事件 emit 之前调用校验。

校验放在 `pkg/tools` 而非 agent 层,因为它无状态、无外部依赖,可独立单测;且 MCP / 扩展工具的 `Definition().Parameters` 同样适用,天然统一受益。

### 单元 1:`ValidateArgs`

```go
// ValidateArgs checks args against a tool's JSON-Schema parameters.
// It verifies that required fields are present and that the top-level
// type of each provided field matches its declared JSON type.
// It returns nil when args are valid, or an LLM-friendly error describing
// the first problem found. Schemas that are not a recognizable object
// schema (no "properties") pass through unchecked.
func ValidateArgs(def models.ToolDefinition, args map[string]any) error
```

校验规则(深度 = **必填 + 顶层类型**):

- 从 `def.Parameters` 取 `properties`(`map[string]any`)与 `required`。若无 `properties` → **优雅降级**,返回 nil(不阻断 MCP/异形工具)。
- **必填检查:** `required` 中每个名字必须是 `args` 的 key。缺失 → 报错。
  - `required` 可能是 `[]string` 或 `[]any`(JSON 反序列化产物),两种都要处理。
- **类型检查:** 对 `args` 中出现、且其 `properties[field]` 声明了 `"type"` 的字段,校验 Go 类型与 JSON 类型一致:
  | JSON type | 接受的 Go 类型 |
  |---|---|
  | `string` | `string` |
  | `number` / `integer` | `float64`、`int`、`int64`、`json.Number` |
  | `boolean` | `bool` |
  | `array` | `[]any` |
  | `object` | `map[string]any` |
  - 数值宽松接受(provider 反序列化差异);`integer` 不强校验整数性(YAGNI)。
  - schema 未声明 `type` 的字段不检查类型。
  - **不递归**进数组 items / 对象嵌套字段 —— 顶层为界。
- 错误信息面向 LLM 自纠正,首个问题即返回。示例:
  - `invalid arguments for "edit": missing required field "edits" (expected array); provided: path`
  - `invalid arguments for "read": field "line" must be number, got string`

### 单元 2:`executeOneToolCall` 接入

将校验插入到**任何事件 emit 之前**(这是"实时无感"的关键):

改动后的顺序:
```
1. args nil 归一(原本就有,从 emit 之后上移到最前)
2. 若 a.registry.Get(call.Name) 命中:
     err := tools.ValidateArgs(exec.Definition(), args)
     若 err != nil:
       // 不 emit ToolExecutionStart/End,也不 emit tool_result 的 MessageStart/End
       return a.makeToolResultMessage(call, models.NewToolResultError(err.Error()), true)
3. 校验通过(或工具不存在)→ 照旧:emit ToolExecutionStart → BeforeToolCall hook
   → registry.Execute → AfterToolCall hook → emit ToolExecutionEnd → emit MessageStart/End
```

要点:

- **工具不存在(unknown tool)不归此机制管。** `registry.Get` 未命中时跳过校验,落入既有路径(`registry.Execute` 返回 `Unknown tool` 错误,保持当前可见行为)。unknown tool 不是"参数错误"。
- 校验失败返回的错误 `tool_result` 由上层 `executeSequential` / `executeParallel` 的 `a.appendMessage(resultMsg)` 追加进上下文 → 喂回 LLM → 下一轮自动重试。
- 因为失败分支**完全不 emit**,`pkg/tui/events.go` 的 `handleEvent` 收不到 `ToolExecutionStart/End`,实时 TUI 不出现失败块。

## 数据流与边界(取舍)

- 失败的 assistant 工具调用 + 错误 `tool_result` **仍留在上下文**(LLM 纠正所需)。因此 **session reload 时 `pkg/tui/events.go` 的 `blocksFromMessages` 会重新渲染出失败调用**。这是已确认接受的取舍:**本期只做"实时无感",不处理 reload 隐藏**(不引入抑制标记)。
- **不引入** auto-repair(确定性字段别名/类型矫正)。
- **不引入** LLM 内循环重答(校验失败时的二次 LLM 纠正)。
- 以上两者是后续可选层,超出本期范围。

## 测试

### `pkg/tools/validate_test.go`(表驱动)

覆盖:
- 缺必填字段 → 报错且信息含字段名。
- 顶层类型不符(string vs number、object vs array 等)→ 报错。
- 数值宽松:`float64` / `int` / `json.Number` 对 `number` 均通过。
- 空 schema / 无 `properties` → 降级通过(nil)。
- 有 `properties` 但无 `required` → 仅类型校验,缺字段不报错。
- `required` 为 `[]any`(JSON 形态)与 `[]string` 两种都正确解析。
- 嵌套不深入:数组 items 内的字段错误**不**被顶层校验捕获(确认边界)。
- 合法参数 → nil。

### `pkg/agent`(toolexec 层行为)

- 构造一个带 schema 的 fake 工具 + 事件总线订阅。
- 传入**非法** args 调用执行路径 → 断言:
  - **不产生** `ToolExecutionStartEvent` / `ToolExecutionEndEvent`。
  - 返回的 `tool_result` 消息 `IsError == true`,内容含校验提示。
- 传入**合法** args → 断言:正常 emit start/end,工具被执行。

## 文件清单

- 新增:`pkg/tools/validate.go`
- 新增:`pkg/tools/validate_test.go`
- 修改:`pkg/agent/toolexec.go`(`executeOneToolCall`:上移 args 归一,emit 前插入校验分支)
- 新增/修改:`pkg/agent` 下针对 toolexec 校验行为的测试(可置于现有 `loop_test.go` 同包或新建 `toolexec_test.go`)
