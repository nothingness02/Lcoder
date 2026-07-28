# 参考项目链接集合

> 本目录（`reference/`）原本存放的是各参考项目的完整源码副本，但这些副本会污染 Lcoder 的 Go 模块依赖解析。
> 现在改为只保留指向原始仓库的链接，需要研究时直接 clone 对应仓库即可。

---

## AI Agent / Coding Agent

| 项目 | GitHub 仓库 | 说明 |
|------|------------|------|
| **Pi** | https://github.com/earendil-works/pi.git | 极简可扩展的终端 coding agent，支持 extensions、skills、prompt templates、themes |
| **OpenCode** | https://github.com/anomalyco/opencode.git | 开源 AI coding agent，多语言 README |
| **Shannon** | https://github.com/Kocoro-lab/Shannon.git | 生产级多 agent 编排框架，Temporal 工作流、WASI 沙箱、token 预算 |
| **Kocoro** | https://github.com/Kocoro-lab/Kocoro.git | 与 Shannon 同源的 agent 项目 |
|**hermess**|https://github.com/NousResearch/hermes-agent.git|hermess agent 开源的自主型agent|
|**pi_subagents**|https://github.com/nicobailon/pi-subagents.git|pi_subagents的extension 设计|
|**kimi_code**|https://github.com/MoonshotAI/kimi-code.git|kimi_Code kimi旗下的code agent|
## code graph
|**code graph**|https://github.com/colbymchenry/codegraph.git|
## 其他参考

| 项目 | GitHub 仓库 | 说明 |
|------|------------|------|
| **Higress** | https://github.com/higress-group/higress.git | AI 网关，可作为 LLM provider 路由/代理的参考 |

---

## 使用方式

```bash
# 例如需要研究 Pi 的实现
cd /tmp
git clone https://github.com/earendil-works/pi.git
cd pi
```

## 注意事项

- 不要将参考项目源码直接复制到 `reference/` 下，尤其不要复制其 Go 源码。
- 如果需要本地保存副本，建议放在 Lcoder 仓库之外的目录，避免影响 `go mod tidy`。
- 本文件是 `reference/` 目录下唯一应被 Git 跟踪的文件。
