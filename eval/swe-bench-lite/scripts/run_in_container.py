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
  6. 分类:resolved / partial / failed / timeout / error,汇总指标。
"""
import hashlib
import json
import os
import subprocess
import sys
import time
import glob

WORKDIR = "/workspace/repo"
RESULTS_ROOT = "/eval/results"
TASKS_FILE = "/eval/data/tasks.json"
CONFIG = "/eval/config/lcoder.yaml"
PROMPT_TMPL = "/eval/prompts/swe_task.txt"

AGENT_TIMEOUT_S = int(os.environ.get("AGENT_TIMEOUT_S", "1500"))
INSTALL_TIMEOUT_S = int(os.environ.get("INSTALL_TIMEOUT_S", "1200"))
TEST_TIMEOUT_S = int(os.environ.get("TEST_TIMEOUT_S", "600"))
# PASS_TO_PASS 可能很多,MVP 限制数量以约束耗时(非静默截断:会在结果里记录)。
P2P_CAP = int(os.environ.get("P2P_CAP", "20"))
# 当 fail_to_pass 未通过时,把测试输出反馈给 agent 让它再试几轮。
FEEDBACK_ATTEMPTS = int(os.environ.get("FEEDBACK_ATTEMPTS", "2"))
FEEDBACK_TIMEOUT_S = int(os.environ.get("FEEDBACK_TIMEOUT_S", "600"))


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
        tasks = json.load(f)
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
    """把 FAIL_TO_PASS / PASS_TO_PASS 解析为 pytest 可识别的 node id。

    SWE-bench 不同仓库的存法不一:
    - requests 等存完整 node id(含 '::' 或路径分隔),原样可用。
    - sympy 等存裸函数名(如 'test_decompose'),pytest 无法直接定位,
      需用 test_patch 触及的(首个)测试文件做前缀:'<file>::<func>'。
    """
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
    """运行一组 pytest 节点,返回 (all_passed, returncode)。空集合视为通过。"""
    if not nodes:
        with open(outpath, "w") as f:
            f.write("(no tests in this group)\n")
        return True, 0
    cmd = ["python", "-m", "pytest", "-p", "no:cacheprovider", "--tb=short", "-q"] + nodes
    rc, out = run(cmd, cwd=WORKDIR, timeout=TEST_TIMEOUT_S, env=env)
    with open(outpath, "w", encoding="utf-8") as f:
        f.write(out)
    return rc == 0, rc


def project_session_dir(cwd=WORKDIR):
    """根据 cwd 计算 session store 的目录(lcder 用 sha256 前 16 位)。"""
    h = hashlib.sha256(cwd.encode("utf-8")).hexdigest()[:16]
    return os.path.join("/root/.lcoder/sessions", h)


def latest_session_path(cwd=WORKDIR):
    """返回最近修改的 session 文件路径。"""
    d = project_session_dir(cwd)
    try:
        files = [os.path.join(d, f) for f in os.listdir(d) if f.endswith(".jsonl")]
    except FileNotFoundError:
        return None
    if not files:
        return None
    return max(files, key=os.path.getmtime)


def extract_agent_patch(rdir):
    """提取当前工作树的改动到 rdir/patch.diff,返回 diff 文本。"""
    run(["git", "add", "-A"], cwd=WORKDIR, timeout=120)
    rc, diff = run(["git", "diff", "--cached", "HEAD"], cwd=WORKDIR, timeout=120)
    run(["git", "reset", "-q", "HEAD"], cwd=WORKDIR, timeout=120)
    path = os.path.join(rdir, "patch.diff")
    with open(path, "w", encoding="utf-8") as f:
        f.write(diff)
    return diff


def run_lcoder_agent(runtime_cfg, prompt, events_path, stderr_path, env, timeout,
                     continue_session=False, mode="a"):
    """运行 lcoder agent,将 JSON 事件写入/追加到 events_path。"""
    cmd = ["lcoder", "--config", runtime_cfg, "--unsafe", "--json", "-p", prompt]
    if continue_session:
        cmd.append("--continue")
    with open(events_path, mode, encoding="utf-8") as ev, \
         open(stderr_path, "a", encoding="utf-8") as er:
        p = subprocess.run(cmd, cwd=WORKDIR, env=env, stdout=ev, stderr=er, timeout=timeout)
    return p.returncode


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
        "stages": {},
        "p2p_evaluated": 0,
        "p2p_total": len(task["pass_to_pass"]),
        "p2p_capped": False,
    }
    t0 = time.time()
    env = dict(os.environ)
    env["HOME"] = "/root"

    test_patch_path = os.path.join(rdir, "test_patch.diff")
    with open(test_patch_path, "w", encoding="utf-8") as f:
        f.write(task["test_patch"])

    try:
        # 1) 取源码 + 造合成 base -------------------------------------------
        # 容器内 git 用 GnuTLS,对 github 的 git-smart-http 握手偶发中断;
        # 改用 codeload 的 tarball 单次 HTTPS GET(Python/OpenSSL 栈,更稳),
        # 解包后 git init 造一个合成 base 提交(评测只需干净 HEAD 可 diff,
        # 不依赖真实 sha)。
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

        # 2) install ----------------------------------------------------------
        rc, out = run(task["install_cmd"], cwd=WORKDIR, timeout=INSTALL_TIMEOUT_S, env=env)
        with open(os.path.join(rdir, "install.log"), "w", encoding="utf-8") as f:
            f.write(out)
        result["stages"]["install"] = "ok" if rc == 0 else "failed"
        if rc != 0:
            raise RuntimeError("install failed")

        # 裸函数名(sympy 等)解析为 '<test_file>::<func>',full node id 原样保留。
        f2p = resolve_nodes(task["fail_to_pass"], task["test_patch"])
        p2p_all = resolve_nodes(task["pass_to_pass"], task["test_patch"])
        p2p = p2p_all[:P2P_CAP]
        result["p2p_evaluated"] = len(p2p)
        result["p2p_capped"] = len(p2p) < len(p2p_all)

        # 3) baseline:应用 test_patch,确认 F2P 失败 / P2P 通过 ----------------
        rc, out = apply_patch(test_patch_path, cwd=WORKDIR, timeout=120)
        if rc != 0:
            result["stages"]["baseline_apply_test_patch"] = "failed"
            with open(os.path.join(rdir, "test_patch_apply.log"), "w") as f:
                f.write(out)
            raise RuntimeError("test_patch did not apply on base")
        result["stages"]["baseline_apply_test_patch"] = "ok"

        f2p_before, _ = run_pytest(f2p, os.path.join(rdir, "test_before.log"), env)
        p2p_before, _ = run_pytest(p2p, os.path.join(rdir, "test_before_p2p.log"), env)
        result["baseline"] = {
            "fail_to_pass_passed": f2p_before,  # 期望 False(修复前应失败)
            "pass_to_pass_passed": p2p_before,  # 期望 True
        }

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
        # lcoder 的 --config 路径不展开 {env:...},这里把令牌替换为实际值生成运行时配置。
        runtime_cfg = "/tmp/lcoder-runtime.yaml"
        with open(CONFIG, encoding="utf-8") as cf:
            cfg_text = cf.read()
        cfg_text = cfg_text.replace(
            "{env:ANTHROPIC_AUTH_TOKEN}", os.environ.get("ANTHROPIC_AUTH_TOKEN", "")
        )
        with open(runtime_cfg, "w", encoding="utf-8") as cf:
            cf.write(cfg_text)

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

        rc, timed_out, _ = agent_round(prompt, AGENT_TIMEOUT_S, False, "w", "agent")
        agent_timed_out = timed_out

        # 5) 评测:在 agent 改动之上应用 test_patch,未通过则反馈给 agent -----
        f2p_after = False
        p2p_after = False
        eval_status = "failed"
        test_patch_applied = False

        if agent_timed_out:
            eval_status = "timeout"
        else:
            rc, out = apply_patch(test_patch_path, cwd=WORKDIR, timeout=120)
            if rc != 0:
                with open(os.path.join(rdir, "eval_apply.log"), "w") as f:
                    f.write(out)
                result["stages"]["eval_apply_test_patch"] = "failed"
                eval_status = "error"
            else:
                result["stages"]["eval_apply_test_patch"] = "ok"
                test_patch_applied = True
                f2p_log = os.path.join(rdir, "test_after.log")
                f2p_after, _ = run_pytest(f2p, f2p_log, env)

                fb_attempt = 0
                while fb_attempt < FEEDBACK_ATTEMPTS:
                    # F2P 已通过时检查 P2P,防止回归未被发现。
                    if f2p_after:
                        p2p_log = (
                            os.path.join(rdir, "test_after_p2p.log")
                            if fb_attempt == 0
                            else os.path.join(rdir, f"test_after_fb_{fb_attempt}_p2p.log")
                        )
                        p2p_after, _ = run_pytest(p2p, p2p_log, env)
                        if p2p_after:
                            eval_status = "resolved"
                            break
                        feedback_kind = "regression"
                    else:
                        feedback_kind = "f2p"

                    # 无剩余反馈次数,直接给出当前结论。
                    if fb_attempt >= FEEDBACK_ATTEMPTS:
                        if feedback_kind == "regression":
                            eval_status = "partial"
                        else:
                            eval_status = "failed"
                        break

                    # 撤销 test_patch,保留 agent 改动,让 agent 继续。
                    run(["git", "apply", "-R", test_patch_path], cwd=WORKDIR, timeout=120)
                    test_patch_applied = False

                    if feedback_kind == "f2p":
                        with open(f2p_log, encoding="utf-8") as f:
                            test_output = f.read()
                        fb_prompt = build_feedback_prompt(f2p, test_output, fb_attempt)
                    else:
                        with open(f2p_log, encoding="utf-8") as f:
                            f2p_output = f.read()
                        with open(p2p_log, encoding="utf-8") as f:
                            p2p_output = f.read()
                        fb_prompt = build_feedback_prompt(
                            f2p, f2p_output, fb_attempt,
                            p2p_nodes=p2p, p2p_output=p2p_output,
                        )

                    stage_key = f"agent_feedback_{fb_attempt + 1}"
                    rc, timed_out, _ = agent_round(
                        fb_prompt, FEEDBACK_TIMEOUT_S, True, "a", stage_key,
                    )
                    if timed_out:
                        agent_timed_out = True
                        eval_status = "timeout"
                        break

                    rc, out = apply_patch(test_patch_path, cwd=WORKDIR, timeout=120)
                    if rc != 0:
                        with open(os.path.join(rdir, f"eval_apply_fb_{fb_attempt + 1}.log"), "w") as f:
                            f.write(out)
                        result["stages"][f"eval_apply_test_patch_fb_{fb_attempt + 1}"] = "failed"
                        eval_status = "error"
                        break
                    test_patch_applied = True

                    fb_attempt += 1
                    f2p_log = os.path.join(rdir, f"test_after_fb_{fb_attempt}.log")
                    f2p_after, _ = run_pytest(f2p, f2p_log, env)

                if agent_timed_out:
                    eval_status = "timeout"
                elif eval_status not in ("resolved", "partial", "error"):
                    # 循环结束但尚未得出结论:F2P 仍失败。
                    eval_status = "failed"

        result["fail_to_pass_passed"] = f2p_after
        result["pass_to_pass_passed"] = p2p_after
        result["status"] = eval_status
        result["agent_duration_s"] = round(agent_duration_s, 1)

        # 6) 提取 agent patch(不含 test_patch) -----------------------------
        if test_patch_applied:
            run(["git", "apply", "-R", test_patch_path], cwd=WORKDIR, timeout=120)
        run(["git", "add", "-A"], cwd=WORKDIR, timeout=120)
        rc, diff = run(["git", "diff", "--cached", "HEAD"], cwd=WORKDIR, timeout=120)
        with open(os.path.join(rdir, "patch.diff"), "w", encoding="utf-8") as f:
            f.write(diff)
        result["patch_nonempty"] = bool(diff.strip())
        run(["git", "reset", "-q", "HEAD"], cwd=WORKDIR, timeout=120)

        # 7) 指标 -------------------------------------------------------------
        result["metrics"] = collect_metrics(events_path, env)
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


def collect_metrics(events_path, env):
    """从 events.jsonl(权威) + 当前任务 session 的 observability 采集指标。"""
    m = {
        "turns": 0,
        "tool_calls": 0,
        "file_edits": 0,
        "tests_run": 0,
        "messages": 0,
        "errors": 0,
        "tokens": {"prompt": 0, "completion": 0, "cache_read": 0, "cache_write": 0},
        "cost": 0.0,
        "provider": "",
        "model": "",
    }
    try:
        with open(events_path, encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                try:
                    ev = json.loads(line)
                except json.JSONDecodeError:
                    continue
                t = ev.get("type")
                if t == "turn_start":
                    m["turns"] += 1
                elif t == "tool_execution_start":
                    m["tool_calls"] += 1
                    name = (ev.get("tool_name") or "").lower()
                    if name in ("edit", "write"):
                        m["file_edits"] += 1
                    if name == "bash":
                        cmd = str(ev.get("args", {}).get("command", ""))
                        if "pytest" in cmd or "test" in cmd:
                            m["tests_run"] += 1
                elif t == "message_end":
                    m["messages"] += 1
                elif t == "error":
                    m["errors"] += 1
    except FileNotFoundError:
        pass

    # 精确读取当前任务 session 对应的 observability 文件。
    metric_files = []
    session_path = latest_session_path(WORKDIR)
    if session_path:
        sid = os.path.splitext(os.path.basename(session_path))[0]
        scoped = os.path.join("/root/.lcoder/observability/sessions", f"{sid}.jsonl")
        if os.path.isfile(scoped):
            metric_files = [scoped]
    if not metric_files:
        # 兜底：session 未创建时仍尝试读取所有 observability 文件。
        metric_files = glob.glob("/root/.lcoder/observability/sessions/*.jsonl")

    token_map = {
        "llm_prompt_tokens": "prompt",
        "llm_completion_tokens": "completion",
        "llm_cache_read_tokens": "cache_read",
        "llm_cache_write_tokens": "cache_write",
    }
    for path in metric_files:
        try:
            with open(path, encoding="utf-8") as f:
                for line in f:
                    try:
                        rec = json.loads(line)
                    except json.JSONDecodeError:
                        continue
                    if rec.get("type") != "metric":
                        continue
                    metric = rec.get("metric", {})
                    name = metric.get("name", "")
                    value = metric.get("value")
                    if name in token_map and isinstance(value, (int, float)):
                        m["tokens"][token_map[name]] += int(value)
                    if name == "llm_cost_usd" and isinstance(value, (int, float)):
                        m["cost"] += float(value)
                    labels = metric.get("labels", {})
                    if not m["provider"] and labels.get("provider"):
                        m["provider"] = labels["provider"]
                    if not m["model"] and labels.get("model"):
                        m["model"] = labels["model"]
        except OSError:
            continue

    m["cost"] = round(m["cost"], 6)
    return m


if __name__ == "__main__":
    main()
