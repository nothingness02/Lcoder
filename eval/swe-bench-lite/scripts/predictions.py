#!/usr/bin/env python3
"""从 results/<instance_id>/ 导出官方 SWE-bench 评测器兼容的 predictions.jsonl。

官方格式(每行一条 JSON):
  {"instance_id": ..., "model_name_or_path": ..., "model_patch": "...diff text..."}

可用于对接 princeton-nlp/SWE-bench 官方 execution-based evaluator 的预测输入,
或与其他 agent 的 predictions 做同格式对比。

用法: python eval/swe-bench-lite/scripts/predictions.py --results-dir eval/swe-bench-lite/results
"""
import argparse
import json
import os
import sys


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--results-dir", default="eval/swe-bench-lite/results")
    ap.add_argument("--out", default=None, help="输出路径(默认 <results-dir>/predictions.jsonl)")
    args = ap.parse_args()

    out_path = args.out or os.path.join(args.results_dir, "predictions.jsonl")
    count = 0
    skipped = []
    with open(out_path, "w", encoding="utf-8") as out:
        for name in sorted(os.listdir(args.results_dir)):
            rdir = os.path.join(args.results_dir, name)
            result_path = os.path.join(rdir, "result.json")
            patch_path = os.path.join(rdir, "patch.diff")
            if not (os.path.isdir(rdir) and os.path.exists(result_path)):
                continue
            with open(result_path, encoding="utf-8") as f:
                result = json.load(f)
            if not os.path.exists(patch_path):
                skipped.append((name, "no patch.diff"))
                continue
            with open(patch_path, encoding="utf-8") as f:
                patch = f.read()
            record = {
                "instance_id": result.get("instance_id", name),
                "model_name_or_path": result.get("model") or os.environ.get("MODEL_ID", "lcoder"),
                "model_patch": patch,
            }
            out.write(json.dumps(record, ensure_ascii=False) + "\n")
            count += 1

    print(f"[predictions] wrote {count} record(s) to {out_path}", flush=True)
    for name, why in skipped:
        print(f"[predictions] skipped {name}: {why}", file=sys.stderr, flush=True)


if __name__ == "__main__":
    main()
