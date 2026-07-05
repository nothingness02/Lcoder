# 沙箱机制设计 (Sandbox)

- 日期: 2026-06-30
- 状态: 设计已批准, 待写实现计划
- 范围: 为 agent 系统抽象一个可切换真实后端的沙箱接口, 隔离敏感的环境交互(命令执行 + 网络)与不可预测的 agent 调用

## 1. 目标与非目标

### 目标
- 提供一个**抽象的 `Sandbox` 接口**, 接口之下可切换不同的真实隔离后端而不改动调用方。
- 隔离两类敏感环境交互: **命令执行(bash)** 与 **网络访问(http 工具 / MCP / 子进程)**。
- 首期落地 `Passthrough` 与 `SoftLimit` 两个后端; `Container` 与 `Remote` 仅预留接口(stub), 不引入 Docker 等外部依赖。

### 非目标(本期)
- 不实现 Container / Remote 后端的真实逻辑(仅接口 + stub)。
- 不在 `SoftLimit` 中引入 Linux network namespaces / eBPF / iptables 等内核级网络控制(见 §6 决策记录)。
- 不改动现有 `permissions.Engine` 与 hooks 的准入语义(沙箱与之正交, 见 §5)。

## 2. 核心洞察: 两个执行平面

系统中的环境交互天然分为两个平面, 其可强制性(enforceability)有本质差异。把这一差异**显式建模进接口**, 是本设计的核心:

- **进程内平面 (in-process)**: Go 工具自身发起的交互 —— `http` 工具、MCP 客户端的网络; `read`/`write`/`edit`/`ls`/`find`/`grep` 的文件访问。这些可以被注入的策略对象(定制 `DialContext`、路径检查器)**精确强制**, 且在**所有后端(含 `SoftLimit`)都是真实的**。
- **子进程平面 (subprocess)**: `bash` 工具 `sh -c` 派生的子进程, 直接向 OS 发起系统调用, 绕过 Go 运行时。只能靠 OS / 容器边界约束。`SoftLimit` 对此**只能尽力而为(best-effort)**; 真实强制需 `Container` / `Remote`。

> 接口的"不对称性"不是缺陷, 而是这两个平面的客观差异。显式建模后, 实现者与调用方都清楚每个后端在每个平面上**到底强制了什么**。

## 3. 接口设计 (`pkg/sandbox`)

```go
package sandbox

// Sandbox 隔离敏感的环境交互。实现从 no-op passthrough 到 OS 软限制到容器。
// 调用方(工具)通过本接口交互, 不感知具体后端。
type Sandbox interface {
    // Exec 在隔离策略下运行命令(子进程平面), 替代 bash 的裸 exec.Command。
    Exec(ctx context.Context, spec ExecSpec) (ExecResult, error)
    // Network 返回网络策略, 同时服务进程内平面(DialContext)与子进程平面(SubprocessConfig)。
    Network() NetworkPolicy
    // Filesystem 返回文件策略, 与 Network 对称。
    Filesystem() FilesystemPolicy
    // Name 标识后端, 用于日志 / telemetry。
    Name() string
}

// ExecSpec 描述一次受控命令执行。
type ExecSpec struct {
    Command string        // sh -c 的命令行
    Cwd     string
    Env     []string      // 调用方传入; 后端按 allowlist 进一步过滤(默认剥离凭证类)
    Timeout time.Duration
    Limits  ResourceLimits
}

type ResourceLimits struct {
    MaxMemoryMB    int
    MaxCPUSeconds  int
    MaxOutputBytes int
}

// ExecResult 分离 stdout / stderr, 便于上层对 LLM 区分正常数据流与错误流。
// Combined() 兜底 Bash 工具当前 CombinedOutput 契约。
type ExecResult struct {
    Stdout   string
    Stderr   string
    ExitCode int
    TimedOut bool
}

func (r ExecResult) Combined() string { /* stdout + "\n" + stderr, 去除多余空白 */ }

// NetworkPolicy 显式区分两个平面。
type NetworkPolicy interface {
    // 进程内平面: 注入到 http.Client.Transport.DialContext 与 MCP 客户端。真实强制。
    DialContext(ctx context.Context, network, addr string) (net.Conn, error)
    // 子进程平面: Exec 据此构造底层网络隔离。
    //   Passthrough -> 空(不隔离)
    //   SoftLimit   -> proxy-env hint(HTTP_PROXY/HTTPS_PROXY 指向进程内过滤代理), best-effort
    //   Container   -> --network 相关标志
    SubprocessConfig() SubprocessNetConfig
}

// FilesystemPolicy 与 NetworkPolicy 完全对称。
type FilesystemPolicy interface {
    // 进程内平面: read/write/edit/ls/find/grep 执行前调用, 越界即拒。真实强制。
    Check(path string, op FSOp) error
    // 子进程平面: Container 用于构造只读 / 读写挂载; SoftLimit 返回 best-effort 提示。
    SubprocessMounts() []Mount
}

type FSOp int
const (
    FSRead FSOp = iota
    FSWrite
)
```

### 工厂与选择
```go
// New 按配置选择后端。未知后端报错; 空配置回落 Passthrough。
func New(cfg Config) (Sandbox, error)
```

## 4. 后端实现与真实强制矩阵

下表是本设计的**契约表**, 写明每个后端在每个平面上**真实强制了什么**, 杜绝"伪沙箱错觉":

| 后端 | 进程内 http/MCP | 进程内 file 工具 | 子进程 bash 网络 | 子进程 bash 文件 | 资源/超时 | 状态 |
|---|---|---|---|---|---|---|
| **Passthrough** | 无 | 无 | 无 | 无 | 仅超时 | 默认, 必有 |
| **SoftLimit** | **真实**(Dialer) | **真实**(Check) | 尽力(proxy-env) | 尽力(cwd) | best-effort rlimit | 首期落地 |
| **Container** | **真实** | **真实** | **真实**(--network) | **真实**(mount) | **真实**(--memory/--cpus) | 接口预留, stub |
| **Remote** | **真实** | **真实** | **真实** | **真实** | **真实** | 接口预留, stub |

### Passthrough
- `Exec`: 等价当前 `builtin.Bash` 行为(`sh -c`, `os.Environ()`, cwd = 项目目录)。
- `Network().DialContext`: 直接用 `net.Dialer`。`SubprocessConfig`: 空。
- `Filesystem().Check`: 恒 `nil`(放行)。
- 作为开发调试默认值与回归测试基准。

### SoftLimit (首期真实后端)
命名刻意避开 "Sandbox/Isolated" 字样 —— 它是**软限制**, 防君子不防小人, **不是安全边界**。
- `Exec`:
  - env 按 **default-deny 凭证策略**过滤: 默认仅放行基础变量(`PATH`/`HOME`/`LANG`/`SHELL` 等), 剥离 `AWS_*`、`*_TOKEN`、`*_KEY`、`*_SECRET`、`*_PASSWORD` 等凭证类; allowlist 可配置, 按需注入。
  - 超时(沿用现有 `context.WithTimeout`)。
  - 资源限制: Unix 经 `SysProcAttr` + `setrlimit`(`RLIMIT_AS`/`RLIMIT_CPU`); Windows 经 Job Object。**缺失能力显式降级**(记录日志), 不假装支持。
  - 输出按 `MaxOutputBytes` 截断。
- `Network().DialContext`: allowlist Dialer, 拒绝未授权 `host:port`(进程内平面真实强制)。
- `Network().SubprocessConfig`: 返回 proxy-env hint(指向进程内过滤代理), best-effort, 文档标注可被裸 socket 绕过。
- `Filesystem().Check`: 把 file 工具路径限制在允许的根目录内(进程内平面真实强制)。`SubprocessMounts`: best-effort(仅 cwd 提示), 不构成边界。

### Container / Remote (仅接口预留)
- 提供实现 `Sandbox` 接口的 stub: 构造时若被选中且依赖缺失(如未装 Docker), 返回明确错误。
- 真实逻辑留待后续 spec。映射业界主流: Container ≈ Docker/gVisor; Remote ≈ E2B/Firecracker microVM(AI Agent 基建标杆)。

### 4.1 文件路径校验规范 (`Filesystem().Check` 的 Path Traversal 防御)

进程内 file 工具的边界**只有在路径校验本身防逃逸时才成立**。`Check(path, op)` 的实现契约:

1. **规范化到真实物理路径再比对**: 不能对原始字符串做前缀匹配。必须 `filepath.Abs` → `filepath.Clean` → `filepath.EvalSymlinks`, 算出最终真实路径, 再与放行根做前缀匹配。这样 `../../etc/passwd` 与符号链接越界都会被解析穿透后拒绝。
   - 对**写**操作的不存在路径(目标文件尚未创建), `EvalSymlinks` 会失败 —— 退而对其**父目录**做 `EvalSymlinks` 后再拼接 basename, 防止经由软链接父目录逃逸。
2. **跨平台分隔符抹平**: 目标平台含 Windows 11。比对前统一把 `\` 归一为 `/`(或统一用 `filepath` 的平台分隔符), 确保 Windows 下不会因 `\` 与 `/` 混用绕过边界。已有 `permissions` 包的 `normalize()` 可复用同一约定。
3. **前缀匹配按路径分段**: 用 `strings.HasPrefix` 前需保证以分隔符对齐(`root` 末尾补 `/`), 避免 `/projectevil` 误匹配 `/project`。

> 同样的规范化适用于 `permissions` 的敏感文件 hook —— 二者应共享一个 `normalizePath` 工具函数, 避免两套不一致的校验各开一个洞。

### 4.2 SoftLimit Exec 的平台隐患(实现期约束)

1. **环境变量大小写: Windows 不区分大小写。** Linux/macOS 下 env 严格区分大小写, `[PATH, HOME, LANG, SHELL]` 的等值匹配工作正常; 但 Windows 上系统常存为 `Path`, 严格等值匹配会**误杀** `Path`, 导致子进程找不到可执行文件。env allowlist 过滤必须按 `runtime.GOOS` 引入**大小写折叠(case-folding)**: Windows 下用 `strings.EqualFold` 比对变量名, 其余平台保持区分大小写。
2. **Context 取消与孤儿进程。** `exec.CommandContext` 绑定 ctx, 超时/取消时标准库只对**直接子进程**发 `SIGKILL`; 若 bash 内部 `nohup task &` 再派生后台进程, 这些会逃逸成孤儿继续占用资源。
   - **Unix**: `SysProcAttr{Setpgid: true}` 把子进程放入独立进程组; ctx 取消时不依赖标准库默认行为, 而是向 `-PID`(负数 = 整个进程组)发信号, 连带清理后台子进程。需自己监听 `ctx.Done()` 并 `syscall.Kill(-pid, SIGKILL)`。
   - **Windows**: 用 Job Object(同 §4 资源限制路径)统一管理进程树; 关闭 Job 时连带终止全部后代进程, 天然解决孤儿问题。
   - 两平台的实现走 build-tag 分文件(`exec_unix.go` / `exec_windows.go`), 缺失能力显式降级并记录日志。

## 5. 与现有层的关系(正交分层)

沙箱**不替换**现有的准入逻辑, 而是新增一层执行隔离:

1. **准入层 (admission)** —— `permissions.Engine`(glob → allow/ask/deny) + `BeforeToolCall` hooks(bash 黑名单、敏感文件)。**先执行, 不变。** 决定"这个动作允不允许发生"。
2. **隔离层 (isolation)** —— 本沙箱。准入通过后, 决定"这个动作在什么约束下执行"。

网络 allowlist 与文件 `Check` 是 `permissions` glob 之外**新增的强制层**: 即便一个工具被准入, 其连接 / 文件访问仍受沙箱策略约束(纵深防御)。

## 6. 决策记录: 为何 SoftLimit 不补子进程网络逃逸

`SoftLimit` 下 `sh -c "curl http://x"` 的子进程直接 `connect()`, 绕过 `DialContext`。**决策: 不在此层补救, 降级为诚实的软限制。** 理由:
1. **平台限制**: 目标平台含 Windows 11, Linux netns / eBPF 不可用; 在"零依赖、跨平台"后端塞 Linux-only 内核特性与其价值主张矛盾。
2. **proxy-env 仅是 hint**: 覆盖 curl/git/多数 SDK, 但裸 socket 一行 `connect()` 即绕过, 不能称为边界。
3. **投入高度错误**: 在零依赖层硬刚 iptables/netns 性价比极低。

真实子进程网络 / 文件边界由 `Container` / `Remote` 提供 —— 那里边界是真实的。

## 7. 集成点

- `builtin.Bash`: 增加 `sandbox.Sandbox` 字段, `Execute` 改为调 `sb.Exec(...)`, 用 `result.Combined()` 保持当前返回契约。
- `tools.HTTP` 与 MCP 客户端: `http.Client.Transport.DialContext = sb.Network().DialContext`。
- file 工具(`read`/`write`/`edit`/`ls`/`find`/`grep`): 执行前调用 `sb.Filesystem().Check(path, op)`。
- 装配: `pkg/agentsetup/setup.go` + `cmd/lcoder` 读 config → `sandbox.New` → 注入工具工厂。

## 8. 配置 (`lcoder.yaml` 新增 `sandbox:` 段)

```yaml
sandbox:
  backend: soft-limit          # passthrough | soft-limit | container(stub) | remote(stub)
  env_allowlist: [PATH, HOME, LANG, SHELL]   # default-deny, 其余尤其凭证类一律剥离
  network:
    default: deny              # deny | allow
    allow: ["api.anthropic.com:443", "*.github.com:443"]
  filesystem:
    writable: ["."]            # 允许写的根(相对项目根, 见下方语义说明)
    readable: ["."]            # 允许读的根
  limits:
    max_memory_mb: 512
    max_cpu_seconds: 60
    max_output_bytes: 1048576
```

### 配置路径语义(实现期约束)
- **`.` 的基准是注入的项目根目录**, 不是宿主进程启动时的 CWD。配置解析时由调用方(`agentsetup` / `cmd/lcoder`)把项目根作为基准注入 `sandbox.Config`, 路径在此刻一次性解析为绝对路径并固化。这样多 agent 实例并发、或从不同目录启动时, 文件边界不会随宿主 CWD 漂移。
- 解析后的根路径同样走 §4.1 的规范化(`Abs`→`Clean`→`EvalSymlinks`)与分隔符归一, 保证配置侧与运行期校验用同一物理路径口径。

## 9. 测试策略

- `FakeSandbox`: 记录收到的 `ExecSpec`、可编程返回 `ExecResult` / 错误 → 工具单测不碰真实进程 / 网络。
- `Passthrough`: 行为等价测试, 锁定 Bash 不回归(stdout/stderr 分离 + Combined 等价旧 CombinedOutput)。
- `SoftLimit` 表驱动测试:
  - env 剥离: 凭证类被移除, 基础变量保留; **Windows 下 `Path` 经大小写折叠保留**, 不被误杀。
  - 超时: 触发 `TimedOut`。
  - **孤儿进程**: bash 内 `task &` 派生的后台子进程在 ctx 取消后被进程组/Job 连带清理(Unix 验证进程组信号; Windows 验证 Job 终止)。
  - 输出截断: 超过 `MaxOutputBytes` 被截。
  - `DialContext`: allow / deny 表。
  - `Filesystem().Check`: 越界路径被拒, 允许根内放行。
  - **Path traversal**: `../` 越界、符号链接指向根外、不存在的写目标经软链接父目录逃逸 —— 均被拒; Windows 下 `\` 与 `/` 混用不绕过。
- 跨平台: rlimit / Job Object 缺失时显式降级路径有测试覆盖。

## 10. 已知局限(写入文档与命名)

- `SoftLimit` 非安全边界, 命名与文档显式标注"防君子不防小人"。
- `RLIMIT_AS`/`RLIMIT_CPU` 跨平台行为差异大, 且存在 OOM-killer 误杀宿主进程风险; 实现中显式处理并记录。
- 子进程平面在 `SoftLimit` 下的网络 / 文件限制均为 best-effort, 不可依赖其做安全决策。
