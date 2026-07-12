# SWE-bench Lite MVP 评估平台

用 SWE-bench Lite 真实任务，端到端衡量 Lcoder 的软件工程能力（理解 → 定位 → 修复 → 验证）。实施依据见 `../../docs/mvp-swe-bench-lite.md`。

## 架构

本机 host shell 无外网，仅 Docker 容器有外网。因此**全流程在容器内运行**：

```
host (run.py)                      container (python:3.x + git + lcoder)
  ├─ 交叉编译 linux lcoder   ──┐
  ├─ docker build           ──┤──>  /usr/local/bin/lcoder
  ├─ select_task (容器内)    ──┘     clone → checkout → pip install
  └─ run_task   (容器内)            → baseline（apply test_patch，验 F2P 失败）
                                     → 撤销 test_patch
                                     → lcoder agent 修复（经 Kimi 网关）
                                     → 提取 patch.diff
                                     → 叠加 test_patch → 跑 F2P+P2P → 分类
```

模型经 Kimi coding 网关（Anthropic 兼容）驱动 `kimi-k2.7-code`，provider 名用 `moonshot` 以经别名从 `models.dev` 加载指标；鉴权令牌由 host 通过 `ANTHROPIC_AUTH_TOKEN` 注入容器，见下文 [API Key 注入](#api-key-注入)。

## 目录

```
config/lcoder.yaml            评估专用 lcoder 配置（网关 + 全放行权限 + 钉死上下文窗口）
config/models.yaml            模型/网关/价格配置
Dockerfile                    评估镜像
prompts/swe_task.txt          任务 prompt 模板
scripts/select_task.py        从 HF 拉取并筛选任务 -> data/tasks.json
scripts/metrics.py            从 events.jsonl / observability.jsonl / patch.diff 汇总指标
scripts/run_in_container.py   容器内单任务编排（setup/baseline/agent/patch/eval/feedback/metrics）
scripts/report.py             汇总所有任务结果生成 report.md / report.html
runner/run.py                 host 编排（编译 + 构建 + 筛选 + 运行 + 汇总 + 报告）
data/tasks.json               筛选出的任务
data/swe-bench-lite-cache.jsonl  数据集缓存
results/<instance_id>/        每任务产物
results/report.md             汇总 Markdown 报告
results/report.html           汇总 HTML 报告
results/summary.md            简要汇总
```

## 用法

```bash
# 一键：交叉编译 + 构建镜像 + 多仓库分层采样（默认 4 个仓库，每仓库 5 个，上限 50）+ 运行
python eval/swe-bench-lite/runner/run.py --build --select

# 只构建镜像（不运行）
python eval/swe-bench-lite/runner/run.py --build --no-run

# 指定具体任务（已在 tasks.json）
python eval/swe-bench-lite/runner/run.py --instance psf__requests-2317

# 指定仓库 / 采样规模
python eval/swe-bench-lite/runner/run.py --build --select \
  --repo psf/requests,sympy/sympy --per-repo 10 --limit 20

# 已构建 / 已选，仅重跑（默认跑全部已选任务）
python eval/swe-bench-lite/runner/run.py

# 批量跑前 N 个任务，M 并行
python eval/swe-bench-lite/runner/run.py --sample 50 --workers 2
```

> **注意**：不同任务可能依赖不同 Python 版本（`python_version` 字段）。`run.py` 会按需构建 `lcoder-swe-bench-lite:py<version>` 镜像；如果本地存在旧镜像，请删除后重新 `--build`，否则容器内的脚本可能是旧版本。

## 产物（results/<instance_id>/）

| 文件 | 含义 |
|------|------|
| `result.json` | 状态分类 + 阶段 + baseline + 指标（含 `initial_status`/`final_status`、工具链路、observability 性能、token/cost） |
| `patch.diff` | agent 的代码改动（不含 gold 测试） |
| `test_patch.diff` | 注入的 gold 测试 |
| `test_before.log` / `test_after.log` | 修复前/后 FAIL_TO_PASS 结果 |
| `test_before_p2p.log` / `test_after_p2p.log` | PASS_TO_PASS 结果 |
| `test_after_fb_*.log` | 反馈循环中第 N 次重跑测试日志 |
| `events.jsonl` | 完整事件流 |
| `observability.jsonl` | trace/span/metric 原始数据 |
| `context-snapshots/*.md` | 上下文快照 |
| `install.log` / `agent.stderr.log` | 安装日志 / agent stderr |

## 反馈循环

当 FAIL_TO_PASS 未全部通过，或 PASS_TO_PASS 出现回归时，`run_in_container.py` 会把 pytest 输出反馈给 agent，让它继续修复。最多 `FEEDBACK_ATTEMPTS`（默认 2）轮，每轮单独记录阶段耗时与测试日志。

## 结果分类

| 状态 | 条件 |
|------|------|
| resolved | FAIL_TO_PASS 全过 且 PASS_TO_PASS 全过 |
| partial | FAIL_TO_PASS 全过，PASS_TO_PASS 有失败 |
| failed | FAIL_TO_PASS 仍有失败（含反馈轮次耗尽） |
| timeout | agent 超过 `AGENT_TIMEOUT_S`（默认 1500s） |
| error | 环境/clone/install/打补丁等异常 |

## 指标与汇总报告

`runner/run.py` 跑完后会自动调用 `scripts/report.py`，在 `results/` 下生成：

- `report.md` / `report.html`：跨任务的 SWE-bench 初次/反馈后成功率、工具调用汇总、token/cost、缓存命中率、核心模块性能（turn/LLM/tool/TTFT 耗时）、上下文快照列表、按任务的工具链路。
- `summary.json` / `summary.md`：简要统计表。

也可以单独运行：

```bash
python eval/swe-bench-lite/scripts/report.py --results-dir eval/swe-bench-lite/results
```

指标从以下数据源聚合：

- `events.jsonl`：turns、tool_calls、file_edits、各工具计数、工具链路。
- `observability.jsonl`：prompt/completion/cache tokens、cost、llm_calls、turn/llm/tool 耗时、TTFT。
- `patch.diff`：改动文件数、增删行数。

## 上下文快照

评估默认开启上下文快照，用于人工检查 agent 在关键时间点的完整上下文：

- **触发时机**：只在真正发生 compaction 时拍一张 `context-turn-<n>-compaction.md`，以及任务结束时拍一张 `context-turn-<n>-end.md`；没有 compaction 的任务只保留最终一张快照。
- **内容完整**：快照不再只取 `msg.Text()`，而是遍历 `ContentPart`，完整展示 assistant 的 thinking、tool_call 参数、tool_result 输出（stdout/stderr/exit_code）以及 system/user 文本。
- **不截断**：`max_messages_per_block` 设为 `0`，每个 block 的全部 message 都会列出。

快照目录：`results/<instance_id>/context-snapshots/`。

## API Key 注入

API key 通过 host 环境变量注入容器：

```bash
export ANTHROPIC_AUTH_TOKEN=sk-...
python eval/swe-bench-lite/runner/run.py --build --select
```

`runner/run.py` 会读取 `ANTHROPIC_AUTH_TOKEN` 并通过 `docker run -e ANTHROPIC_AUTH_TOKEN=...` 传入容器。key 不会写入镜像或代码。

## MVP 已知约束

- PASS_TO_PASS 默认只评测前 `P2P_CAP`（20）个，`result.json` 的 `p2p_capped` 字段会标记是否截断。
- 测试命令默认 `python -m pytest`；复杂仓库需在 task 的 `test_cmd` / `install_cmd` 覆盖。
- `kimi-k2.7-code` 经 `moonshot` → `moonshotai` 别名从 `models.dev` 加载指标（窗口/pricing）；容器内无网时回退 `config/lcoder.yaml` 钉死的窗口，cost 估算可能为 0。
- 如果本地已存在 `lcoder-swe-bench-lite:py<x.y>` 旧镜像，建议删除后重新 `--build`，否则容器内脚本可能不是最新版本。
