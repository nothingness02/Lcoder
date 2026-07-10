# SWE-bench Lite Baseline

> 本文件记录 Lcoder 在 SWE-bench Lite 上的评估基线、配置、样本结果与已知限制。
> 每次重大变更后，应重新运行评估并更新“结果”章节。

## 评估目标

用 SWE-bench Lite 真实任务端到端衡量 Lcoder 的软件工程能力：
理解 issue → 定位根因 → 修改源码 → 运行测试验证。

## 当前配置

| 配置项 | 值 |
|--------|-----|
| 数据集 | `princeton-nlp/SWE-bench_Lite` |
| 模型 | `kimi-k2.7-code` |
| Provider (Lcoder 内) | `moonshot` → `moonshotai` catalog 别名 |
| 网关 | Kimi coding gateway (Anthropic-compatible), `https://api.kimi.com/coding/v1` |
| 鉴权 | host 通过 `ANTHROPIC_AUTH_TOKEN` 注入容器 |
| 上下文 | `max_tokens: 262144`, `max_output: 16384` |
| 权限 | 评估传 `--unsafe`，容器内非交互放行 |
| 反馈循环 | F2P 未通过时反馈 pytest 输出，最多再试 2 轮 |
| P2P 截断 | 默认最多评测前 20 个 PASS_TO_PASS，结果中标记 `p2p_capped` |
| Runner | `python eval/swe-bench-lite/runner/run.py --sample N` |

模型元数据已硬编码到 `configs/models.yaml`，容器内不再依赖实时 `models.dev` 拉取。

## 当前样本任务

`eval/swe-bench-lite/data/tasks.json` 当前固定了 4 个 sympy 任务，
用于验证反馈循环与评估 pipeline 的稳定性：

| instance_id | repo | fail_to_pass | pass_to_pass | python |
|-------------|------|--------------|--------------|--------|
| sympy__sympy-13043 | sympy/sympy | `test_decompose` | `test_best_origin` | 3.9 |
| sympy__sympy-24102 | sympy/sympy | `test_mathematica`, `test_parser_mathematica_tokenizer` | (空) | 3.9 |
| sympy__sympy-22005 | sympy/sympy | `test_solve_poly_system` | `test_solve_biquadratic`, `test_solve_triangulated` | 3.9 |
| sympy__sympy-24909 | sympy/sympy | `test_prefix_operations` | `test_prefix_unit`, `test_bases` | 3.9 |

## 运行方式

```bash
# 全流程:编译镜像 + 筛选 + 运行
cd eval/swe-bench-lite/runner
python run.py --build --select --limit 4 --repo sympy/sympy

# 仅运行已选任务(前 N 个)
python run.py --sample 4

# 指定单个任务
python run.py --instance sympy__sympy-13043

# 并行运行
python run.py --sample 4 --workers 2
```

运行结束后会自动生成：

- `eval/swe-bench-lite/results/summary.json`
- `eval/swe-bench-lite/results/summary.md`

## 产物与指标

每个任务产物位于 `eval/swe-bench-lite/results/<instance_id>/`：

| 文件 | 含义 |
|------|------|
| `result.json` | 状态、阶段、指标、模型、耗时 |
| `patch.diff` | agent 产生的代码改动(不含 gold 测试) |
| `test_patch.diff` | 注入的 gold 测试 |
| `test_before.log` / `test_after.log` | 修复前/后 F2P 结果 |
| `test_before_p2p.log` / `test_after_p2p.log` | 修复前/后 P2P 结果 |
| `events.jsonl` | 完整事件流 |
| `agent.stderr.log` | agent stderr |

`result.json` 中的 `metrics` 字段：

| 字段 | 含义 |
|------|------|
| `turns` | agent 轮数 |
| `tool_calls` | 总工具调用次数 |
| `file_edits` | edit/write 次数 |
| `tests_run` | agent 自行运行测试的次数 |
| `messages` | message_end 事件数 |
| `errors` | error 事件数 |
| `tokens.prompt` | prompt tokens |
| `tokens.completion` | completion tokens |
| `tokens.cache_read` | cache read tokens |
| `tokens.cache_write` | cache write tokens |
| `cost` | 估算成本(USD) |

## 结果分类

| 状态 | 条件 |
|------|------|
| resolved | FAIL_TO_PASS 全过 且 PASS_TO_PASS 全过 |
| partial | FAIL_TO_PASS 全过，PASS_TO_PASS 有失败 |
| failed | FAIL_TO_PASS 仍有失败 |
| timeout | agent 超过 `AGENT_TIMEOUT_S` |
| error | 环境/clone/install/打补丁等异常 |

## 样本结果 (sympy, N=4)

> 来自早期带反馈循环的实验运行。

| Instance | Status | F2P | P2P | Turns | Tool Calls | File Edits | Duration |
|----------|--------|-----|-----|-------|------------|------------|----------|
| sympy__sympy-13043 | resolved | pass | pass | 91 | 93 | 9 | 710s |
| sympy__sympy-22005 | resolved | pass | pass | 31 | 30 | 3 | 200s |
| sympy__sympy-24909 | resolved | pass | pass | 30 | 39 | 2 | 252s |
| sympy__sympy-24102 | timeout | fail | fail | 64 | 68 | 3 | 770s |

**解决率:** 3/4 (75%)。

- `sympy__sympy-24102` 在第一次反馈循环期间触发 agent 超时，agent 已产生非空 patch。
- 其余三个任务在一次 agent 运行内收敛，反馈循环被跳过或很快完成。

## 已知限制

- **Token / Cost 统计**: 当前指标采集已按 session 精确读取 observability 文件，但 Kimi coding gateway 未必在 Anthropic 兼容响应中返回标准 usage 字段，因此 `cost` 与 `tokens` 可能为 0。需要对照网关实际响应进一步校准 provider adapter。
- **样本单一**: 当前仅覆盖 sympy，后续应扩展到 psf/requests、django、scikit-learn 等仓库。
- **复杂仓库**: 可能需要自定义 `install_cmd` / `test_cmd`。
- **网络波动**: codeload tarball 下载偶有失败，runner 已内置重试。

## CI / Automation

`.github/workflows/swe-bench-lite.yml` 每周日 06:00 UTC 或手动触发：

1. 交叉编译 Linux 版 `lcoder`。
2. 构建评估 Docker 镜像。
3. 从指定 repo 筛选最小的 N 个任务。
4. 运行最多 N 个任务。
5. 上传 `eval/swe-bench-lite/results/` 为 artifact。

需要仓库 secret `ANTHROPIC_AUTH_TOKEN`。

## 后续演进

- [ ] 校准 Kimi gateway 的 token/cost 提取
- [ ] 扩展样本到更多仓库
- [ ] 引入 LLM-as-a-judge 对 patch 质量打分
- [ ] 与 CI 集成，每次提交自动跑小数据集回归
- [ ] 支持多模型 A/B 对比
