#!/usr/bin/env python3
"""从 HuggingFace 拉取 SWE-bench Lite 数据,筛选并写出 tasks.json。

在容器内运行(需要外网访问 datasets-server.huggingface.co)。

筛选策略:
- 默认覆盖多个常见轻量仓库,按仓库分层采样,避免只测单一仓库。
- 在每个仓库内按 FAIL_TO_PASS + PASS_TO_PASS 规模升序,优先选择测试少、
  环境风险低的任务。
- 支持 --repo 单仓库 / 多仓库(逗号分隔),以及 --instance 指定具体任务。
"""
import argparse
import json
import os
import sys
import urllib.request
from datetime import datetime, timezone

DATASET = "princeton-nlp/SWE-bench_Lite"
ROWS_URL = (
    "https://datasets-server.huggingface.co/rows"
    "?dataset={ds}&config=default&split=test&offset={off}&length={ln}"
)

# 默认评估仓库集合(轻量或代表性)。逗号分隔,可在命令行覆盖。
DEFAULT_REPOS = [
    "psf/requests",
    "sympy/sympy",
    "pallets/flask",
    "pytest-dev/pytest",
]

# 仓库 -> 适配的 Python 版本(对齐各任务代码的时代依赖)。未列出者默认 3.11。
REPO_PYVER = {
    "psf/requests": "3.9",
    "sympy/sympy": "3.9",
    "pallets/flask": "3.9",
    "pytest-dev/pytest": "3.9",
    "django/django": "3.9",
    "scikit-learn/scikit-learn": "3.9",
    "matplotlib/matplotlib": "3.9",
    "numpy/numpy": "3.9",
    "pandas-dev/pandas": "3.9",
    "sphinx-doc/sphinx": "3.9",
    "astropy/astropy": "3.9",
    "scipy/scipy": "3.9",
}


def fetch_rows(total=400, page=100):
    """分页拉取全部行。Lite 测试集约 300 条。"""
    rows = []
    off = 0
    while off < total:
        url = ROWS_URL.format(ds=DATASET, off=off, ln=page)
        with urllib.request.urlopen(url, timeout=60) as r:
            data = json.load(r)
        batch = data.get("rows", [])
        if not batch:
            break
        rows.extend(x["row"] for x in batch)
        off += page
    return rows


def load_cache(path):
    """从本地缓存读取数据集行。"""
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def save_cache(path, rows):
    """把数据集行写入本地缓存。"""
    os.makedirs(os.path.dirname(path) or ".", exist_ok=True)
    with open(path, "w", encoding="utf-8") as f:
        json.dump(rows, f, indent=2, ensure_ascii=False)


def as_list(v):
    if isinstance(v, list):
        return v
    if isinstance(v, str):
        try:
            parsed = json.loads(v)
            return parsed if isinstance(parsed, list) else [parsed]
        except json.JSONDecodeError:
            return [v] if v else []
    return []


def difficulty(row):
    """以测试规模作为任务难度/耗时代理。"""
    return len(as_list(row.get("FAIL_TO_PASS"))) + len(as_list(row.get("PASS_TO_PASS")))


def to_task(row, rank=0):
    return {
        "instance_id": row["instance_id"],
        "repo": row["repo"],
        "base_commit": row["base_commit"],
        "problem_statement": row.get("problem_statement", ""),
        "test_patch": row.get("test_patch", ""),
        "fail_to_pass": as_list(row.get("FAIL_TO_PASS")),
        "pass_to_pass": as_list(row.get("PASS_TO_PASS")),
        "python_version": REPO_PYVER.get(row["repo"], "3.11"),
        "test_cmd": "python -m pytest",
        "install_cmd": "pip install -e .",
        "difficulty": difficulty(row),
        "rank": rank,
    }


def parse_repos(s):
    """解析逗号分隔的仓库列表,支持额外空格。"""
    return [r.strip() for r in s.split(",") if r.strip()]


def select_tasks(rows, repos, per_repo, total_limit, instance=None):
    if instance:
        chosen = [r for r in rows if r["instance_id"] == instance]
        if not chosen:
            raise ValueError(f"instance {instance} not found")
        return [to_task(chosen[0], rank=1)]

    tasks = []
    for repo in repos:
        candidates = [r for r in rows if r["repo"] == repo]
        if not candidates:
            print(f"[select] warning: no tasks for repo {repo}", file=sys.stderr, flush=True)
            continue
        candidates.sort(key=difficulty)
        picked = candidates[: max(1, per_repo)]
        for rank, row in enumerate(picked, start=1):
            tasks.append(to_task(row, rank=rank))
        print(
            f"[select] repo={repo}: {len(candidates)} candidates, picked {len(picked)}",
            flush=True,
        )

    if total_limit > 0 and len(tasks) > total_limit:
        # 截断时仍保持各仓库分布(已按每仓库升序,简单取前 N)。
        tasks = tasks[:total_limit]
    return tasks


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument(
        "--repo",
        default=",".join(DEFAULT_REPOS),
        help="目标仓库,逗号分隔(默认: %(default)s)",
    )
    ap.add_argument(
        "--instance",
        default="",
        help="指定 instance_id(优先于 --repo 筛选)",
    )
    ap.add_argument(
        "--per-repo",
        type=int,
        default=5,
        help="每个仓库选规模最小的前 N 个任务",
    )
    ap.add_argument(
        "--limit",
        type=int,
        default=50,
        help="最终写出的总任务数上限(0=不限制)",
    )
    ap.add_argument("--out", default="/eval/data/tasks.json")
    ap.add_argument(
        "--cache",
        default="/eval/data/swe-bench-lite-cache.jsonl",
        help="本地数据集缓存路径",
    )
    ap.add_argument(
        "--refresh",
        action="store_true",
        help="强制重新从 HuggingFace 拉取并刷新缓存",
    )
    args = ap.parse_args()

    rows = None
    if not args.refresh and args.cache and os.path.isfile(args.cache):
        print(f"[select] loading cache from {args.cache} ...", flush=True)
        try:
            rows = load_cache(args.cache)
            print(f"[select] loaded {len(rows)} rows from cache", flush=True)
        except Exception as e:  # noqa: BLE001
            print(f"[select] cache load failed: {e}, will fetch", file=sys.stderr, flush=True)
            rows = None

    if rows is None:
        print(f"[select] fetching {DATASET} ...", flush=True)
        rows = fetch_rows()
        print(f"[select] got {len(rows)} rows", flush=True)
        if args.cache:
            save_cache(args.cache, rows)
            print(f"[select] wrote cache to {args.cache}", flush=True)

    repos = parse_repos(args.repo)
    tasks = select_tasks(rows, repos, args.per_repo, args.limit, args.instance)

    output = {
        "generated_at": datetime.now(timezone.utc).isoformat(),
        "dataset": DATASET,
        "repos": repos,
        "per_repo": args.per_repo,
        "limit": args.limit,
        "count": len(tasks),
        "tasks": tasks,
    }

    os.makedirs(os.path.dirname(args.out) or ".", exist_ok=True)
    with open(args.out, "w", encoding="utf-8") as f:
        json.dump(output, f, indent=2, ensure_ascii=False)
    print(f"[select] wrote {len(tasks)} task(s) to {args.out}", flush=True)


if __name__ == "__main__":
    main()
