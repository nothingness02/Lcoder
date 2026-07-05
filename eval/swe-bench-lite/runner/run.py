#!/usr/bin/env python3
"""Host 侧编排:交叉编译 linux lcoder -> 构建镜像 -> (可选)筛选任务 -> 跑任务 -> 汇总。

在 host(Windows,Docker Desktop)上用 python 运行。所有需要外网/隔离的步骤都委托给容器。

用法:
    python run.py --build --select --repo psf/requests   # 全流程:编译+构建镜像+筛选
    python run.py --instance <id>                         # 跑指定任务(已在 tasks.json)
    python run.py                                         # 跑 tasks.json 第一个任务
"""
import argparse
import json
import os
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
EVAL_DIR = os.path.abspath(os.path.join(HERE, ".."))          # eval/swe-bench-lite
REPO_ROOT = os.path.abspath(os.path.join(EVAL_DIR, "..", ".."))
IMAGE_BASE = "lcoder-swe-bench-lite"
DATA_DIR = os.path.join(EVAL_DIR, "data")
RESULTS_DIR = os.path.join(EVAL_DIR, "results")
AGENTS_DIR = os.path.join(REPO_ROOT, "configs", "agents")
# 容器内 agent 的 cwd 是 /workspace/repo，不能把 agents 挂载到该目录下（会干扰源码移动）。
# DefaultModeDirs 还会查找 ~/.lcoder/agents，所以挂载到 /root/.lcoder/agents。
AGENTS_CONTAINER_DIR = "/root/.lcoder/agents"
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


def select_task(repo, instance, limit):
    os.makedirs(DATA_DIR, exist_ok=True)
    cmd = [
        "docker", "run", "--rm",
        "-v", f"{DATA_DIR}:/eval/data",
        image_tag(DEFAULT_PYVER),
        "python", "/eval/scripts/select_task.py",
        "--out", "/eval/data/tasks.json",
    ]
    if instance:
        cmd += ["--instance", instance]
    else:
        cmd += ["--repo", repo, "--limit", str(limit)]
    r = sh(cmd)
    if r.returncode != 0:
        sys.exit("select_task failed")


def run_task(task):
    os.makedirs(RESULTS_DIR, exist_ok=True)
    token = os.environ.get("ANTHROPIC_AUTH_TOKEN", "")
    if not token:
        sys.exit("ANTHROPIC_AUTH_TOKEN not set in environment")
    pyver = task.get("python_version", DEFAULT_PYVER)
    tag = image_tag(pyver)
    # 该 python 版本的镜像若不存在则按需构建。
    if sh(["docker", "image", "inspect", tag],
          stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode != 0:
        build_image(pyver)
    cmd = [
        "docker", "run", "--rm",
        "-e", f"ANTHROPIC_AUTH_TOKEN={token}",
        "-e", f"INSTANCE_ID={task['instance_id']}",
        "-v", f"{DATA_DIR}:/eval/data:ro",
        "-v", f"{RESULTS_DIR}:/eval/results",
        "-v", f"{AGENTS_DIR}:{AGENTS_CONTAINER_DIR}:ro",
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
              f"edits={m.get('file_edits')} dur={res.get('duration_s')}s "
              f"patch={'Y' if res.get('patch_nonempty') else 'N'}", flush=True)
    if rows:
        resolved = sum(1 for r in rows if r.get("status") == "resolved")
        print(f"\nresolved {resolved}/{len(rows)}", flush=True)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--build", action="store_true", help="交叉编译并构建默认镜像")
    ap.add_argument("--select", action="store_true", help="从 HF 筛选任务写 tasks.json")
    ap.add_argument("--repo", default="psf/requests")
    ap.add_argument("--instance", default="")
    ap.add_argument("--limit", type=int, default=1, help="筛选/运行规模最小的前 N 个任务")
    ap.add_argument("--no-run", action="store_true", help="只准备,不跑任务")
    args = ap.parse_args()

    if args.build:
        cross_compile()
        build_image(DEFAULT_PYVER)
    if args.select:
        select_task(args.repo, args.instance, args.limit)

    if not args.no_run:
        if not os.path.isfile(TASKS_FILE):
            sys.exit("tasks.json missing — run with --select first")
        with open(TASKS_FILE, encoding="utf-8") as f:
            tasks = json.load(f)
        if args.instance:
            tasks = [t for t in tasks if t["instance_id"] == args.instance] or tasks
        # 确保交叉编译产物存在(供按需构建其它 python 版本镜像)。
        if not os.path.isfile(BIN_PATH):
            cross_compile()
        # 逐个跑 tasks.json 里的任务(批量测解决率)。
        for t in tasks:
            print(f"\n########## RUN {t['instance_id']} ##########", flush=True)
            run_task(t)
        summarize()


if __name__ == "__main__":
    main()
