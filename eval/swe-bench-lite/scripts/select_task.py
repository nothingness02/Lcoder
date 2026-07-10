#!/usr/bin/env python3
"""从 HuggingFace 拉取 SWE-bench Lite 数据,筛选并写出 tasks.json。

在容器内运行(需要外网访问 datasets-server.huggingface.co)。

筛选策略(MVP):
- 默认锁定一个轻量、纯 Python、依赖简单的仓库(psf/requests)。
- 在该仓库的任务中,选 FAIL_TO_PASS + PASS_TO_PASS 规模最小者,降低运行与环境风险。
- 也可用 --instance 指定具体 instance_id。

字段说明:HF 行里的 FAIL_TO_PASS / PASS_TO_PASS 既可能是 list,也可能是 JSON 字符串,这里统一解析为 list。
"""
import argparse
import json
import os
import sys
import urllib.request

DATASET = "princeton-nlp/SWE-bench_Lite"
ROWS_URL = (
    "https://datasets-server.huggingface.co/rows"
    "?dataset={ds}&config=default&split=test&offset={off}&length={ln}"
)

# 仓库 -> 适配的 Python 版本(对齐各任务代码的时代依赖)。未列出者默认 3.11。
REPO_PYVER = {
    "psf/requests": "3.9",
    "sympy/sympy": "3.9",
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


def to_task(row):
    return {
        "instance_id": row["instance_id"],
        "repo": row["repo"],
        "base_commit": row["base_commit"],
        "problem_statement": row.get("problem_statement", ""),
        "test_patch": row.get("test_patch", ""),
        "fail_to_pass": as_list(row.get("FAIL_TO_PASS")),
        "pass_to_pass": as_list(row.get("PASS_TO_PASS")),
        # 适配的 Python 版本(决定容器基础镜像)。
        "python_version": REPO_PYVER.get(row["repo"], "3.11"),
        # MVP 默认测试命令;复杂仓库可在此覆盖。
        "test_cmd": "python -m pytest",
        # 默认安装命令。
        "install_cmd": "pip install -e .",
    }


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--repo", default="psf/requests", help="筛选目标仓库")
    ap.add_argument("--instance", default="", help="指定 instance_id(优先于 --repo 筛选)")
    ap.add_argument("--limit", type=int, default=1, help="写出规模最小的前 N 个任务")
    ap.add_argument("--out", default="/eval/data/tasks.json")
    ap.add_argument("--cache", default="/eval/data/swe-bench-lite-cache.jsonl",
                    help="本地数据集缓存路径")
    ap.add_argument("--refresh", action="store_true",
                    help="强制重新从 HuggingFace 拉取并刷新缓存")
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

    if args.instance:
        chosen = [r for r in rows if r["instance_id"] == args.instance]
        if not chosen:
            print(f"[select] instance {args.instance} not found", file=sys.stderr)
            sys.exit(1)
        tasks = [to_task(chosen[0])]
    else:
        candidates = [r for r in rows if r["repo"] == args.repo]
        if not candidates:
            print(f"[select] no tasks for repo {args.repo}", file=sys.stderr)
            sys.exit(1)
        # 按测试规模升序,挑最小的前 N 个
        candidates.sort(
            key=lambda r: len(as_list(r.get("FAIL_TO_PASS"))) + len(as_list(r.get("PASS_TO_PASS")))
        )
        tasks = [to_task(r) for r in candidates[: max(1, args.limit)]]
        print(
            f"[select] repo={args.repo}: {len(candidates)} candidates, "
            f"picked {len(tasks)}: "
            + ", ".join(t["instance_id"] for t in tasks),
            flush=True,
        )

    with open(args.out, "w", encoding="utf-8") as f:
        json.dump(tasks, f, indent=2, ensure_ascii=False)
    print(f"[select] wrote {len(tasks)} task(s) to {args.out}", flush=True)


if __name__ == "__main__":
    main()
