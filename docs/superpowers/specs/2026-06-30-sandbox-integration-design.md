# 沙箱接入工具系统设计 (Sandbox Integration — Plan 2)

- 日期: 2026-06-30
- 状态: 设计已批准, 待写实现计划
- 范围: 把已合并的 `pkg/sandbox` 接入运行时, 让 bash / file / http 工具在 sandbox 策略下执行
- 前置: Plan 1 (`docs/superpowers/specs/2026-06-30-sandbox-design.md`) 已实现并合并到 master

## 1. 目标与非目标

### 目标
- 把 `pkg/sandbox`(当前是未接线的孤岛)接入工具系统与启动装配链。
- bash 工具经 `sb.Exec` 执行; file 工具执行前经 `sb.Filesystem().Check`; http 工具经 `sb.Network().DialContext`。
- 默认 backend = `passthrough` → 纯接线、**零行为变化**; 用户在 `lcoder.yaml` 配 `soft-limit` 才启用约束。
- 注入机制对**第三方工具扩展零影响**。

### 非目标(本期)
- 不注入 LLM provider(openai/anthropic)与 catalog 刷新的网络出口 —— 它们是宿主基础设施, 直连, 不受 agent 的网络 allowlist 约束(避免 `default=deny` 掐断 agent↔LLM)。
- 不接 MCP —— MCP 走 stdio 子进程管道, 不走网络, `DialContext` 对它无意义。
- 不改 `pkg/sandbox` 本身的接口或后端实现(Plan 1 已完成且已评审)。
- 不实现 Container / Remote 后端真实逻辑。

## 2. 决策记录

| 决策点 | 选择 | 理由 |
|---|---|---|
| 注入方式 | **可选接口 `SandboxAware`** | 不改 `Factory` 签名 → 第三方扩展零影响; 敏感工具 opt-in 实现接口, registry 探测注入。改签名会破坏扩展 API 并强制不相关工具背依赖。 |
| 网络范围 | **仅 http 工具** | 严格遵循 Plan 1 spec §7; LLM/catalog 是宿主基础设施; churn 最小; 避免 default-deny 自掐。 |
| 默认 backend | **passthrough** | 接线正确性与策略生效解耦; 零回归风险。 |
| 任务范围 | **一个 plan 全做** | 三块共享同一注入骨架, 默认 passthrough 全程零回归, 无需切分。 |

## 3. 注入机制(核心)

```go
// pkg/tools/base.go
type Factory func(cwd string) Executable        // 不变, 扩展零影响

// SandboxAware 由需要 sandbox 的工具可选实现。registry 注册时探测并注入。
type SandboxAware interface {
    UseSandbox(sb sandbox.Sandbox)
}
```

- `Registry` 增加 `sb sandbox.Sandbox` 字段; 构造函数签名改为 `NewRegistry(cwd string, sb sandbox.Sandbox) *Registry`(`sb` 可为 `nil`)。
- `Registry.Register(name, exec)` 末尾统一探测注入:
  ```go
  if r.sb != nil {
      if sa, ok := exec.(SandboxAware); ok {
          sa.UseSandbox(r.sb)
      }
  }
  ```
- 所有注册路径(`RegisterBuiltinFactories` / http 工具 `Register` / MCP `RegisterTools`)最终都经 `Register`, 因此自动覆盖。MCP / 普通工具不实现 `SandboxAware` → 自动跳过。
- **依赖方向**: `pkg/tools → pkg/sandbox`(单向)。已确认 `pkg/sandbox` 不 import `pkg/tools`, 无循环依赖。

## 4. 各工具改动

### 4.1 bash (`pkg/tools/builtin/bash.go`)
- 结构体加 `sb sandbox.Sandbox` 字段; 实现 `UseSandbox(sb)`。
- `Execute` 改调 `sb.Exec(ctx, sandbox.ExecSpec{Command: command, Cwd: cwd, Env: os.Environ(), Timeout: ...})`, 用 `result.Combined()` 保持现有返回契约(原 `CombinedOutput` 语义)。
- `sb == nil` 时退化为当前 `exec.CommandContext(...).CombinedOutput()`(防御路径, 供无 sandbox 的单测)。

### 4.2 file 工具 ×6 (read/write/edit/ls/find/grep)
- 抽公共 helper, 收敛当前 6 处重复的"取 path → IsAbs/Join/Clean"逻辑并追加越界校验:
  ```go
  // pkg/tools/builtin/fspath.go (新文件)
  func resolveAndCheck(cwd string, sb sandbox.Sandbox, rawPath string, op sandbox.FSOp) (string, error) {
      path := rawPath
      if !filepath.IsAbs(path) {
          path = filepath.Join(cwd, path)
      }
      path = filepath.Clean(path)
      if sb != nil {
          if err := sb.Filesystem().Check(path, op); err != nil {
              return "", err
          }
      }
      return path, nil
  }
  ```
- 每个工具加 `sb` 字段 + `UseSandbox`; 把那 4 行路径解析替换为一次 `resolveAndCheck` 调用, 传对应 `op`: read/ls/find/grep → `FSRead`; write → `FSWrite`; **edit → `FSWrite`**(edit 最终要写, 以写权限做单次校验; 只读不可写的路径对 edit 即应拒绝)。
- **find / grep 在 `WalkDir` 回调内对遍历到的每个子路径 `p` 也调 `sb.Filesystem().Check(p, FSRead)`**, 越界子路径跳过, 防止经根内入口遍历到根外(经符号链接)。

### 4.3 http 工具 (`pkg/tools/http.go`)
- `HTTPExecutable` 实现 `UseSandbox(sb)`: 把 `h.client.Transport` 替换为 `&http.Transport{DialContext: sb.Network().DialContext}`。
- 既有 `h.client.Do(req)` 调用点不变, 透明走注入的 DialContext。

## 5. 配置

### 5.1 config 结构
`pkg/config/config.go` 顶层 `Config` 增加字段:
```go
Sandbox SandboxConfig `yaml:"sandbox"`
```
新增 yaml 友好的结构(`pkg/config` **不依赖** `pkg/sandbox`):
```go
type SandboxConfig struct {
    Backend      string                 `yaml:"backend"`       // "" → passthrough
    EnvAllowlist []string               `yaml:"env_allowlist"`
    Network      SandboxNetworkConfig   `yaml:"network"`
    Filesystem   SandboxFilesystemConfig `yaml:"filesystem"`
    Limits       SandboxLimitsConfig    `yaml:"limits"`
}
type SandboxNetworkConfig struct {
    Default string   `yaml:"default"`  // "deny" | "allow"
    Allow   []string `yaml:"allow"`
}
type SandboxFilesystemConfig struct {
    Readable []string `yaml:"readable"`
    Writable []string `yaml:"writable"`
}
type SandboxLimitsConfig struct {
    MaxMemoryMB    int `yaml:"max_memory_mb"`
    MaxCPUSeconds  int `yaml:"max_cpu_seconds"`
    MaxOutputBytes int `yaml:"max_output_bytes"`
}
```
`DefaultConfig()` 中 `Sandbox.Backend` 留空(→ passthrough)。

### 5.2 映射函数
由装配层(`cmd/lcoder`)提供翻译函数, 把 `config.SandboxConfig` + `cwd` 映射成 `sandbox.Config`, 注入 `ProjectRoot = cwd`:
```go
func toSandboxConfig(c config.SandboxConfig, projectRoot string) sandbox.Config
```
此函数把字符串 `Default`("deny"/"allow")映射成 `sandbox.NetworkConfig.DefaultAllow` 布尔等。

### 5.3 配置示例
`configs/lcoder.yaml` 增加注释化的 `sandbox:` 段:
```yaml
# sandbox:
#   backend: passthrough        # passthrough | soft-limit | container(stub) | remote(stub)
#   env_allowlist: [PATH, HOME, LANG, SHELL]
#   network:
#     default: deny
#     allow: ["api.github.com:443"]
#   filesystem:
#     writable: ["."]
#     readable: ["."]
#   limits:
#     max_memory_mb: 512
#     max_cpu_seconds: 60
#     max_output_bytes: 1048576
```

## 6. 装配链

`cmd/lcoder/main.go` 的 `prepareAgent`(当前 line 146 附近):
```go
sb, err := sandbox.New(toSandboxConfig(cfg.Sandbox, cwd))   // cwd 来自 runRoot 的 os.Getwd()
if err != nil {
    return nil, fmt.Errorf("init sandbox: %w", err)
}
registry := tools.NewRegistry(cwd, sb)
if err := registry.RegisterBuiltinFactories(cwd); err != nil { ... }   // 注册即注入
// http / mcp 注册同样经 registry.Register → 自动注入
```
- `ProjectRoot` 取自已有的 `cwd`(`os.Getwd()`, `main.go:269`), 不新增配置概念。
- `agentsetup/setup.go` 不涉及工具装配(只管 prompt + context), 本期不改。

## 7. 错误处理与默认行为
- `sandbox.New` 失败(如 `backend: container` 未实现)→ `prepareAgent` 返回明确错误, 启动中止。
- 默认 passthrough: `Exec` 等价当前 bash; `Check` 恒放行; `DialContext` 直连 → **零回归**。
- `sb == nil`(单测不注入)路径在每个工具内保留为退化分支。

## 8. 测试策略
- **registry**: `SandboxAware` 工具被注入; 普通工具不受影响; `sb == nil` 不 panic。
- **bash**: 用 `FakeSandbox` 验证 `Execute` 调 `sb.Exec` 且 `ExecSpec` 字段正确; passthrough 下 stdout/Combined 输出契约不回归。
- **file 工具**: `FakeSandbox` 的 `Check` 返回错误时工具返回错误且不触碰磁盘; 放行时正常; find/grep 遍历子路径被 Check(越界子路径跳过)。
- **http**: `UseSandbox` 后 `client.Transport.DialContext` 指向注入的策略。
- **config**: yaml 解析出 `sandbox` 段; `toSandboxConfig` 正确注入 `ProjectRoot` 并映射 `default: deny` → `DefaultAllow=false`。
- **装配集成**: 默认配置下 backend=passthrough, 全链路 bash/file/http 零回归(集成测试)。

## 9. 任务切分(预告, 细节留给 writing-plans)

约 6 个 TDD 任务:
1. `SandboxAware` 接口 + `Registry.sb` 字段 + `Register` 探测注入 + `NewRegistry` 签名。
2. bash 接入(`UseSandbox` + `Execute` 走 `sb.Exec` + 退化分支)。
3. file 公共 helper `resolveAndCheck` + 6 个工具改造(含 find/grep 子路径 Check)。
4. http 工具 `UseSandbox`(替换 Transport.DialContext)。
5. `config.SandboxConfig` + 顶层字段 + `DefaultConfig` + `configs/lcoder.yaml` 段。
6. `toSandboxConfig` 映射 + `prepareAgent` 装配 + 集成测试。

## 10. 已知局限
- 仅 http 工具受网络策略约束; bash 子进程发起的网络仍是 best-effort(Plan 1 已记录), 本期不变。
- LLM/catalog 出口不受 sandbox 约束 —— 有意为之(宿主基础设施), 文档标注。
- 默认 passthrough 意味着开箱无任何约束; 安全收益需用户显式配置 `soft-limit`。
