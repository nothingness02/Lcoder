# SWE-bench Lite MVP Evaluation Platform

End-to-end evaluation of Lcoder's software-engineering capabilities (understand → locate → fix → verify) using real SWE-bench Lite tasks. Implementation rationale is in `../../docs/mvp-swe-bench-lite.md`.

## Architecture

The host shell has no external network; only Docker containers have internet access. Therefore the **entire pipeline runs inside a container**:

```
host (run.py)                      container (python:3.x + git + lcoder)
  ├─ cross-compile linux lcoder  ──┐
  ├─ docker build                ──┤──> /usr/local/bin/lcoder
  ├─ select_task (inside container)─┘     clone → checkout → pip install
  └─ run_task   (inside container)        → baseline (apply test_patch, verify F2P fails)
                                          → revert test_patch
                                          → lcoder agent fixes (via Kimi gateway)
                                          → extract patch.diff
                                          → re-apply test_patch → run F2P+P2P → classify
```

The model is driven by the Kimi coding gateway (Anthropic-compatible) using `kimi-k2.7-code`; the provider name is `moonshot`, which resolves via alias to metrics from `models.dev`. The auth token is injected into the container from the host via `ANTHROPIC_AUTH_TOKEN`, see [API Key Injection](#api-key-injection) below.

## Directory Layout

```
config/lcoder.yaml            Evaluation-specific lcoder config (gateway + fully open permissions + fixed context window)
config/models.yaml            Model/gateway/pricing config
Dockerfile                    Evaluation image
prompts/swe_task.txt          Task prompt template
scripts/select_task.py        Fetch and filter tasks from HF -> data/tasks.json
scripts/metrics.py            Aggregate metrics from events.jsonl / observability.jsonl / patch.diff
scripts/run_in_container.py   In-container single-task orchestration (setup/baseline/agent/patch/eval/feedback/metrics)
scripts/report.py             Aggregate all task results into report.md / report.html
runner/run.py                 Host orchestration (compile + build + select + run + summarize + report)
data/tasks.json               Selected tasks
data/swe-bench-lite-cache.jsonl  Dataset cache
results/<instance_id>/        Per-task artifacts
results/report.md             Aggregated Markdown report
results/report.html           Aggregated HTML report
results/summary.md            Short summary
```

## Usage

```bash
# One-shot: cross-compile + build image + stratified sampling (default 4 repos, 5 per repo, cap 50) + run
python eval/swe-bench-lite/runner/run.py --build --select

# Build images only (do not run tasks)
python eval/swe-bench-lite/runner/run.py --build --no-run

# Run a specific instance (must already be in tasks.json)
python eval/swe-bench-lite/runner/run.py --instance psf__requests-2317

# Specify repos / sampling size
python eval/swe-bench-lite/runner/run.py --build --select \
  --repo psf/requests,sympy/sympy --per-repo 10 --limit 20

# Already built/selected, just rerun all selected tasks
python eval/swe-bench-lite/runner/run.py

# Run first N tasks with M workers
python eval/swe-bench-lite/runner/run.py --sample 50 --workers 2

# Archive a batch run as runs/<run-id> (config snapshot + light results + INDEX.md)
python eval/swe-bench-lite/runner/run.py --sample 18 --run-id baseline-20260801 \
  --variant baseline --note "baseline run"

# Archive existing results without rerunning
python eval/swe-bench-lite/scripts/archive.py --id baseline-20260729 \
  --variant baseline --model kimi-k2.7-code --note "existing baseline"

# Rebuild runs/INDEX.md comparison index only
python eval/swe-bench-lite/scripts/archive.py --index
```

> **Note**: Different tasks may depend on different Python versions (the `python_version` field). `run.py` builds `lcoder-swe-bench-lite:py<version>` images on demand. If an old image already exists locally, delete it and re-run `--build`, otherwise the scripts inside the container may be stale.

## Run Archiving (runs/)

Each batch run can be archived by `archive.py` into a comparable history snapshot, so **harness changes can be accepted/rejected by data** (change configs/ — system prompts, modes, model config — then compare on the same task subset):

```
eval/swe-bench-lite/runs/<run-id>/
├── meta.json          # time/variant/model/task count/aggregate metrics
├── config.snapshot/   # effective config snapshot (configs/*, eval config, prompts) — exact reproduction/comparison
└── results/           # light results (report.md/html + per-task result.json/patch.diff/test_patch.diff)
```

- Archive keeps only light artifacts and **drops** large raw logs such as events.jsonl / context-snapshots / observability (per-task events can reach hundreds of MB); go back to the original `results/` for full events.
- `runs/INDEX.md` aggregates a comparison table across all runs: resolved / initially-resolved / edited-tasks (share of tasks that actually modified files via edit/write — diagnoses whether the agent lands changes) / produced-patch / avg turns / cost / duration.
- Acceptance criterion: on the same task subset, resolved must not drop and cost/duration must not rise significantly; or a targeted metric (e.g. edited-task share) must clearly improve.

## Artifacts (`results/<instance_id>/`)

| File | Meaning |
|------|---------|
| `result.json` | Status classification + stages + baseline + metrics (includes `initial_status`/`final_status`, tool chain, observability performance, token/cost) |
| `patch.diff` | Agent's code changes (without gold tests) |
| `test_patch.diff` | Injected gold tests |
| `test_before.log` / `test_after.log` | FAIL_TO_PASS results before/after fix |
| `test_before_p2p.log` / `test_after_p2p.log` | PASS_TO_PASS results |
| `test_after_fb_*.log` | Test logs from the N-th feedback iteration |
| `events.jsonl` | Full event stream |
| `observability.jsonl` | Raw trace/span/metric data |
| `context-snapshots/*.md` | Context snapshots |
| `install.log` / `agent.stderr.log` | Install log / agent stderr |

## Feedback Loop

When FAIL_TO_PASS is not fully passing, or PASS_TO_PASS regresses, `run_in_container.py` feeds the pytest output back to the agent for further repair. Up to `FEEDBACK_ATTEMPTS` (default 2) rounds; each round records its own stage timing and test logs.

## Status Classification

| Status | Condition |
|--------|-----------|
| resolved | FAIL_TO_PASS all pass **and** PASS_TO_PASS all pass |
| partial | FAIL_TO_PASS all pass, but PASS_TO_PASS has failures |
| failed | FAIL_TO_PASS still has failures (including feedback rounds exhausted) |
| timeout | Agent exceeds `AGENT_TIMEOUT_S` (default 1500s) |
| error | Environment/clone/install/patch application error |

## Metrics and Reports

After `runner/run.py` finishes, it automatically calls `scripts/report.py`, producing in `results/`:

- `report.md` / `report.html`: Cross-task SWE-bench initial/post-feedback resolution rates, tool usage summary, token/cost, cache hit rate, core module performance (turn/LLM/tool/TTFT latency), context snapshot list, and per-task tool chains.
- `summary.json` / `summary.md`: Short summary tables.

You can also run the report independently:

```bash
python eval/swe-bench-lite/scripts/report.py --results-dir eval/swe-bench-lite/results
```

Metrics are aggregated from:

- `events.jsonl`: turns, tool_calls, file_edits, per-tool counts, tool chain.
- `observability.jsonl`: prompt/completion/cache tokens, cost, llm_calls, turn/llm/tool latency, TTFT.
- `patch.diff`: changed files, insertions/deletions.

## Context Snapshots

Context snapshots are enabled by default for manual inspection of the agent's full context at key moments:

- **Trigger points**: A `context-turn-<n>-compaction.md` is captured only when a compaction is actually committed, and a `context-turn-<n>-end.md` is captured at task end. Tasks with no compaction keep only the final snapshot.
- **Full content**: Snapshots no longer rely solely on `msg.Text()`; they iterate over `ContentPart` and display assistant thinking, tool_call arguments, and full tool_result output (stdout/stderr/exit_code) as well as system/user text.
- **No truncation**: `max_messages_per_block` is set to `0`, so every message in each block is listed.

Snapshot directory: `results/<instance_id>/context-snapshots/`.

## API Key Injection

The API key is injected into the container via a host environment variable:

```bash
export ANTHROPIC_AUTH_TOKEN=sk-...
python eval/swe-bench-lite/runner/run.py --build --select
```

`runner/run.py` reads `ANTHROPIC_AUTH_TOKEN` and passes it into the container with `docker run -e ANTHROPIC_AUTH_TOKEN=...`. The key is never written into the image or the code.

## Known MVP Constraints

- PASS_TO_PASS is capped at the first `P2P_CAP` (20) tests; `result.json` marks this with the `p2p_capped` field.
- Default test command is `python -m pytest`; complex repos may need per-task `test_cmd` / `install_cmd` overrides.
- `kimi-k2.7-code` is resolved via the `moonshot` → `moonshotai` alias from `models.dev` for window/pricing metrics; if the container has no network, it falls back to the fixed window in `config/lcoder.yaml`, and cost estimates may be zero.
- If a stale `lcoder-swe-bench-lite:py<x.y>` image exists locally, delete it and re-run `--build` so the container uses the latest scripts.
