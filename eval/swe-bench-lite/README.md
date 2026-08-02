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

## 评测协议（official vs extended）

| 模式 | 说明 | 用途 |
|---|---|---|
| **official**（`OFFICIAL_PROTOCOL=1`） | 对齐官方 SWE-bench：一次性评估、无测试反馈重试、PASS_TO_PASS 全量不截断 | 与其他 agent 横向对比的分数 |
| **extended**（默认） | 允许 ≤2 轮测试反馈重试（`FEEDBACK_ATTEMPTS`）、P2P 截断 20 条（`P2P_CAP`） | 工程诊断、agent 能力上限观察 |

结果文件（`result.json`）记录每次运行的 `protocol` 与 `model`；报告同时给出两种协议的分开分数，并以 `initial_status`（反馈前）与 `final_status`（反馈后）区分——**对外报分时应使用 official 协议或 initial_status 口径**。

`scripts/predictions.py` 可从结果目录导出官方评测器兼容的 `predictions.jsonl`（`instance_id / model_name_or_path / model_patch`）。

## 自定义模型与密钥

默认链路：Kimi coding 网关 + `ANTHROPIC_AUTH_TOKEN`。换其他模型只需两步：

1. 编辑 `config/lcoder.yaml`——改顶部的 `provider`/`model`，并在 `providers:` 下加/改对应条目（`protocol` 选协议(可选,缺省按名推断)、`base_url` 指端点、`api_key` 用 `{env:EVAL_API_KEY}` 引用）。文件里已带 Anthropic / OpenAI / OpenRouter / 自建 OpenAI 兼容端点四段注释示例。
2. 导出密钥并运行：

```bash
export EVAL_API_KEY=sk-xxxx
export MODEL_ID=<报表里要显示的模型名>   # 可选，默认 kimi-k2.7-code
python eval/swe-bench-lite/runner/run.py --build --select
```

协议路由说明：`anthropic` = Anthropic Messages API；`openai` = OpenAI chat completions 兼容（含 OpenRouter、vLLM、LiteLLM 等绝大多数网关）；`openai-responses` = OpenAI Responses API。`ANTHROPIC_AUTH_TOKEN` 与 `EVAL_API_KEY` 设任一个即可，两者都会透传进容器。

## 用法

```bash
# 一键：交叉编译 + 构建镜像 + 多仓库分层采样（默认覆盖 SWE-bench Lite 全部 11 个官方仓库）+ 运行
python eval/swe-bench-lite/runner/run.py --build --select

# 官方协议（一发无反馈、P2P 全量）跑可对比分数
OFFICIAL_PROTOCOL=1 python eval/swe-bench-lite/runner/run.py --build --select

# 导出官方评测器兼容的 predictions.jsonl
python eval/swe-bench-lite/scripts/predictions.py --results-dir eval/swe-bench-lite/results

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

# goal 模式：用 lcoder --goal(GoalDriver 跨轮收敛 + 预算护栏)替代单次 -p
GOAL_MODE=1 GOAL_TURNS=60 python eval/swe-bench-lite/runner/run.py \
  --sample 18 --run-id goal-20260801 --variant goal-driver --note "GoalDriver 对比"

# 跑批后归档为 runs/<run-id>（配置快照 + 轻量结果 + 更新 INDEX.md）
python eval/swe-bench-lite/runner/run.py --sample 18 --run-id baseline-20260801 \
  --variant baseline --note "基准基线"

# 单独归档已有 results（不重跑任务）
python eval/swe-bench-lite/scripts/archive.py --id baseline-20260729 \
  --variant baseline --model kimi-k2.7-code --note "现有基线"

# 只重建 runs/INDEX.md 对比索引
python eval/swe-bench-lite/scripts/archive.py --index
```

> **注意**：不同任务可能依赖不同 Python 版本（`python_version` 字段）。`run.py` 会按需构建 `lcoder-swe-bench-lite:py<version>` 镜像；如果本地存在旧镜像，请删除后重新 `--build`，否则容器内的脚本可能是旧版本。

## 评测归档（runs/）

每次跑批可用 `archive.py` 沉淀为一个可对比的历史快照，用于**用数据决定是否接受某个 harness 改动**（改 configs/ 下的系统提示、模式、模型配置后对比同一任务子集）：

```
eval/swe-bench-lite/runs/<run-id>/
├── meta.json          # 时间/变体/模型/任务数/汇总指标
├── config.snapshot/   # 生效配置快照(configs/*, eval config, prompts)——精确复现/对比
└── results/           # 轻量结果(report.md/html + 每任务 result.json/patch.diff/test_patch.diff)
```

- 归档只保留轻量产物，**丢弃** events.jsonl / context-snapshots / observability 等大体积原始日志（单任务 events 可达数百 MB），需要完整事件时回原始 `results/` 目录。
- `runs/INDEX.md` 汇总所有 run 的对比表：resolved / 初始resolved / 编辑任务（用 edit/write 工具改文件的任务占比，诊断 agent 是否落地修改）/ 产patch / avg turns / cost / duration。
- 接受改动判据：同一任务子集上 resolved 不降，且 cost/duration 不显著上升；或目标指标（如编辑任务占比）有明确提升。

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
