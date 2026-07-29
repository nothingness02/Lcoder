#!/usr/bin/env python3
"""容器内单任务编排:setup -> baseline -> agent -> patch -> evaluate -> metrics。

在 SWE-bench 镜像内运行。读取 /eval/data/tasks.json,按环境变量 INSTANCE_ID 选任务,
所有产物写入 /eval/results/<instance_id>/。

评测协议(对齐 SWE-bench):
  1. clone 仓库并 checkout 到 base_commit,安装依赖。
  2. baseline 校验:应用 test_patch,跑 FAIL_TO_PASS(应失败)+ PASS_TO_PASS(应通过),
     记录 test_before.log,然后**反向撤销** test_patch,使 agent 看不到 gold 测试。
  3. 在纯净 base 上运行 lcoder agent 修复源码。
  4. 提取 agent 的代码改动为 patch.diff(此时尚未含 test_patch)。
  5. 在 agent 改动之上应用 test_patch,跑 F2P + P2P,记录 test_after.log。
  6. 分类:resolved / partial / failed / timeout / error,汇总丰富指标。
"""
import hashlib
import json
import os
import subprocess
import sys
import time

import metrics

WORKDIR = "/workspace/repo"
RESULTS_ROOT = "/eval/results"
TASKS_FILE = "/eval/data/tasks.json"
CONFIG = "/eval/config/lcoder.yaml"
PROMPT_TMPL = "/eval/prompts/swe_task.txt"

AGENT_TIMEOUT_S = int(os.environ.get("AGENT_TIMEOUT_S", "1500"))
INSTALL_TIMEOUT_S = int(os.environ.get("INSTALL_TIMEOUT_S", "1200"))
TEST_TIMEOUT_S = int(os.environ.get("TEST_TIMEOUT_S", "600"))

# 官方 SWE-bench 协议是一次性评估:agent 产出 patch 后只评测一次,
# 无测试反馈重试,PASS_TO_PASS 全量运行。OFFICIAL_PROTOCOL=1 时强制遵守,
# 使分数可与官方/其他 agent 横向对比;默认 extended 模式(有反馈+P2P 截断)。
OFFICIAL_PROTOCOL = os.environ.get("OFFICIAL_PROTOCOL", "0") == "1"

# PASS_TO_PASS 可能很多,MVP 限制数量以约束耗时(非静默截断:会在结果里记录)。
P2P_CAP = 0 if OFFICIAL_PROTOCOL else int(os.environ.get("P2P_CAP", "20"))
# 当 fail_to_pass 未通过时,把测试输出反馈给 agent 让它再试几轮。
FEEDBACK_ATTEMPTS = 0 if OFFICIAL_PROTOCOL else int(os.environ.get("FEEDBACK_ATTEMPTS", "2"))
FEEDBACK_TIMEOUT_S = int(os.environ.get("FEEDBACK_TIMEOUT_S", "600"))


def write_observability_config(rdir):
    """写出 observability.yaml:把 trace/metric 和上下文快照固定到任务结果目录。"""
    cfg = {
        "exporter": {
            "type": "file",
            "output": os.path.join(rdir, "observability.jsonl"),
        },
        "audit": {"enabled": True, "path": os.path.join(rdir, "audit.jsonl")},
        "sampling": {"enabled": True, "rate": 1.0},
        "context_snapshots": {
            "enabled": True,
            "output_dir": os.path.join(rdir, "context-snapshots"),
            "max_messages_per_block": 0,
        },
    }
    os.makedirs("/root/.lcoder", exist_ok=True)
    path = "/root/.lcoder/observability.yaml"
    with open(path, "w", encoding="utf-8") as f:
        # 手动写成 YAML,避免额外依赖。
        f.write("exporter:\n")
        f.write(f"  type: {cfg['exporter']['type']}\n")
        f.write(f"  output: {cfg['exporter']['output']}\n")
        f.write("audit:\n")
        f.write(f"  enabled: {str(cfg['audit']['enabled']).lower()}\n")
        f.write(f"  path: {cfg['audit']['path']}\n")
        f.write("sampling:\n")
        f.write(f"  enabled: {str(cfg['sampling']['enabled']).lower()}\n")
        f.write(f"  rate: {cfg['sampling']['rate']}\n")
        f.write("context_snapshots:\n")
        f.write(f"  enabled: {str(cfg['context_snapshots']['enabled']).lower()}\n")
        f.write(f"  output_dir: {cfg['context_snapshots']['output_dir']}\n")
        f.write(f"  max_messages_per_block: {cfg['context_snapshots']['max_messages_per_block']}\n")
    return cfg


def run(cmd, cwd=None, timeout=None, env=None, capture=True):
    """执行命令,返回 (returncode, stdout+stderr)。"""
    print(f"$ {cmd if isinstance(cmd, str) else ' '.join(cmd)}", flush=True)
    p = subprocess.run(
        cmd,
        cwd=cwd,
        shell=isinstance(cmd, str),
        timeout=timeout,
        env=env,
        stdout=subprocess.PIPE if capture else None,
        stderr=subprocess.STDOUT if capture else None,
        text=True,
    )
    out = p.stdout or ""
    return p.returncode, out


def load_task():
    with open(TASKS_FILE, encoding="utf-8") as f:
        data = json.load(f)
    # select_task.py 新格式把任务列表放在 tasks 字段;旧格式直接是列表。
    tasks = data.get("tasks", data) if isinstance(data, dict) else data
    iid = os.environ.get("INSTANCE_ID", "")
    if iid:
        for t in tasks:
            if t["instance_id"] == iid:
                return t
        raise SystemExit(f"INSTANCE_ID {iid} not in tasks.json")
    return tasks[0]


def git(args, cwd=WORKDIR, timeout=300):
    return run(["git"] + args, cwd=cwd, timeout=timeout)


def test_files_from_patch(patch_text):
    """从 test_patch 的 diff header(+++ b/...)提取被改动的测试文件路径。"""
    files = []
    for line in patch_text.splitlines():
        if line.startswith("+++ b/"):
            files.append(line[len("+++ b/"):].strip())
    return files


def resolve_nodes(nodes, patch_text):
    """把 FAIL_TO_PASS / PASS_TO_PASS 解析为 pytest 可识别的 node id。"""
    tfiles = test_files_from_patch(patch_text)
    out = []
    for n in nodes:
        if "::" in n or "/" in n:
            out.append(n)
        elif tfiles:
            out.append(f"{tfiles[0]}::{n}")
        else:
            out.append(n)
    return out


def run_pytest(nodes, outpath, env):
    """运行一组 pytest 节点,返回 (all_passed, returncode, summary)。"""
    if not nodes:
        with open(outpath, "w") as f:
            f.write("(no tests in this group)\n")
        return True, 0, {"passed": 0, "failed": 0, "error": 0, "skipped": 0, "total": 0}
    cmd = ["python", "-m", "pytest", "-p", "no:cacheprovider", "--tb=short", "-q"] + nodes
    rc, out = run(cmd, cwd=WORKDIR, timeout=TEST_TIMEOUT_S, env=env)
    with open(outpath, "w", encoding="utf-8") as f:
        f.write(out)
    summary = metrics.parse_test_summary(out)
    return rc == 0, rc, summary


def apply_patch(patch_path, cwd=WORKDIR, timeout=120):
    """应用 patch,失败时尝试 --3way 合并。"""
    rc, out = run(["git", "apply", patch_path], cwd=cwd, timeout=timeout)
    if rc == 0:
        return 0, out
    rc2, out2 = run(["git", "apply", "--3way", patch_path], cwd=cwd, timeout=timeout)
    if rc2 == 0:
        return 0, out2
    return rc2, out + "\n---3way attempt---\n" + out2


def truncate_test_output(output, max_chars=6000, head_lines=80, tail_lines=80):
    """智能截断 pytest 输出,保留头部和尾部。"""
    if len(output) <= max_chars:
        return output
    lines = output.splitlines()
    if len(lines) <= head_lines + tail_lines:
        return output[:max_chars]
    head = "\n".join(lines[:head_lines])
    tail = "\n".join(lines[-tail_lines:])
    omitted = len(output) - len(head) - len(tail)
    return f"{head}\n\n... [{omitted} chars truncated] ...\n\n{tail}"


def build_feedback_prompt(f2p_nodes, test_output, attempt, p2p_nodes=None, p2p_output=None):
    """把 pytest 失败输出组装成给 agent 的反馈 prompt。"""
    output = truncate_test_output(test_output)
    nodes = "\n".join(f2p_nodes)
    sections = [
        f"反馈 #{attempt + 1}: 当前修改仍未通过以下测试:\n{nodes}",
        f"pytest 输出(节选):\n```\n{output}\n```",
    ]
    if p2p_nodes:
        p2p_out = truncate_test_output(p2p_output or "")
        sections.append(
            "同时以下回归测试也失败了:\n"
            + "\n".join(p2p_nodes)
            + f"\n\npytest 输出:\n```\n{p2p_out}\n```"
        )
    sections.append(
        "请分析失败原因,在源码中定位根因并继续修改。"
        "不要改动测试文件。修改完成后会再次运行这些测试。"
    )
    return "\n\n".join(sections)


def run_lcoder_agent(runtime_cfg, prompt, events_path, stderr_path, env, timeout,
                     continue_session=False, mode="a"):
    """运行 lcoder agent,将 JSON 事件写入/追加到 events_path。"""
    cmd = ["lcoder", "--config", runtime_cfg, "--json", "-p", prompt]
    if continue_session:
        cmd.append("--continue")
    with open(events_path, mode, encoding="utf-8") as ev, \
         open(stderr_path, "a", encoding="utf-8") as er:
        p = subprocess.run(cmd, cwd=WORKDIR, env=env, stdout=ev, stderr=er, timeout=timeout)
    return p.returncode


def extract_agent_patch(rdir):
    """提取当前工作树的改动到 rdir/patch.diff,返回 diff 文本。"""
    run(["git", "add", "-A"], cwd=WORKDIR, timeout=120)
    rc, diff = run(["git", "diff", "--cached", "HEAD"], cwd=WORKDIR, timeout=120)
    run(["git", "reset", "-q", "HEAD"], cwd=WORKDIR, timeout=120)
    path = os.path.join(rdir, "patch.diff")
    with open(path, "w", encoding="utf-8") as f:
        f.write(diff)
    return diff


def stage_timer(result, name, start):
    """记录阶段耗时到 result['stage_durations']。"""
    result.setdefault("stage_durations", {})[name] = round(time.time() - start, 1)


def classify_status(f2p_passed, p2p_passed):
    """根据 F2P/P2P 结果分类为 resolved / partial / failed。"""
    if not f2p_passed:
        return "failed"
    if f2p_passed and p2p_passed:
        return "resolved"
    return "partial"


def start_local_httpbin():
    """老 requests 测试套件直连 httpbin.org,评测网络不可达时会挂到超时。
    在容器内本地拉起 httpbin 并把 httpbin.org 指回 127.0.0.1,使测试完全离线。
    失败只告警不致命(非 requests 仓库不受影响)。"""
    import urllib.request

    try:
        with open("/etc/hosts", "a", encoding="utf-8") as f:
            f.write("\n127.0.0.1 httpbin.org\n")
        proc = subprocess.Popen(
            [sys.executable, "-c",
             "from gevent.pywsgi import WSGIServer; from httpbin import app; "
             "WSGIServer(('127.0.0.1', 8080), app).serve_forever()"],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        for _ in range(50):
            try:
                if urllib.request.urlopen("http://127.0.0.1:8080/get", timeout=1).status == 200:
                    print("[httpbin] local server ready on :8080", flush=True)
                    return proc
            except Exception:
                time.sleep(0.2)
        print("[httpbin] WARNING: local server not ready in 10s", flush=True)
        return proc
    except Exception as e:
        print(f"[httpbin] WARNING: failed to start local server: {e}", flush=True)
        return None


def main():
    task = load_task()
    iid = task["instance_id"]
    rdir = os.path.join(RESULTS_ROOT, iid)
    os.makedirs(rdir, exist_ok=True)

    result = {
        "instance_id": iid,
        "repo": task["repo"],
        "base_commit": task["base_commit"],
        "status": "error",
        "initial_status": "error",
        "final_status": "error",
        "stages": {},
        "stage_durations": {},
        "protocol": "official" if OFFICIAL_PROTOCOL else "extended",
        "model": os.environ.get("MODEL_ID", ""),
        "p2p_evaluated": 0,
        "p2p_total": len(task["pass_to_pass"]),
        "p2p_capped": False,
        "f2p_total": len(task["fail_to_pass"]),
        "feedback_attempts_used": 0,
        "patch_stats": {},
        "test_summary": {},
        "tool_chain": [],
        "context_snapshots": [],
    }
    t0 = time.time()
    env = dict(os.environ)
    env["HOME"] = "/root"

    test_patch_path = os.path.join(rdir, "test_patch.diff")
    with open(test_patch_path, "w", encoding="utf-8") as f:
        f.write(task["test_patch"])

    try:
        # 1) 取源码 + 造合成 base -------------------------------------------
        stage_start = time.time()
        os.makedirs("/workspace", exist_ok=True)
        sha = task["base_commit"]
        tar_url = f"https://codeload.github.com/{task['repo']}/tar.gz/{sha}"
        clone_ok = False
        for attempt in range(1, 7):
            try:
                run(["rm", "-rf", WORKDIR, "/tmp/ex", "/tmp/src.tgz"])
                import urllib.request, tarfile, shutil
                urllib.request.urlretrieve(tar_url, "/tmp/src.tgz")
                os.makedirs("/tmp/ex", exist_ok=True)
                with tarfile.open("/tmp/src.tgz") as tf:
                    tf.extractall("/tmp/ex")
                subs = [os.path.join("/tmp/ex", d) for d in os.listdir("/tmp/ex")]
                shutil.move(subs[0], WORKDIR)
                git(["init", "-q"], timeout=60)
                git(["config", "user.email", "eval@lcoder"], timeout=30)
                git(["config", "user.name", "lcoder-eval"], timeout=30)
                git(["add", "-A"], timeout=300)
                git(["commit", "-q", "-m", f"base {sha}"], timeout=300)
                clone_ok = True
                break
            except Exception as ce:  # noqa: BLE001
                print(f"[clone] attempt {attempt} failed: {ce}", flush=True)
                time.sleep(min(5 * attempt, 30))
        if not clone_ok:
            result["stages"]["clone"] = "failed"
            raise RuntimeError("source fetch failed after retries")
        result["stages"]["clone"] = "ok"
        stage_timer(result, "clone", stage_start)

        # 2) install ----------------------------------------------------------
        stage_start = time.time()
        rc, out = run(task["install_cmd"], cwd=WORKDIR, timeout=INSTALL_TIMEOUT_S, env=env)
        with open(os.path.join(rdir, "install.log"), "w", encoding="utf-8") as f:
            f.write(out)
        result["stages"]["install"] = "ok" if rc == 0 else "failed"
        stage_timer(result, "install", stage_start)
        if rc != 0:
            raise RuntimeError("install failed")

        # 裸函数名(sympy 等)解析为 '<test_file>::<func>',full node id 原样保留。
        f2p = resolve_nodes(task["fail_to_pass"], task["test_patch"])
        p2p_all = resolve_nodes(task["pass_to_pass"], task["test_patch"])
        p2p = p2p_all if P2P_CAP <= 0 else p2p_all[:P2P_CAP]
        result["p2p_evaluated"] = len(p2p)
        result["p2p_capped"] = len(p2p) < len(p2p_all)

        httpbin_proc = start_local_httpbin()

        # 3) baseline:应用 test_patch,确认 F2P 失败 / P2P 通过 ----------------
        stage_start = time.time()
        rc, out = apply_patch(test_patch_path, cwd=WORKDIR, timeout=120)
        if rc != 0:
            result["stages"]["baseline_apply_test_patch"] = "failed"
            with open(os.path.join(rdir, "test_patch_apply.log"), "w") as f:
                f.write(out)
            raise RuntimeError("test_patch did not apply on base")
        result["stages"]["baseline_apply_test_patch"] = "ok"

        f2p_before, _, f2p_before_summary = run_pytest(
            f2p, os.path.join(rdir, "test_before.log"), env
        )
        p2p_before, _, p2p_before_summary = run_pytest(
            p2p, os.path.join(rdir, "test_before_p2p.log"), env
        )
        result["baseline"] = {
            "fail_to_pass_passed": f2p_before,
            "pass_to_pass_passed": p2p_before,
        }
        result["test_summary"]["before"] = {
            "fail_to_pass": f2p_before_summary,
            "pass_to_pass": p2p_before_summary,
        }
        stage_timer(result, "baseline", stage_start)

        # 撤销 test_patch,让 agent 看不到 gold 测试
        run(["git", "apply", "-R", test_patch_path], cwd=WORKDIR, timeout=120)
        git(["checkout", "-q", "--", "."])
        run(["git", "clean", "-fdq"], cwd=WORKDIR, timeout=120)

        # 4) agent 运行 -------------------------------------------------------
        with open(PROMPT_TMPL, encoding="utf-8") as f:
            tmpl = f.read()
        prompt = tmpl.format(
            repo=task["repo"],
            workdir=WORKDIR,
            problem_statement=task["problem_statement"],
            fail_to_pass="\n".join(f2p),
            test_cmd=task["test_cmd"] + " " + " ".join(f2p[:3]),
        )
        events_path = os.path.join(rdir, "events.jsonl")
        agent_stderr = os.path.join(rdir, "agent.stderr.log")
        runtime_cfg = "/tmp/lcoder-runtime.yaml"
        with open(CONFIG, encoding="utf-8") as cf:
            cfg_text = cf.read()
        cfg_text = cfg_text.replace(
            "{env:ANTHROPIC_AUTH_TOKEN}", os.environ.get("ANTHROPIC_AUTH_TOKEN", "")
        )
        with open(runtime_cfg, "w", encoding="utf-8") as cf:
            cf.write(cfg_text)

        write_observability_config(rdir)

        agent_timed_out = False
        agent_duration_s = 0.0

        def agent_round(prompt_text, timeout, continue_session, mode, stage_key):
            nonlocal agent_duration_s
            start = time.time()
            timed_out = False
            try:
                rc = run_lcoder_agent(
                    runtime_cfg, prompt_text, events_path, agent_stderr, env,
                    timeout, continue_session=continue_session, mode=mode,
                )
            except subprocess.TimeoutExpired:
                timed_out = True
                rc = None
            dur = time.time() - start
            agent_duration_s += dur
            if timed_out:
                result["stages"][stage_key] = "timeout"
            else:
                result["stages"][stage_key] = "ok" if rc == 0 else f"exit_{rc}"
            return rc, timed_out, dur

        stage_start = time.time()
        rc, timed_out, _ = agent_round(prompt, AGENT_TIMEOUT_S, False, "w", "agent")
        agent_timed_out = timed_out
        stage_timer(result, "agent", stage_start)

        # 5) 评测:在 agent 改动之上应用 test_patch,先记录 initial_status,再反馈 -----
        stage_start = time.time()
        f2p_after = False
        p2p_after = False
        f2p_after_summary = {}
        p2p_after_summary = {}
        eval_status = "failed"
        initial_status = "failed"
        final_status = "failed"
        test_patch_applied = False
        feedback_attempts_used = 0

        if agent_timed_out:
            eval_status = initial_status = final_status = "timeout"
        else:
            rc, out = apply_patch(test_patch_path, cwd=WORKDIR, timeout=120)
            if rc != 0:
                with open(os.path.join(rdir, "eval_apply.log"), "w") as f:
                    f.write(out)
                result["stages"]["eval_apply_test_patch"] = "failed"
                eval_status = initial_status = final_status = "error"
            else:
                result["stages"]["eval_apply_test_patch"] = "ok"
                test_patch_applied = True
                f2p_log = os.path.join(rdir, "test_after.log")
                f2p_after, _, f2p_after_summary = run_pytest(f2p, f2p_log, env)
                if f2p_after:
                    p2p_log = os.path.join(rdir, "test_after_p2p.log")
                    p2p_after, _, p2p_after_summary = run_pytest(p2p, p2p_log, env)
                else:
                    p2p_after = False
                    p2p_after_summary = {}

                initial_status = classify_status(f2p_after, p2p_after)
                result["test_summary"]["initial"] = {
                    "fail_to_pass": f2p_after_summary,
                    "pass_to_pass": p2p_after_summary,
                }

                if initial_status == "resolved":
                    final_status = eval_status = "resolved"
                else:
                    # 反馈循环:最多 FEEDBACK_ATTEMPTS 轮
                    fb_attempt = 0
                    while fb_attempt < FEEDBACK_ATTEMPTS:
                        run(["git", "apply", "-R", test_patch_path], cwd=WORKDIR, timeout=120)
                        test_patch_applied = False

                        if f2p_after:
                            with open(f2p_log, encoding="utf-8") as f:
                                f2p_output = f.read()
                            with open(p2p_log, encoding="utf-8") as f:
                                p2p_output = f.read()
                            fb_prompt = build_feedback_prompt(
                                f2p, f2p_output, fb_attempt,
                                p2p_nodes=p2p, p2p_output=p2p_output,
                            )
                        else:
                            with open(f2p_log, encoding="utf-8") as f:
                                test_output = f.read()
                            fb_prompt = build_feedback_prompt(f2p, test_output, fb_attempt)

                        stage_key = f"agent_feedback_{fb_attempt + 1}"
                        fb_start = time.time()
                        rc, timed_out, _ = agent_round(
                            fb_prompt, FEEDBACK_TIMEOUT_S, True, "a", stage_key,
                        )
                        stage_timer(result, stage_key, fb_start)
                        if timed_out:
                            agent_timed_out = True
                            final_status = eval_status = "timeout"
                            break

                        rc, out = apply_patch(test_patch_path, cwd=WORKDIR, timeout=120)
                        if rc != 0:
                            with open(os.path.join(rdir, f"eval_apply_fb_{fb_attempt + 1}.log"), "w") as f:
                                f.write(out)
                            result["stages"][f"eval_apply_test_patch_fb_{fb_attempt + 1}"] = "failed"
                            final_status = eval_status = "error"
                            break
                        test_patch_applied = True

                        fb_attempt += 1
                        feedback_attempts_used = fb_attempt
                        f2p_log = os.path.join(rdir, f"test_after_fb_{fb_attempt}.log")
                        f2p_after, _, f2p_after_summary = run_pytest(f2p, f2p_log, env)
                        if f2p_after:
                            p2p_log = os.path.join(rdir, f"test_after_fb_{fb_attempt}_p2p.log")
                            p2p_after, _, p2p_after_summary = run_pytest(p2p, p2p_log, env)
                            if p2p_after:
                                final_status = eval_status = "resolved"
                                break
                        else:
                            p2p_after = False
                            p2p_after_summary = {}

                        if fb_attempt >= FEEDBACK_ATTEMPTS:
                            final_status = eval_status = classify_status(f2p_after, p2p_after)
                            break

                    if not agent_timed_out and eval_status not in ("resolved", "partial", "error"):
                        final_status = eval_status = classify_status(f2p_after, p2p_after)

        result["fail_to_pass_passed"] = f2p_after
        result["pass_to_pass_passed"] = p2p_after
        result["status"] = eval_status
        result["initial_status"] = initial_status
        result["final_status"] = final_status
        result["agent_duration_s"] = round(agent_duration_s, 1)
        result["feedback_attempts_used"] = feedback_attempts_used
        result["f2p_passed_count"] = f2p_after_summary.get("passed", 0)
        result["p2p_passed_count"] = p2p_after_summary.get("passed", 0) if p2p_after_summary else 0
        result["test_summary"]["after"] = {
            "fail_to_pass": f2p_after_summary,
            "pass_to_pass": p2p_after_summary if p2p_after else {},
        }
        stage_timer(result, "eval", stage_start)

        # 6) 提取 agent patch(不含 test_patch) -----------------------------
        stage_start = time.time()
        if test_patch_applied:
            run(["git", "apply", "-R", test_patch_path], cwd=WORKDIR, timeout=120)
        diff = extract_agent_patch(rdir)
        result["patch_stats"] = metrics.patch_stats(diff)
        result["patch_nonempty"] = result["patch_stats"]["non_empty"]
        stage_timer(result, "patch_extraction", stage_start)

        # 7) 指标 -------------------------------------------------------------
        result["metrics"], obs_perf, snapshot_dir = collect_metrics(events_path, env, rdir)
        result["context_snapshots"] = metrics.list_context_snapshots(snapshot_dir)
        result["observability_perf"] = obs_perf
        model_id = result["metrics"].get("model", "")
        provider_id = result["metrics"].get("provider", "")
        if provider_id and model_id:
            result["model"] = f"{provider_id}/{model_id}"
        elif model_id:
            result["model"] = model_id

    except Exception as e:  # noqa: BLE001
        result["error"] = str(e)
        print(f"[run] ERROR: {e}", file=sys.stderr, flush=True)

    result["duration_s"] = round(time.time() - t0, 1)
    with open(os.path.join(rdir, "result.json"), "w", encoding="utf-8") as f:
        json.dump(result, f, indent=2, ensure_ascii=False)
    print(f"[run] {iid} -> status={result['status']} "
          f"({result['duration_s']}s)", flush=True)
    print(json.dumps(result, indent=2, ensure_ascii=False))


def collect_metrics(events_path, env, rdir):
    """从 events.jsonl + observability 文件汇总指标。"""
    event_m = metrics.collect_event_metrics(events_path)
    observability_path = os.path.join(rdir, "observability.jsonl")
    obs_m = metrics.collect_observability_tokens(WORKDIR, observability_path=observability_path)
    obs_perf = metrics.collect_observability_perf(observability_path)
    obs_perf_summary = metrics.summarize_observability_perf(obs_perf)
    snapshot_dir = os.path.join(rdir, "context-snapshots")

    m = {
        "turns": event_m["turns"],
        "agent_rounds": event_m["agent_rounds"],
        "tool_calls": event_m["tool_calls"],
        "file_edits": event_m["file_edits"],
        "file_writes": event_m["file_writes"],
        "file_reads": event_m["file_reads"],
        "bash_commands": event_m["bash_commands"],
        "test_commands": event_m["test_commands"],
        "grep_searches": event_m["grep_searches"],
        "find_searches": event_m["find_searches"],
        "code_index_lookups": event_m["code_index_lookups"],
        "compactions": event_m["compactions"],
        "messages": event_m["messages"],
        "errors": event_m["errors"],
        "max_consecutive_errors": event_m["max_consecutive_errors"],
        "last_tool_before_error": event_m["last_tool_before_error"],
        "tool_counts": event_m["tool_counts"],
        "tool_chain": metrics.collect_tool_chain(events_path),
        "tokens": obs_m["tokens"],
        "total_tokens": sum(obs_m["tokens"].values()),
        "cost": obs_m["cost"],
        "llm_calls": obs_perf_summary["llm_calls"] or obs_m["llm_calls"],
        "provider": obs_m["provider"],
        "model": obs_m["model"],
        "cache_hit_rate": obs_perf_summary["cache_hit_rate"],
        "observability_perf": obs_perf_summary,
    }
    if m["turns"] > 0:
        m["tools_per_turn"] = round(m["tool_calls"] / m["turns"], 2)
        m["errors_per_turn"] = round(m["errors"] / m["turns"], 2)
    else:
        m["tools_per_turn"] = 0.0
        m["errors_per_turn"] = 0.0
    if m["llm_calls"] > 0:
        m["cost_per_llm_call"] = round(m["cost"] / m["llm_calls"], 6)
    else:
        m["cost_per_llm_call"] = 0.0
    return m, obs_perf, snapshot_dir


if __name__ == "__main__":
    main()
