#!/usr/bin/env python3
"""SWE-bench Lite 评估指标收集 helpers。

这些函数在容器内运行,从 events.jsonl、observability JSONL 与 patch.diff 中
提取可观测指标,供 run_in_container.py 与 runner/run.py 汇总使用。
"""
import json
import os
import re


def collect_event_metrics(events_path):
    """解析 lcoder JSONL 事件流,返回各类动作计数。"""
    m = {
        "turns": 0,
        "agent_rounds": 0,
        "tool_calls": 0,
        "messages": 0,
        "errors": 0,
        "file_edits": 0,
        "file_writes": 0,
        "file_reads": 0,
        "bash_commands": 0,
        "test_commands": 0,
        "grep_searches": 0,
        "find_searches": 0,
        "code_index_lookups": 0,
        "compactions": 0,
        "tool_counts": {},
        "max_consecutive_errors": 0,
        "last_tool_before_error": "",
    }
    consecutive_errors = 0
    last_tool = ""

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
                    if m["turns"] == 1 or ev.get("role") == "user":
                        # 每个新的 user/assistant 轮次视为一次 agent_round;
                        # 反馈轮次也会以 user 消息触发 turn_start。
                        m["agent_rounds"] += 1
                elif t == "tool_execution_start":
                    m["tool_calls"] += 1
                    name = (ev.get("tool_name") or "").lower()
                    m["tool_counts"][name] = m["tool_counts"].get(name, 0) + 1
                    last_tool = name
                    if name in ("edit", "multi_edit"):
                        m["file_edits"] += 1
                    elif name in ("write",):
                        m["file_writes"] += 1
                    elif name in ("read", "view"):
                        m["file_reads"] += 1
                    elif name == "bash":
                        m["bash_commands"] += 1
                        cmd = str(ev.get("args", {}).get("command", ""))
                        if "pytest" in cmd or "test" in cmd:
                            m["test_commands"] += 1
                    elif name in ("grep",):
                        m["grep_searches"] += 1
                    elif name in ("find",):
                        m["find_searches"] += 1
                    elif name in ("repo_index", "code_index"):
                        m["code_index_lookups"] += 1
                elif t == "message_end":
                    m["messages"] += 1
                elif t == "error":
                    m["errors"] += 1
                    consecutive_errors += 1
                    if consecutive_errors > m["max_consecutive_errors"]:
                        m["max_consecutive_errors"] = consecutive_errors
                        m["last_tool_before_error"] = last_tool
                elif t == "compaction_committed":
                    m["compactions"] += 1
                else:
                    # 非 error 事件重置连续错误计数。
                    if t not in ("tool_execution_start",):
                        consecutive_errors = 0
    except FileNotFoundError:
        pass

    return m


def collect_tool_chain(events_path):
    """按时间顺序返回工具调用链路,每项包含 turn、tool_name、args。"""
    chain = []
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
                if ev.get("type") == "tool_execution_start":
                    chain.append({
                        "turn": ev.get("turn", 0),
                        "tool_name": ev.get("tool_name", ""),
                        "args": ev.get("args", {}),
                    })
    except FileNotFoundError:
        pass
    return chain


def patch_stats(patch_text):
    """统计 patch.diff 的改动规模。"""
    stats = {
        "files_changed": 0,
        "insertions": 0,
        "deletions": 0,
        "net_lines": 0,
        "hunks": 0,
        "non_empty": bool(patch_text and patch_text.strip()),
    }
    if not patch_text:
        return stats

    files = set()
    in_hunk = False
    for line in patch_text.splitlines():
        if line.startswith("diff --git "):
            # "diff --git a/... b/..."
            parts = line.split()
            if len(parts) >= 4:
                files.add(parts[-1][2:] if parts[-1].startswith("b/") else parts[-1])
            in_hunk = False
        elif line.startswith("@@"):
            stats["hunks"] += 1
            in_hunk = True
        elif in_hunk:
            if line.startswith("+") and not line.startswith("+++"):
                stats["insertions"] += 1
            elif line.startswith("-") and not line.startswith("---"):
                stats["deletions"] += 1
    stats["files_changed"] = len(files)
    stats["net_lines"] = stats["insertions"] - stats["deletions"]
    return stats


def parse_test_summary(log_text):
    """从 pytest 简短输出中粗略解析通过/失败数。"""
    summary = {"passed": 0, "failed": 0, "error": 0, "skipped": 0, "total": 0}
    if not log_text:
        return summary
    # pytest -q 尾部常见形如: "1 passed, 2 failed in 0.03s"
    m = re.search(r"(\d+)\s+passed", log_text, re.IGNORECASE)
    if m:
        summary["passed"] = int(m.group(1))
    m = re.search(r"(\d+)\s+failed", log_text, re.IGNORECASE)
    if m:
        summary["failed"] = int(m.group(1))
    m = re.search(r"(\d+)\s+error", log_text, re.IGNORECASE)
    if m:
        summary["error"] = int(m.group(1))
    m = re.search(r"(\d+)\s+skipped", log_text, re.IGNORECASE)
    if m:
        summary["skipped"] = int(m.group(1))
    summary["total"] = summary["passed"] + summary["failed"] + summary["error"] + summary["skipped"]
    return summary


def latest_session_path(cwd):
    """返回最近修改的 lcoder session 文件路径。"""
    import hashlib

    h = hashlib.sha256(cwd.encode("utf-8")).hexdigest()[:16]
    d = os.path.join("/root/.lcoder/sessions", h)
    try:
        files = [os.path.join(d, f) for f in os.listdir(d) if f.endswith(".jsonl")]
    except FileNotFoundError:
        return None
    if not files:
        return None
    return max(files, key=os.path.getmtime)


def collect_observability_tokens(cwd, observability_path=None):
    """从 observability session 文件汇总 token / cost / provider / model。

    如果提供了 observability_path,优先读取该文件;否则按 session 哈希或全局 glob 查找。
    """
    import glob

    m = {
        "tokens": {"prompt": 0, "completion": 0, "cache_read": 0, "cache_write": 0},
        "cost": 0.0,
        "provider": "",
        "model": "",
        "llm_calls": 0,
    }
    token_map = {
        "llm_prompt_tokens": "prompt",
        "llm_completion_tokens": "completion",
        "llm_cache_read_tokens": "cache_read",
        "llm_cache_write_tokens": "cache_write",
    }

    metric_files = []
    if observability_path and os.path.isfile(observability_path):
        metric_files = [observability_path]
    else:
        session_path = latest_session_path(cwd)
        if session_path:
            sid = os.path.splitext(os.path.basename(session_path))[0]
            scoped = os.path.join("/root/.lcoder/observability/sessions", f"{sid}.jsonl")
            if os.path.isfile(scoped):
                metric_files = [scoped]
        if not metric_files:
            metric_files = glob.glob("/root/.lcoder/observability/sessions/*.jsonl")

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
                    # 兼容扁平输出与旧的嵌套 metric 对象。
                    metric = rec.get("metric") or {}
                    if metric:
                        name = metric.get("name", "")
                        value = metric.get("value")
                        labels = metric.get("labels", {})
                    else:
                        name = rec.get("name", "")
                        value = rec.get("value")
                        labels = rec.get("labels", {})
                    if name in token_map and isinstance(value, (int, float)):
                        m["tokens"][token_map[name]] += int(value)
                    if name == "llm_cost_usd" and isinstance(value, (int, float)):
                        m["cost"] += float(value)
                    if name == "llm_calls" and isinstance(value, (int, float)):
                        m["llm_calls"] += int(value)
                    if not m["provider"] and labels.get("provider"):
                        m["provider"] = labels["provider"]
                    if not m["model"] and labels.get("model"):
                        m["model"] = labels["model"]
        except OSError:
            continue

    m["cost"] = round(m["cost"], 6)
    return m


def collect_observability_perf(observability_path):
    """解析 observability JSONL,返回核心模块性能指标。"""
    perf = {
        "agent_duration_ms": 0,
        "turn_durations_ms": [],
        "llm_durations_ms": [],
        "tool_durations_ms": [],
        "tool_durations_by_name": {},
        "ttft_ms": [],
        "cache": {"prompt": 0, "completion": 0, "cache_read": 0, "cache_write": 0},
        "cache_hit_rate": 0.0,
        "llm_calls": 0,
        "tool_calls": 0,
        "tool_errors": 0,
        "compactions": 0,
    }
    if not observability_path or not os.path.isfile(observability_path):
        return perf

    token_map = {
        "llm_prompt_tokens": "prompt",
        "llm_completion_tokens": "completion",
        "llm_cache_read_tokens": "cache_read",
        "llm_cache_write_tokens": "cache_write",
    }

    try:
        with open(observability_path, encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                try:
                    rec = json.loads(line)
                except json.JSONDecodeError:
                    continue
                typ = rec.get("type")
                if typ in ("span_start", "span_end"):
                    # 兼容扁平输出与旧的嵌套 span 对象。
                    span = rec.get("span") or {}
                    if span:
                        name = span.get("name", "")
                        dur = span.get("duration_ms", 0)
                        status = span.get("status", "")
                    else:
                        name = rec.get("name", "")
                        dur = rec.get("duration_ms", 0)
                        status = rec.get("status", "")
                    if name == "agent_run" and typ == "span_end":
                        perf["agent_duration_ms"] = dur
                    elif name.startswith("turn_") and typ == "span_end":
                        perf["turn_durations_ms"].append(dur)
                    elif name == "llm_response" and typ == "span_end":
                        perf["llm_durations_ms"].append(dur)
                        perf["llm_calls"] += 1
                    elif name.startswith("tool_") and typ == "span_end":
                        perf["tool_durations_ms"].append(dur)
                        perf["tool_calls"] += 1
                        tool_name = name[len("tool_"):]
                        perf["tool_durations_by_name"].setdefault(tool_name, []).append(dur)
                        if status == "error":
                            perf["tool_errors"] += 1
                elif typ == "metric":
                    metric = rec.get("metric") or {}
                    if metric:
                        name = metric.get("name", "")
                        value = metric.get("value", 0)
                    else:
                        name = rec.get("name", "")
                        value = rec.get("value", 0)
                    if name in token_map:
                        perf["cache"][token_map[name]] += int(value)
                    elif name == "llm_ttft_ms":
                        perf["ttft_ms"].append(float(value))
    except OSError:
        pass

    prompt = perf["cache"]["prompt"]
    cache_read = perf["cache"]["cache_read"]
    denom = prompt + cache_read
    if denom > 0:
        perf["cache_hit_rate"] = round(cache_read / denom, 4)
    return perf


def avg(vals, default=0.0):
    """计算数值列表的平均值。"""
    if not vals:
        return default
    return round(sum(vals) / len(vals), 2)


def summarize_observability_perf(perf):
    """把 observability_perf 原始列表聚合成可读摘要。"""
    return {
        "agent_duration_ms": perf.get("agent_duration_ms", 0),
        "avg_turn_duration_ms": avg(perf.get("turn_durations_ms", [])),
        "max_turn_duration_ms": max(perf.get("turn_durations_ms", []) or [0]),
        "avg_llm_duration_ms": avg(perf.get("llm_durations_ms", [])),
        "max_llm_duration_ms": max(perf.get("llm_durations_ms", []) or [0]),
        "avg_ttft_ms": avg(perf.get("ttft_ms", [])),
        "max_ttft_ms": max(perf.get("ttft_ms", []) or [0]),
        "avg_tool_duration_ms": avg(perf.get("tool_durations_ms", [])),
        "max_tool_duration_ms": max(perf.get("tool_durations_ms", []) or [0]),
        "tool_calls": perf.get("tool_calls", 0),
        "tool_errors": perf.get("tool_errors", 0),
        "llm_calls": perf.get("llm_calls", 0),
        "cache_hit_rate": perf.get("cache_hit_rate", 0.0),
        "cache": perf.get("cache", {}),
    }


def list_context_snapshots(snapshot_dir):
    """返回上下文快照目录下的 Markdown 文件列表(相对路径)。"""
    if not snapshot_dir or not os.path.isdir(snapshot_dir):
        return []
    try:
        return sorted(f for f in os.listdir(snapshot_dir) if f.endswith(".md"))
    except OSError:
        return []
