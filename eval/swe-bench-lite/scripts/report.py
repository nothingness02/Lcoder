#!/usr/bin/env python3
"""汇总 SWE-bench Lite 评测结果,生成 report.md 与 report.html。

读取 eval/swe-bench-lite/results/<instance_id>/result.json,
聚合 SWE-bench 初解/反馈后成功率、工具链路、token/cost、缓存命中率、
核心模块性能、上下文快照等指标,输出到结果根目录。
"""
import argparse
import html
import json
import os
import sys
from datetime import datetime, timezone


def load_results(results_dir):
    """加载 results_dir 下所有 result.json。"""
    rows = []
    if not os.path.isdir(results_dir):
        return rows
    for iid in sorted(os.listdir(results_dir)):
        rp = os.path.join(results_dir, iid, "result.json")
        if not os.path.isfile(rp):
            continue
        try:
            with open(rp, encoding="utf-8") as f:
                rows.append(json.load(f))
        except Exception as e:  # noqa: BLE001
            print(f"[report] skip {rp}: {e}", file=sys.stderr, flush=True)
    return rows


def avg(vals, default=0.0):
    if not vals:
        return default
    return round(sum(vals) / len(vals), 2)


def pct(n, total):
    if total == 0:
        return 0.0
    return round(n / total * 100, 2)


def summarize(rows):
    total = len(rows)
    initial_resolved = sum(1 for r in rows if r.get("initial_status") == "resolved")
    initial_partial = sum(1 for r in rows if r.get("initial_status") == "partial")
    initial_failed = sum(1 for r in rows if r.get("initial_status") == "failed")
    final_resolved = sum(1 for r in rows if r.get("final_status") == "resolved")
    final_partial = sum(1 for r in rows if r.get("final_status") == "partial")
    final_failed = sum(1 for r in rows if r.get("final_status") == "failed")
    timeouts = sum(1 for r in rows if r.get("final_status") == "timeout")
    errors = sum(1 for r in rows if r.get("final_status") == "error")
    feedback_improved = sum(
        1 for r in rows
        if r.get("initial_status") != "resolved" and r.get("final_status") == "resolved"
    )

    # 分协议分数:official(一发无反馈,可横向对比)与 extended(反馈+P2P 截断)分开统计。
    protocol_stats = {}
    for proto in ("official", "extended"):
        sub = [r for r in rows if r.get("protocol", "extended") == proto]
        if sub:
            resolved = sum(1 for r in sub if r.get("final_status") == "resolved")
            protocol_stats[proto] = {
                "total": len(sub),
                "resolved": resolved,
                "resolved_rate": round(100.0 * resolved / len(sub), 2),
            }

    def mget(key, subkey=None):
        vals = []
        for r in rows:
            v = r.get("metrics", {})
            if subkey:
                v = v.get(key, {})
                v = v.get(subkey, 0)
            else:
                v = v.get(key, 0)
            if isinstance(v, (int, float)):
                vals.append(v)
        return vals

    perf = [r.get("metrics", {}).get("observability_perf", {}) for r in rows]
    return {
        "total": total,
        "initial": {
            "resolved": initial_resolved,
            "partial": initial_partial,
            "failed": initial_failed,
            "resolved_rate": pct(initial_resolved, total),
        },
        "final": {
            "resolved": final_resolved,
            "partial": final_partial,
            "failed": final_failed,
            "resolved_rate": pct(final_resolved, total),
        },
        "timeouts": timeouts,
        "errors": errors,
        "feedback_improved": feedback_improved,
        "protocols": protocol_stats,
        "avg": {
            "turns": avg(mget("turns")),
            "agent_rounds": avg(mget("agent_rounds")),
            "tool_calls": avg(mget("tool_calls")),
            "file_edits": avg(mget("file_edits")),
            "total_tokens": avg(mget("total_tokens")),
            "cost": avg(mget("cost")),
            "duration_s": avg([r.get("duration_s", 0) for r in rows]),
            "agent_duration_s": avg([r.get("agent_duration_s", 0) for r in rows]),
            "cache_hit_rate": avg(mget("cache_hit_rate")),
            "errors": avg(mget("errors")),
            "compactions": avg(mget("compactions")),
            "feedback_attempts_used": avg([r.get("feedback_attempts_used", 0) for r in rows]),
        },
        "perf": {
            "avg_turn_duration_ms": avg([p.get("avg_turn_duration_ms", 0) for p in perf]),
            "max_turn_duration_ms": max([p.get("max_turn_duration_ms", 0) for p in perf] or [0]),
            "avg_llm_duration_ms": avg([p.get("avg_llm_duration_ms", 0) for p in perf]),
            "max_llm_duration_ms": max([p.get("max_llm_duration_ms", 0) for p in perf] or [0]),
            "avg_ttft_ms": avg([p.get("avg_ttft_ms", 0) for p in perf]),
            "max_ttft_ms": max([p.get("max_ttft_ms", 0) for p in perf] or [0]),
            "avg_tool_duration_ms": avg([p.get("avg_tool_duration_ms", 0) for p in perf]),
            "max_tool_duration_ms": max([p.get("max_tool_duration_ms", 0) for p in perf] or [0]),
            "total_llm_calls": sum(p.get("llm_calls", 0) for p in perf),
            "total_tool_calls": sum(p.get("tool_calls", 0) for p in perf),
            "total_tool_errors": sum(p.get("tool_errors", 0) for p in perf),
        },
    }


def aggregate_tool_counts(rows):
    total = {}
    per_task = []
    for r in rows:
        counts = r.get("metrics", {}).get("tool_counts", {})
        per_task.append({"instance_id": r.get("instance_id"), "counts": counts})
        for name, n in counts.items():
            total[name] = total.get(name, 0) + n
    return total, per_task


def tool_chain_text(r):
    chain = r.get("metrics", {}).get("tool_chain", [])
    if not chain:
        return "(no tool chain recorded)"
    parts = []
    cur_turn = None
    for item in chain:
        if item.get("turn") != cur_turn:
            cur_turn = item.get("turn")
            parts.append(f"\n[turn {cur_turn}]")
        parts.append(item.get("tool_name", "?"))
    return " ".join(parts).strip()


def render_md(rows, summary, total_tools, per_task_tools, results_dir):
    lines = [
        "# Agent 评测指标汇总报告",
        "",
        f"- 生成时间: {datetime.now(timezone.utc).isoformat()}",
        f"- 结果目录: `{results_dir}`",
        f"- 任务总数: {summary['total']}",
        "",
        "## SWE-bench 成功率",
        "",
        "| 阶段 | resolved | partial | failed | resolved_rate |",
        "|------|----------|---------|--------|---------------|",
        f"| 初次 (initial) | {summary['initial']['resolved']} | {summary['initial']['partial']} | "
        f"{summary['initial']['failed']} | {summary['initial']['resolved_rate']}% |",
        f"| 反馈后 (final) | {summary['final']['resolved']} | {summary['final']['partial']} | "
        f"{summary['final']['failed']} | {summary['final']['resolved_rate']}% |",
        "",
        f"- 反馈提升数: {summary['feedback_improved']} (初次未解、反馈后解决)",
        f"- 协议分数: official {summary['protocols'].get('official', {}).get('resolved', 0)}/"
        f"{summary['protocols'].get('official', {}).get('total', 0)} "
        f"({summary['protocols'].get('official', {}).get('resolved_rate', 0)}%) · "
        f"extended {summary['protocols'].get('extended', {}).get('resolved', 0)}/"
        f"{summary['protocols'].get('extended', {}).get('total', 0)} "
        f"({summary['protocols'].get('extended', {}).get('resolved_rate', 0)}%)",
        f"- timeout: {summary['timeouts']}, error: {summary['errors']}",
        "",
        "## 平均指标",
        "",
        "| 指标 | 平均值 |",
        "|------|--------|",
    ]
    for k, v in summary["avg"].items():
        lines.append(f"| {k} | {v} |")
    lines.append("")

    lines.append("## 核心模块性能")
    lines.append("")
    lines.append("| 指标 | 平均值 | 最大值 |")
    lines.append("|------|--------|--------|")
    p = summary["perf"]
    lines.append(f"| turn_duration_ms | {p['avg_turn_duration_ms']} | {p['max_turn_duration_ms']} |")
    lines.append(f"| llm_duration_ms | {p['avg_llm_duration_ms']} | {p['max_llm_duration_ms']} |")
    lines.append(f"| ttft_ms | {p['avg_ttft_ms']} | {p['max_ttft_ms']} |")
    lines.append(f"| tool_duration_ms | {p['avg_tool_duration_ms']} | {p['max_tool_duration_ms']} |")
    lines.append(f"| total_llm_calls | {p['total_llm_calls']} | - |")
    lines.append(f"| total_tool_calls | {p['total_tool_calls']} | - |")
    lines.append(f"| total_tool_errors | {p['total_tool_errors']} | - |")
    lines.append("")

    lines.append("## 工具调用汇总")
    lines.append("")
    lines.append("| 工具 | 总次数 |")
    lines.append("|------|--------|")
    for name, n in sorted(total_tools.items(), key=lambda x: -x[1]):
        lines.append(f"| {name} | {n} |")
    lines.append("")

    lines.append("## 任务明细")
    lines.append("")
    lines.append("| instance_id | repo | protocol | initial | final | feedback | turns | tools | tokens | cost | cache_hit | dur(s) | model |")
    lines.append("|-------------|------|----------|---------|-------|----------|-------|-------|--------|------|-----------|--------|-------|")
    for r in rows:
        m = r.get("metrics", {})
        model = r.get("model", "")
        lines.append(
            f"| {r.get('instance_id', '')} | {r.get('repo', '')} | {r.get('protocol', 'extended')} | "
            f"{r.get('initial_status', '')} | {r.get('final_status', '')} | "
            f"{r.get('feedback_attempts_used', 0)} | {m.get('turns', 0)} | "
            f"{m.get('tool_calls', 0)} | {m.get('total_tokens', 0)} | "
            f"{m.get('cost', 0)} | {m.get('cache_hit_rate', 0)} | "
            f"{r.get('duration_s', 0)} | {model} |"
        )
    lines.append("")

    lines.append("## 工具链路 (按任务)")
    lines.append("")
    for r in rows:
        iid = r.get("instance_id", "unknown")
        lines.append(f"### {iid}")
        lines.append("")
        lines.append("```")
        lines.append(tool_chain_text(r))
        lines.append("```")
        lines.append("")

    lines.append("## 上下文快照")
    lines.append("")
    for r in rows:
        iid = r.get("instance_id", "unknown")
        snaps = r.get("context_snapshots", [])
        if not snaps:
            lines.append(f"- {iid}: (none)")
            continue
        lines.append(f"- {iid}:")
        for s in snaps:
            lines.append(f"  - `{s}`")
    lines.append("")

    return "\n".join(lines)


def render_html(rows, summary, total_tools, per_task_tools, results_dir):
    title = "Agent Evaluation Metrics Report"
    generated = datetime.now(timezone.utc).isoformat()

    def tr_cells(cells):
        return "<tr>" + "".join(f"<td>{c}</td>" for c in cells) + "</tr>\n"

    def th_cells(cells):
        return "<tr>" + "".join(f"<th>{c}</th>" for c in cells) + "</tr>\n"

    avg_rows = "\n".join(tr_cells([k, str(v)]) for k, v in summary["avg"].items())
    perf_rows = "\n".join([
        tr_cells(["turn_duration_ms", summary["perf"]["avg_turn_duration_ms"], summary["perf"]["max_turn_duration_ms"]]),
        tr_cells(["llm_duration_ms", summary["perf"]["avg_llm_duration_ms"], summary["perf"]["max_llm_duration_ms"]]),
        tr_cells(["ttft_ms", summary["perf"]["avg_ttft_ms"], summary["perf"]["max_ttft_ms"]]),
        tr_cells(["tool_duration_ms", summary["perf"]["avg_tool_duration_ms"], summary["perf"]["max_tool_duration_ms"]]),
        tr_cells(["total_llm_calls", summary["perf"]["total_llm_calls"], "-"]),
        tr_cells(["total_tool_calls", summary["perf"]["total_tool_calls"], "-"]),
        tr_cells(["total_tool_errors", summary["perf"]["total_tool_errors"], "-"]),
    ])
    tool_rows = "\n".join(tr_cells([html.escape(name), n]) for name, n in sorted(total_tools.items(), key=lambda x: -x[1]))
    task_rows = "\n".join(
        tr_cells([
            html.escape(r.get("instance_id", "")),
            html.escape(r.get("repo", "")),
            r.get("initial_status", ""),
            r.get("final_status", ""),
            str(r.get("feedback_attempts_used", 0)),
            str(r.get("metrics", {}).get("turns", 0)),
            str(r.get("metrics", {}).get("tool_calls", 0)),
            str(r.get("metrics", {}).get("total_tokens", 0)),
            str(r.get("metrics", {}).get("cost", 0)),
            str(r.get("metrics", {}).get("cache_hit_rate", 0)),
            str(r.get("duration_s", 0)),
            html.escape(r.get("model", "")),
        ])
        for r in rows
    )

    chain_sections = []
    for r in rows:
        iid = html.escape(r.get("instance_id", "unknown"))
        chain_html = "<pre>" + html.escape(tool_chain_text(r)) + "</pre>"
        chain_sections.append(f"<h4>{iid}</h4>\n{chain_html}\n")

    snapshot_sections = []
    for r in rows:
        iid = html.escape(r.get("instance_id", "unknown"))
        snaps = r.get("context_snapshots", [])
        if not snaps:
            snapshot_sections.append(f"<p><strong>{iid}</strong>: (none)</p>")
        else:
            items = "\n".join(f"<li>{html.escape(s)}</li>" for s in snaps)
            snapshot_sections.append(f"<p><strong>{iid}</strong></p>\n<ul>\n{items}\n</ul>")

    body = f"""<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>{title}</title>
<style>
body {{ font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; max-width: 1100px; margin: 2em auto; line-height: 1.6; }}
h1, h2, h3, h4 {{ color: #222; }}
table {{ border-collapse: collapse; width: 100%; margin: 1em 0; }}
th, td {{ border: 1px solid #ddd; padding: 6px 10px; text-align: left; }}
th {{ background: #f4f4f4; }}
.metric {{ font-size: 1.3em; font-weight: bold; color: #0066cc; }}
pre {{ background: #f8f8f8; padding: 10px; overflow-x: auto; border: 1px solid #eee; }}
</style>
</head>
<body>
<h1>{title}</h1>
<p>Generated: {generated}</p>
<p>Results directory: <code>{html.escape(results_dir)}</code></p>
<p>Total tasks: <span class="metric">{summary['total']}</span></p>

<h2>SWE-bench Success Rate</h2>
<table>
{th_cells(["Stage", "resolved", "partial", "failed", "resolved_rate"])}
{tr_cells(["initial", summary['initial']['resolved'], summary['initial']['partial'], summary['initial']['failed'], f"{summary['initial']['resolved_rate']}%"])}
{tr_cells(["final", summary['final']['resolved'], summary['final']['partial'], summary['final']['failed'], f"{summary['final']['resolved_rate']}%"])}
</table>
<p>Feedback improved: {summary['feedback_improved']} | timeout: {summary['timeouts']} | error: {summary['errors']}</p>

<h2>Average Metrics</h2>
<table>
{th_cells(["Metric", "Value"])}
{avg_rows}
</table>

<h2>Core Module Performance</h2>
<table>
{th_cells(["Metric", "Avg", "Max"])}
{perf_rows}
</table>

<h2>Tool Usage Summary</h2>
<table>
{th_cells(["Tool", "Total Calls"])}
{tool_rows}
</table>

<h2>Task Details</h2>
<table>
{th_cells(["instance_id", "repo", "initial", "final", "feedback", "turns", "tools", "tokens", "cost", "cache_hit", "dur(s)", "model"])}
{task_rows}
</table>

<h2>Tool Chains</h2>
{''.join(chain_sections)}

<h2>Context Snapshots</h2>
{''.join(snapshot_sections)}
</body>
</html>"""
    return body


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--results-dir", default="/eval/results",
                    help="评测结果根目录")
    ap.add_argument("--out-prefix", default="report",
                    help="输出文件名前缀(不含扩展名)")
    args = ap.parse_args()

    rows = load_results(args.results_dir)
    if not rows:
        print(f"[report] no results found in {args.results_dir}", file=sys.stderr, flush=True)
        sys.exit(1)

    summary = summarize(rows)
    total_tools, per_task_tools = aggregate_tool_counts(rows)

    md_path = os.path.join(args.results_dir, f"{args.out_prefix}.md")
    html_path = os.path.join(args.results_dir, f"{args.out_prefix}.html")

    with open(md_path, "w", encoding="utf-8") as f:
        f.write(render_md(rows, summary, total_tools, per_task_tools, args.results_dir))
    with open(html_path, "w", encoding="utf-8") as f:
        f.write(render_html(rows, summary, total_tools, per_task_tools, args.results_dir))

    print(f"[report] wrote {md_path} and {html_path}", flush=True)
    print(json.dumps(summary, indent=2, ensure_ascii=False))


if __name__ == "__main__":
    main()
