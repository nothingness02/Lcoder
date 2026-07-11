# Skill 延迟加载设计（Pi 式 Progressive Disclosure）

**日期**: 2026-07-11  
**状态**: 已实现  
**决策**: 采用 Pi agent 的 progressive disclosure 模式加载 skills；active skill 不进入静态上下文，以动态消息注入。

## 1. 背景与目标

### 当前问题

Lcoder 启动时会扫描所有 skill 目录，解析每个 `SKILL.md` 的完整内容，并将全部 skills 渲染进 `BlockSkills` 系统提示块。当用户拥有较多 skills 时，这会导致：

- 系统提示膨胀，挤压对话上下文；
- 无关 skill 的 steps/examples 常驻上下文，干扰模型判断；
- 每次 `/skill:name` 或 `AutoDetect` 触发时，由于所有内容都在同一个 block 中，缓存效率不佳。

### 设计目标

1. **未激活的 skill 不加载完整内容**：启动时只保留轻量元数据；
2. **保留发现与触发能力**：目录信息足够支持 `/skill:name` 补全和 `AutoDetect`；
3. **激活后按需加载完整内容**：与 Pi agent 一致，通过读取源文件或工具把完整 skill 注入上下文；
4. **保持结构清晰与缓存友好**：目录与激活内容分离，系统提示前缀默认保持精简。

## 2. 高层设计：Pi 式 Progressive Disclosure

借鉴 Pi agent 的实现：

| 阶段 | 加载内容 | 时机 | 常驻上下文？ |
|---|---|---|---|
| **发现（Discovery）** | 扫描目录，读取 `SKILL.md` 的 YAML frontmatter | 启动时 | 是（仅元数据） |
| **目录（Catalog）** | `name` + `description` + `keywords` | 启动时注入系统提示 | 是 |
| **激活（Activation）** | 完整 `SKILL.md` body | `/skill:name` 或 `AutoDetect` 命中后 | 是，作为独立动态块 |
| **执行（Execution）** | skill 关联的脚本/参考文件 | 执行阶段按需读取 | 否 |

## 3. 数据模型变更

### 3.1 新增 `SkillMeta`

```go
// pkg/skills/skill.go

// SkillMeta 是轻量目录项，启动时全量加载，常驻上下文用于发现与匹配。
type SkillMeta struct {
    Name        string
    Description string   // 市场标准字段
    Keywords    []string // 用于 AutoDetect
    Tags        []string // 可选分组
    Source      string   // SKILL.md 绝对路径，延迟加载入口
}

// Skill 是完整 skill，只在命中后解析。
type Skill struct {
    SkillMeta
    Body string // frontmatter 之后的完整 Markdown 正文
}
```

### 3.2 `SKILL.md` frontmatter

采用市场通用格式，只要求 `name` 和 `description`：

```yaml
---
name: writing-plans
description: 在多步实现任务前生成可执行的实现计划
keywords: [plan, roadmap, implementation, spec]
tags: [process]
---
```

当 `keywords` 缺失时，从 `name` 和 `description` 自动分词提取（复用 `pkg/skills/auto.go` 的 `tokenize`）。

**不再兼容**旧有的 `when_to_use`、`steps`、`examples`、`output_format` 等 frontmatter 字段；正文不再被拆分为固定章节，而是作为自由 Markdown 整体保留。

## 4. Loader 拆分

`pkg/skills/loader.go` 拆为两个阶段，不再提供全量 eager-loading 的入口：

```go
// LoadCatalog 扫描目录，仅解析 frontmatter，返回轻量目录。
func LoadCatalog(paths []string) ([]SkillMeta, error)

// LoadSkill 根据 Source 路径读取完整 SKILL.md，返回 Skill。
func LoadSkill(source string) (Skill, error)
```

- `prepareAgent` 使用 `LoadCatalog` 生成目录块；
- `/skill:name` 和 `AutoDetect` 命中后调用 `LoadSkill` 读取完整正文。

## 5. 上下文块设计

复用并调整现有 block 语义：

| 块 | Kind | Stability | Priority | 内容 | 说明 |
|---|---|---|---|---|---|
| 系统提示 | `BlockSystem` | Static | 100 | 固定人设 | 不变 |
| Mode | `BlockMode` | Stable | 95 | 当前 mode | 不变 |
| **Skill 目录** | `BlockSkills` | Stable | 90 | 所有 skill 的 name + description + keywords | 精简常驻 |
| Project docs | `BlockProjectDocs` | Stable | 80 | CLAUDE.md / AGENTS.md | 不变 |
| Memory | `BlockMemory` | Stable | 75 | 记忆 | 不变 |
| User profile | `BlockUserProfile` | Stable | 70 | 用户画像 | 不变 |
| Recent | `BlockRecent` | Dynamic | 100 | 近期消息 | 不变 |


## 6. 激活流程

### 6.1 启动阶段

`cmd/lcoder/main.go:prepareAgent` 调整：

```go
skillPaths := append(skills.DefaultPaths(cwd), extMgr.SkillDirs()...)
catalog, _ := skills.LoadCatalog(skillPaths)
skillsBlock := skills.ToCatalogBlock(catalog)

// 传入 agentsetup.NewContextManager
mgr := agentsetup.NewContextManager(..., skillsBlock, ...)

// 保存目录供后续触发查询
setup.loadedSkillCatalog = catalog
```

`agentsetup.NewContextManager` 中：

```go
if skillsBlock != "" {
    mgr.SetBlock(contextmgr.NewBlockWithCacheHint(
        contextmgr.BlockSkills, "skills",
        contextmgr.StabilityStable, 90,
        contextmgr.CacheHintBreakpoint,
        models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: skillsBlock}),
    ))
}
// BlockSkillActive 初始为空，不设置
```

### 6.2 手动触发 `/skill:name`

1. `ParseManualTrigger` 解析 skill name；
2. `FindByName(catalog, name)` 查找 `SkillMeta`；
3. 调用 `LoadSkill(meta.Source)` 读取完整 `SKILL.md`；
4. 通过 `ExpandManualTrigger(skill, rest)` 生成 system + user 消息对，追加到当前会话。

```go
skill, err := skills.LoadSkill(meta.Source)
if err != nil {
    return fmt.Errorf("load skill %q: %w", name, err)
}
initialMessages := skills.ExpandManualTrigger(skill, rest)
```

如果该 skill 已激活，则追加新的消息对；旧的激活消息仍保留在会话历史中。

### 6.3 自动检测 `AutoDetect`

`AutoDetect` 对 `[]SkillMeta` 打分（而非完整 `[]Skill`）：

```go
func AutoDetect(prompt string, catalog []SkillMeta) (MatchScore, bool)
```

命中后同样走 `LoadSkill` → `ExpandManualTrigger` 流程。

### 6.4 新增内置工具 `read_skill`（可选但推荐）

为完全对齐 Pi 的按需读取模式，新增内置工具：

```go
// read_skill(name string) -> string
// 根据 name 从 catalog 查找 Source，读取并返回完整 SKILL.md 内容。
```

使用场景：
- 模型在对话中自行决定调用 `read_skill` 获取某个 skill；
- 手动触发和自动检测在内部也可以复用该工具逻辑。

**注意**：与 Pi 一致，完整 skill 内容不进入静态 system prompt，而是作为动态消息注入。

## 7. 渲染格式

### 7.1 目录块格式

```text
You have access to the following skills. Use them when appropriate:

- writing-plans: Generate an implementation plan before multi-step work
  keywords: plan, roadmap, implementation, spec
- code-review: Review code for bugs and style issues
  keywords: review, audit, correctness
```

### 7.2 激活内容格式

完整保留 `SKILL.md` 正文：

```text
You are now using the {name} skill.

Purpose: {description}

{body}
```

`body` 是 frontmatter 之后的全部 Markdown 内容，包含步骤、示例、参数说明、脚本引用等。不再拆分为 `Steps/Examples/OutputFormat` 小节。

## 8. 缓存与性能考量

### 8.1 默认状态（无 skill 激活）

- 系统提示仅包含：人设 + mode + skill 目录（通常 < 500 tokens）。
- 目录块标记 `CacheHintBreakpoint`，前缀缓存高效。

### 8.2 激活 skill 时

- 完整 skill 内容通过 `ExpandManualTrigger` 以 system + user 消息对的形式追加到 `BlockRecent`。
- 由于它不在 system prompt 中，**不会使静态 system prompt 缓存失效**。
- 这与 Pi 的实现一致：静态上下文只保留目录，完整 skill 按需进入动态对话。

### 8.3 未来优化

若后续 skill 数量达到 50+，可引入 `skill_search`/`read_skill` 工具，让模型自行决定加载哪个 skill，进一步减少目录体积。

## 9. 配置扩展（可选）

```yaml
skills:
  auto_detect: true       # 是否启用 AutoDetect
  default_active: []      # 启动时就激活的 skill 名列表
```

MVP 阶段可不引入配置，默认行为即为：目录常驻、完整内容按需激活。

## 10. 迁移说明

本设计**不兼容**旧版 skill 格式：

- 旧的 `when_to_use` frontmatter 字段不再支持，统一使用 `description`；
- 旧的 `steps`、`examples`、`output_format` frontmatter 字段不再支持；
- 正文不再被拆分为固定章节，而是作为自由 Markdown 整体保留；
- `pkg/skills.Load` 已删除，调用方需使用 `LoadCatalog` 或 `LoadSkill`；
- `BlockSkills` 语义从“完整 skills”变为“skills 目录”；
- TUI 与 CLI 内部字段从 `loadedSkills []skills.Skill` 改为 `loadedSkillCatalog []skills.SkillMeta`。

存量 skill 需要把 `when_to_use` 改为 `description`，并把步骤/示例移到正文中。Lcoder 自带的 `security-review` skill 已同步更新。

## 11. 测试计划

1. **单元测试**
   - `LoadCatalog` 仅解析 frontmatter，不读取 body；
   - `LoadSkill` 正确读取完整 body；
   - `AutoDetect` 基于 `SkillMeta.Description` + `Keywords` 正确打分；
   - `ToCatalogBlock` 输出仅包含 name/description/keywords；
   - 激活后渲染包含完整 body。

2. **集成测试**
   - 启动后 `BlockSkills` 只包含目录；
   - `/skill:name` 后会话追加 system + user 消息对；
   - `BlockSkills` 在激活前后保持稳定。

3. **回归测试**
   - 无 skill 时系统提示 token 数显著下降；
   - 现有手动触发和自动检测功能仍可用。

## 12. 参考

- [Pi Skills Docs](https://pi.dev/docs/latest/skills)
- [Pi SDK Docs](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/sdk.md)
- [OpenCode skill bloat issue](https://github.com/anomalyco/opencode/issues/20647)
- [OpenCode lazy skill plugin](https://github.com/zenobi-us/opencode-skillful)
- [Claude Code lazy loading feature request](https://github.com/anthropics/claude-code/issues/16160)
- [How Prompt Caching Works in Claude Code](https://www.claudecodecamp.com/p/how-prompt-caching-actually-works-in-claude-code)

## 13. 待实现规划

下一步：使用 `superpowers:writing-plans` 将本设计拆分为可执行的实现步骤、文件变更清单和验证标准。
