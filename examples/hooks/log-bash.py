#!/usr/bin/env python3
"""
after_tool_result hook — logs all bash commands to an audit file.

Reads JSON from stdin:
  {"hook_event":"after_tool_result","tool_name":"bash","tool_input":{...},"tool_result":"..."}

Exits:
  0 = allow (this hook never blocks)
"""
import json
import sys
from datetime import datetime, timezone

data = json.load(sys.stdin)
tool = data.get("tool_name", "")

if tool != "bash":
    sys.exit(0)

command = data.get("tool_input", {}).get("command", "")
result = data.get("tool_result", "")
is_error = data.get("is_error", False)

ts = datetime.now(timezone.utc).isoformat()
status = "ERROR" if is_error else "OK"

# Append to audit log in ~/.lcoder/hooks/
log_path = os.path.join(os.path.expanduser("~"), ".lcoder", "hooks", "bash-audit.log")
os.makedirs(os.path.dirname(log_path), exist_ok=True)

with open(log_path, "a") as f:
    f.write(f"[{ts}] {status} bash: {command}\n")
    if is_error:
        f.write(f"  result: {result[:200]}\n")

sys.exit(0)
