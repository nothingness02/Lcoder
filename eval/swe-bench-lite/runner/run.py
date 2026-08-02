#!/usr/bin/env python3
"""Host 侧编排:交叉编译 linux lcoder -> 构建镜像 -> (可选)筛选任务 -> 跑任务 -> 汇总。

在 host(Windows,Docker Desktop)上用 python 运行。所有需要外网/隔离的步骤都委托给容器。

用法:
    python run.py --build --select --repo psf/requests   # 全流程:编译+构建镜像+筛选
    python run.py --instance <id>                         # 跑指定任务(已在 tasks.json)
    python run.py                                         # 跑 tasks.json 第一个任务
"""
import argparse
import concurrent.futures
import json
import os
import subprocess
from datetime import datetime
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
EVAL_DIR = os.path.abspath(os.path.join(HERE, ".."))          # eval/swe-bench-lite
REPO_ROOT = os.path.abspath(os.path.join(EVAL_DIR, "..", ".."))
IMAGE_BASE = "lcoder-swe-bench-lite"
DATA_DIR = os.path.join(EVAL_DIR, "data")
RESULTS_DIR = os.path.join(EVAL_DIR, "results")
MODES_DIR = os.path.join(REPO_ROOT, "configs", "modes")
CONFIGS_DIR = os.path.join(REPO_ROOT, "configs")
# 容器内 agent 的 cwd 是 /workspace/repo，不能把 modes 挂载到该目录下（会干扰源码移动）。
# DefaultModeDirs 还会查找 ~/.lcoder/modes，所以挂载到 /root/.lcoder/modes。
MODES_CONTAINER_DIR = "/root/.lcoder/modes"
CONFIGS_CONTAINER_DIR = "/eval/configs"
BIN_PATH = os.path.join(EVAL_DIR, "bin", "lcoder-linux")
TASKS_FILE = os.path.join(DATA_DIR, "tasks.json")
# 筛选阶段(尚无 tasks.json)用的默认镜像版本。
DEFAULT_PYVER = "3.11"


def image_tag(pyver):
    return f"{IMAGE_BASE}:py{pyver}"


def sh(cmd, **kw):
    print("+ " + (cmd if isinstance(cmd, str) else " ".join(cmd)), flush=True)
    return subprocess.run(cmd, shell=isinstance(cmd, str), **kw)


def cross_compile():
    os.makedirs(os.path.dirname(BIN_PATH), exist_ok=True)
    env = dict(os.environ, CGO_ENABLED="0", GOOS="linux", GOARCH="amd64")
    r = sh(["go", "build", "-o", BIN_PATH, "./cmd/lcoder"], cwd=REPO_ROOT, env=env)
    if r.returncode != 0:
        sys.exit("cross-compile failed")
    print(f"[build] linux binary -> {BIN_PATH}", flush=True)


def build_image(pyver):
    # legacy builder 走 daemon 网络(buildkit 在本机访问 registry 偶发超时)。
    env = dict(os.environ, DOCKER_BUILDKIT="0")
    sh(["docker", "pull", f"python:{pyver}-slim"], env=env)
    r = sh(["docker", "build", "--build-arg", f"PYVER={pyver}",
            "-t", image_tag(pyver), "."], cwd=EVAL_DIR, env=env)
    if r.returncode != 0:
        sys.exit("docker build failed")


def select_task(repo, instance, per_repo, limit, refresh=False):
    os.makedirs(DATA_DIR, exist_ok=True)
    cmd = [
        "docker", "run", "--rm",
        "-v", f"{DATA_DIR}:/eval/data",
        image_tag(DEFAULT_PYVER),
        "python", "/eval/scripts/select_task.py",
        "--out", "/eval/data/tasks.json",
        "--cache", "/eval/data/swe-bench-lite-cache.jsonl",
    ]
    if refresh:
        cmd.append("--refresh")
    if instance:
        cmd += ["--instance", instance]
    else:
        cmd += ["--repo", repo, "--per-repo", str(per_repo), "--limit", str(limit)]
    r = sh(cmd)
    if r.returncode != 0:
        sys.exit("select_task failed")


def run_task(task):
    os.makedirs(RESULTS_DIR, exist_ok=True)
    token = os.environ.get("ANTHROPIC_AUTH_TOKEN", "")
    eval_key = os.environ.get("EVAL_API_KEY", "")
    if not token and not eval_key:
        sys.exit("set ANTHROPIC_AUTH_TOKEN (kimi gateway) or EVAL_API_KEY (custom provider)")
    pyver = task.get("python_version", DEFAULT_PYVER)
    tag = image_tag(pyver)
    # 该 python 版本的镜像若不存在则按需构建;二进制比镜像新时也必须重建,
    # 否则容器里跑的是旧 lcoder(旧镜像层不会随源代码自动失效)。
    need_build = sh(["docker", "image", "inspect", tag],
                    stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode != 0
    if not need_build:
        out = subprocess.run(["docker", "image", "inspect", "--format", "{{.Created}}", tag],
                             capture_output=True, text=True).stdout.strip()
        try:
            img_ts = datetime.fromisoformat(out.replace("Z", "+00:00")).timestamp()
            if os.path.getmtime(BIN_PATH) > img_ts:
                print(f"[build] binary newer than image {tag}, rebuilding", flush=True)
                need_build = True
        except ValueError:
            need_build = True
    if need_build:
        build_image(pyver)
    cmd = [
        "docker", "run", "--rm",
        "-e", f"ANTHROPIC_AUTH_TOKEN={token}",
        "-e", f"EVAL_API_KEY={eval_key}",
        "-e", f"INSTANCE_ID={task['instance_id']}",
        "-e", f"OFFICIAL_PROTOCOL={os.environ.get('OFFICIAL_PROTOCOL', '0')}",
        "-e", f"MODEL_ID={os.environ.get('MODEL_ID', 'kimi-k2.7-code')}",
        "-e", f"GOAL_MODE={os.environ.get('GOAL_MODE', '0')}",
        "-e", f"GOAL_TURNS={os.environ.get('GOAL_TURNS', '60')}",
        "-e", f"GOAL_TOKENS={os.environ.get('GOAL_TOKENS', '0')}",
        "-e", "LCODER_MODELS_CONFIG=/eval/configs/models.yaml",
        "-v", f"{DATA_DIR}:/eval/data:ro",
        "-v", f"{RESULTS_DIR}:/eval/results",
        "-v", f"{MODES_DIR}:{MODES_CONTAINER_DIR}:ro",
        "-v", f"{CONFIGS_DIR}:{CONFIGS_CONTAINER_DIR}:ro",
        tag, "python", "/eval/scripts/run_in_container.py",
    ]
    r = sh(cmd)
    return r.returncode


def summarize():
    if not os.path.isdir(RESULTS_DIR):
        return
    print("\n==================== SUMMARY ====================", flush=True)
    rows = []
    for iid in sorted(os.listdir(RESULTS_DIR)):
        rp = os.path.join(RESULTS_DIR, iid, "result.json")
        if not os.path.isfile(rp):
            continue
        with open(rp, encoding="utf-8") as f:
            res = json.load(f)
        rows.append(res)
        m = res.get("metrics", {})
        print(f"- {iid}: {res.get('status')}  "
              f"turns={m.get('turns')} tools={m.get('tool_calls')} "
              f"edits={m.get('file_edits')} f2p={res.get('f2p_passed_count', 0)}/{res.get('f2p_total', 0)} "
              f"p2p={res.get('p2p_passed_count', 0)}/{res.get('p2p_evaluated', 0)} "
              f"dur={res.get('duration_s')}s "
              f"patch={'Y' if res.get('patch_nonempty') else 'N'}", flush=True)
    if rows:
        resolved = sum(1 for r in rows if r.get("status") == "resolved")
        print(f"\nresolved {resolved}/{len(rows)}", flush=True)

    summary = build_summary(rows)
    with open(os.path.join(RESULTS_DIR, "summary.json"), "w", encoding="utf-8") as f:
        json.dump(summary, f, indent=2, ensure_ascii=False)
    with open(os.path.join(RESULTS_DIR, "summary.md"), "w", encoding="utf-8") as f:
        f.write(render_summary_md(summary))
    print(f"[summary] wrote {RESULTS_DIR}/summary.json and summary.md", flush=True)

    report_script = os.path.join(EVAL_DIR, "scripts", "report.py")
    sh(["python", report_script, "--results-dir", RESULTS_DIR, "--out-prefix", "report"])


def build_summary(rows):
    total = len(rows)
    counts = {}
    for r in rows:
        counts[r.get("status", "unknown")] = counts.get(r.get("status", "unknown"), 0) + 1
    resolved = counts.get("resolved", 0)

    def avg(key, subkey=None, root=False, default=0):
        vals = []
        for r in rows:
            if root:
                v = r.get(key)
            elif subkey:
                v = r.get(key, {}).get(subkey)
            else:
                v = r.get("metrics", {}).get(key)
            if isinstance(v, (int, float)):
                vals.append(v)
            elif isinstance(v, str):
                try:
                    vals.append(float(v))
                except ValueError:
                    pass
        if not vals:
            return default
        return round(sum(vals) / len(vals), 2)

    return {
        "total": total,
        "resolved": resolved,
        "resolved_rate": round(resolved / total, 4) if total else 0.0,
        "counts": counts,
        "avg": {
            "turns": avg("turns"),
            "tool_calls": avg("tool_calls"),
            "file_edits": avg("file_edits"),
            "cost": avg("cost"),
            "duration_s": avg("duration_s", root=True),
            "f2p_passed_count": avg("f2p_passed_count", root=True),
            "p2p_passed_count": avg("p2p_passed_count", root=True),
            "total_tokens": avg("total_tokens"),
            "patch_files_changed": avg("patch_stats", "files_changed"),
        },
        "tasks": rows,
    }


def render_summary_md(summary):
    lines = [
        "# SWE-bench Lite 评估汇总",
        "",
        f"- 任务总数: {summary['total']}",
        f"- 已解决 (resolved): {summary['resolved']} / {summary['total']} ({summary['resolved_rate']*100:.2f}%)",
        "- 状态分布:",
    ]
    for status, cnt in sorted(summary["counts"].items()):
        lines.append(f"  - {status}: {cnt}")
    lines.append("")
    lines.append("| 指标 | 平均值 |")
    lines.append("|------|--------|")
    for k, v in summary["avg"].items():
        lines.append(f"| {k} | {v} |")
    lines.append("")
    lines.append("## 任务明细")
    lines.append("")
    lines.append("| instance_id | status | turns | tools | edits | f2p | p2p | files | cost | duration_s | model |")
    lines.append("|-------------|--------|-------|-------|-------|-----|-----|-------|------|------------|-------|")
    for r in summary["tasks"]:
        m = r.get("metrics", {})
        model = r.get("model", "")
        f2p = f"{r.get('f2p_passed_count', 0)}/{r.get('f2p_total', 0)}"
        p2p = f"{r.get('p2p_passed_count', 0)}/{r.get('p2p_evaluated', 0)}"
        files = r.get("patch_stats", {}).get("files_changed", 0)
        lines.append(
            f"| {r.get('instance_id', '')} | {r.get('status', '')} | "
            f"{m.get('turns', '')} | {m.get('tool_calls', '')} | "
            f"{m.get('file_edits', '')} | {f2p} | {p2p} | {files} | "
            f"{m.get('cost', '')} | {r.get('duration_s', '')} | {model} |"
        )
    lines.append("")
    return "\n".join(lines)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--build", action="store_true", help="交叉编译并构建默认镜像")
    ap.add_argument("--select", action="store_true", help="从 HF 筛选任务写 tasks.json")
    ap.add_argument("--refresh", action="store_true", help="--select 时强制刷新数据集缓存")
    ap.add_argument("--repo", default="psf/requests,sympy/sympy,pallets/flask,pytest-dev/pytest",
                    help="逗号分隔的目标仓库(默认覆盖多个轻量仓库)")
    ap.add_argument("--instance", default="")
    ap.add_argument("--per-repo", type=int, default=5,
                    help="每个仓库筛选规模最小的前 N 个任务")
    ap.add_argument("--limit", type=int, default=50,
                    help="筛选任务总数上限(0=不限制)")
    ap.add_argument("--sample", type=int, default=0,
                    help="只运行 tasks.json 中的前 N 个任务(0=全部)")
    ap.add_argument("--workers", type=int, default=1,
                    help="并行运行任务数(默认 1)")
    ap.add_argument("--no-run", action="store_true", help="只准备,不跑任务")
    ap.add_argument("--variant", default="",
                    help="配置变体名(如 guard-rules-v1),记录到归档 meta")
    ap.add_argument("--run-id", default="",
                    help="跑批后归档为 runs/<run-id>(留空则不归档)")
    ap.add_argument("--note", default="",
                    help="归档备注(如接受/验证某改动)")
    args = ap.parse_args()

    if args.build:
        cross_compile()
        build_image(DEFAULT_PYVER)
    if args.select:
        select_task(args.repo, args.instance, args.per_repo, args.limit,
                    refresh=args.refresh)

    if not args.no_run:
        if not os.path.isfile(TASKS_FILE):
            sys.exit("tasks.json missing — run with --select first")
        with open(TASKS_FILE, encoding="utf-8") as f:
            data = json.load(f)
        # select_task.py 新格式把任务列表放在 tasks 字段;旧格式直接是列表。
        tasks = data.get("tasks", data) if isinstance(data, dict) else data
        if isinstance(data, dict):
            print(f"[run] loaded {len(tasks)} task(s) from {data.get('repos', [])}", flush=True)
        if args.instance:
            tasks = [t for t in tasks if t["instance_id"] == args.instance] or tasks
        if args.sample > 0:
            tasks = tasks[:args.sample]
        # 确保交叉编译产物存在(供按需构建其它 python 版本镜像)。
        if not os.path.isfile(BIN_PATH):
            cross_compile()
        # 逐个或并行跑 tasks.json 里的任务(批量测解决率)。
        if args.workers <= 1:
            for t in tasks:
                print(f"\n########## RUN {t['instance_id']} ##########", flush=True)
                run_task(t)
        else:
            print(f"\n########## RUN {len(tasks)} tasks with {args.workers} workers ##########", flush=True)

            def run_one(t):
                print(f"\n########## START {t['instance_id']} ##########", flush=True)
                run_task(t)
                print(f"\n########## END {t['instance_id']} ##########", flush=True)

            with concurrent.futures.ThreadPoolExecutor(max_workers=args.workers) as ex:
                list(ex.map(run_one, tasks))
        summarize()

        if args.run_id:
            sys.path.insert(0, os.path.join(EVAL_DIR, "scripts"))
            import archive
            archive.archive(
                args.run_id,
                RESULTS_DIR,
                variant=args.variant,
                model=os.environ.get("MODEL_ID", ""),
                note=args.note,
            )


if __name__ == "__main__":
    main()
