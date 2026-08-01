#!/usr/bin/env python3
"""SWE-bench Lite 评测 run 归档与跨 run 对比。

背景(参考 docs 评测标准方案):
  每个"借鉴改动"都应能通过同一任务子集上的评测对比来决定是否接受。
  本脚本把一次评测跑批沉淀为一个可对比的历史快照:

    eval/swe-bench-lite/runs/<run-id>/
      ├── meta.json          归档元数据(时间/变体/模型/协议/任务数)
      ├── config.snapshot/   生效的配置快照(configs/*, eval config, prompts)
      └── results/           该 run 的 results 目录(任务结果 + report)

  并在 runs/INDEX.md 维护所有 run 的对比表,使配置变体之间的
  resolved / cost / duration / edits 差异一目了然。

用法:
    python archive.py --id <run-id> --results-dir <dir> [--variant <name>] [--model <id>]
    python archive.py --index            # 只重建 runs/INDEX.md
"""
import argparse
import json
import os
import shutil
import sys
from datetime import datetime, timezone

HERE = os.path.dirname(os.path.abspath(__file__))
EVAL_DIR = os.path.abspath(os.path.join(HERE, ".."))          # eval/swe-bench-lite
RUNS_DIR = os.path.join(EVAL_DIR, "runs")
DEFAULT_RESULTS = os.path.join(EVAL_DIR, "results")

# 归档时快照哪些配置:相对于仓库根(REPO_ROOT)的路径。
# 这些是"harness"的主要载体 —— 改它们就是改 harness,对比它们就是对比改动。
SNAPSHOT_PATHS = [
    "configs/agents",
    "configs/modes",
    "configs/lcoder.yaml",
    "configs/models.yaml",
    "eval/swe-bench-lite/config",
    "eval/swe-bench-lite/prompts",
]


def repo_root():
    return os.path.abspath(os.path.join(EVAL_DIR, "..", ".."))


def copy_light_results(src, dst):
    """轻量归档:顶层 report/summary 文件 + 每任务 result.json/patch.diff/test_patch.diff。

    丢弃 events.jsonl / context-snapshots / observability.jsonl / audit.jsonl 等
    大体积原始日志(单任务 events 可达数百 MB)。
    """
    os.makedirs(dst, exist_ok=True)
    keep_root = ("report.md", "report.html", "summary.md", "summary.json")
    keep_task = ("result.json", "patch.diff", "test_patch.diff")
    for name in sorted(os.listdir(src)):
        sp = os.path.join(src, name)
        if os.path.isfile(sp) and name in keep_root:
            shutil.copy2(sp, os.path.join(dst, name))
        elif os.path.isdir(sp) and os.path.isfile(os.path.join(sp, "result.json")):
            tdir = os.path.join(dst, name)
            os.makedirs(tdir, exist_ok=True)
            for fn in keep_task:
                fp = os.path.join(sp, fn)
                if os.path.isfile(fp):
                    shutil.copy2(fp, os.path.join(tdir, fn))


def snapshot_config(run_dir):
    """把生效配置复制进 run 目录,供以后精确复现/对比。"""
    snap = os.path.join(run_dir, "config.snapshot")
    root = repo_root()
    for rel in SNAPSHOT_PATHS:
        src = os.path.join(root, rel)
        if not os.path.exists(src):
            continue
        dst = os.path.join(snap, rel)
        if os.path.isdir(src):
            shutil.copytree(src, dst, dirs_exist_ok=True)
        else:
            os.makedirs(os.path.dirname(dst), exist_ok=True)
            shutil.copy2(src, dst)
    return snap


def collect_results(results_dir):
    """读取 results_dir 下所有 result.json,返回 (rows, summary)。"""
    rows = []
    if not os.path.isdir(results_dir):
        return rows, {}
    for iid in sorted(os.listdir(results_dir)):
        rp = os.path.join(results_dir, iid, "result.json")
        if not os.path.isfile(rp):
            continue
        try:
            with open(rp, encoding="utf-8") as f:
                rows.append(json.load(f))
        except Exception as e:  # noqa: BLE001
            print(f"[archive] skip {rp}: {e}", file=sys.stderr, flush=True)

    summary = summarize(rows)
    return rows, summary


def summarize(rows):
    total = len(rows)
    counts = {}
    for r in rows:
        counts[r.get("status", "unknown")] = counts.get(r.get("status", "unknown"), 0) + 1

    def avg(key, root=False, default=0.0):
        vals = []
        for r in rows:
            if root:
                v = r.get(key)
            else:
                v = r.get("metrics", {}).get(key)
            if isinstance(v, (int, float)):
                vals.append(v)
        return round(sum(vals) / len(vals), 4) if vals else default

    resolved = sum(1 for r in rows if r.get("status") == "resolved")
    resolved_initial = sum(1 for r in rows if r.get("initial_status") == "resolved")
    # 编辑行为:用 edit/write 工具实际改文件的任务(诊断 agent 是否落地修改)。
    edited_tasks = sum(
        1 for r in rows
        if (r.get("metrics", {}).get("file_edits", 0) or 0) > 0
        or (r.get("metrics", {}).get("file_writes", 0) or 0) > 0
    )
    # 产出 patch 的任务(可能只 touch 未真正修改,与编辑行为分开看)。
    patched_tasks = sum(
        1 for r in rows
        if (r.get("patch_stats", {}).get("files_changed", 0) or 0) > 0
    )
    return {
        "total": total,
        "resolved": resolved,
        "resolved_initial": resolved_initial,
        "resolved_rate": round(100.0 * resolved / total, 2) if total else 0.0,
        "resolved_initial_rate": round(100.0 * resolved_initial / total, 2) if total else 0.0,
        "edited_tasks": edited_tasks,
        "edited_tasks_rate": round(100.0 * edited_tasks / total, 2) if total else 0.0,
        "patched_tasks": patched_tasks,
        "patched_tasks_rate": round(100.0 * patched_tasks / total, 2) if total else 0.0,
        "counts": counts,
        "avg": {
            "turns": avg("turns"),
            "tool_calls": avg("tool_calls"),
            "file_edits": avg("file_edits"),
            "cost": avg("cost"),
            "duration_s": avg("duration_s", root=True),
            "total_tokens": avg("total_tokens"),
        },
    }


def archive(run_id, results_dir, variant, model, note):
    run_dir = os.path.join(RUNS_DIR, run_id)
    if os.path.exists(run_dir):
        sys.exit(f"[archive] run {run_id} already exists at {run_dir} — pick a new --id")

    rows, summary = collect_results(results_dir)
    if not rows:
        sys.exit(f"[archive] no result.json found in {results_dir}")

    os.makedirs(run_dir, exist_ok=True)

    # 1) 元数据
    meta = {
        "run_id": run_id,
        "created_at": datetime.now(timezone.utc).isoformat(),
        "variant": variant or "",
        "model": model or "",
        "note": note or "",
        "results_dir": os.path.abspath(results_dir),
        "tasks": summary["total"],
        "summary": summary,
    }
    with open(os.path.join(run_dir, "meta.json"), "w", encoding="utf-8") as f:
        json.dump(meta, f, indent=2, ensure_ascii=False)

    # 2) 配置快照
    snapshot_config(run_dir)

    # 3) 拷贝轻量结果(只保留 report/summary 与每任务 result.json/patch.diff)
    #    丢弃 events.jsonl / context-snapshots / observability 等大体积原始日志,
    #    避免单次归档几百 MB 到 GB。需要完整事件时仍可回原始 results_dir。
    copy_light_results(results_dir, os.path.join(run_dir, "results"))

    print(f"[archive] run {run_id}: tasks={summary['total']} "
          f"resolved={summary['resolved']} ({summary['resolved_rate']}%) "
          f"edited_tasks={summary['edited_tasks']} ({summary['edited_tasks_rate']}%)")
    print(f"[archive] wrote {run_dir}")

    # 4) 重建索引
    build_index()
    return run_dir


def build_index():
    """遍历 runs/,生成 INDEX.md 对比表。"""
    if not os.path.isdir(RUNS_DIR):
        return
    runs = []
    for rid in sorted(os.listdir(RUNS_DIR), reverse=True):
        mp = os.path.join(RUNS_DIR, rid, "meta.json")
        if not os.path.isfile(mp):
            continue
        try:
            with open(mp, encoding="utf-8") as f:
                runs.append(json.load(f))
        except Exception:  # noqa: BLE001
            continue

    lines = [
        "# 评测 Runs 索引(配置变体对比)",
        "",
        "> 每次跑批 `archive.py --id <run>` 后自动更新。",
        "> 对比同一任务子集上的不同配置变体,用数据决定是否接受改动。",
        "",
    ]
    if not runs:
        lines.append("(尚无归档 run)")
        with open(os.path.join(RUNS_DIR, "INDEX.md"), "w", encoding="utf-8") as f:
            f.write("\n".join(lines))
        return

    lines.append("| run | 时间 | 变体 | 模型 | 任务 | resolved | 初始resolved | 编辑任务 | 产patch | avg turns | avg cost | avg dur(s) | 备注 |")
    lines.append("|-----|------|------|------|------|----------|--------------|----------|---------|-----------|----------|------------|------|")
    for r in runs:
        s = r.get("summary", {})
        created = (r.get("created_at", "") or "")[:19].replace("T", " ")
        note = (r.get("note", "") or "").replace("|", "/")[:40]
        lines.append(
            f"| {r['run_id']} | {created} | {r.get('variant','')} | {r.get('model','')} | "
            f"{s.get('total',0)} | {s.get('resolved',0)} ({s.get('resolved_rate',0)}%) | "
            f"{s.get('resolved_initial',0)} ({s.get('resolved_initial_rate',0)}%) | "
            f"{s.get('edited_tasks',0)} ({s.get('edited_tasks_rate',0)}%) | "
            f"{s.get('patched_tasks',0)} ({s.get('patched_tasks_rate',0)}%) | "
            f"{s.get('avg',{}).get('turns',0)} | {s.get('avg',{}).get('cost',0)} | "
            f"{s.get('avg',{}).get('duration_s',0)} | {note} |"
        )
    lines.append("")
    lines.append("## 对比指引")
    lines.append("")
    lines.append("- **resolved**: resolved 任务数(反馈后, extended 协议口径)")
    lines.append("- **初始resolved**: 一次评估即解决(initial_status=resolved),接近 official 口径")
    lines.append("- **编辑任务**: 用 edit/write 工具实际改文件的任务占比 —— 诊断 agent 是否落地修改")
    lines.append("- **产patch**: 产出非空 patch 的任务占比(可能只 touch 文件未真正修改,与编辑任务分开看)")
    lines.append("- **avg turns / cost / duration**: 成本与效率对比")
    lines.append("")
    lines.append("接受改动的判据:同一任务子集上 resolved 不降,且 cost/duration 不显著上升;")
    lines.append("或目标指标(如编辑任务占比)有明确提升。")

    with open(os.path.join(RUNS_DIR, "INDEX.md"), "w", encoding="utf-8") as f:
        f.write("\n".join(lines))
    print(f"[archive] updated {os.path.join(RUNS_DIR, 'INDEX.md')} ({len(runs)} runs)")


def main():
    ap = argparse.ArgumentParser(description="SWE-bench Lite 评测 run 归档与对比")
    ap.add_argument("--id", default="", help="run 标识(如 baseline-20260801)")
    ap.add_argument("--results-dir", default=DEFAULT_RESULTS, help="结果根目录")
    ap.add_argument("--variant", default="", help="配置变体名(如 guard-rules-v1)")
    ap.add_argument("--model", default="", help="模型标识(如 kimi-k2.7-code)")
    ap.add_argument("--note", default="", help="备注")
    ap.add_argument("--index", action="store_true", help="只重建 INDEX.md")
    args = ap.parse_args()

    if args.index:
        build_index()
        return
    if not args.id:
        sys.exit("--id is required (or use --index)")

    archive(args.id, args.results_dir, args.variant, args.model, args.note)


if __name__ == "__main__":
    main()
