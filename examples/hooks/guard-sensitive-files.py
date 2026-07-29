#!/usr/bin/env python3
"""
before_tool_call hook — blocks write/edit to sensitive files.

Reads JSON from stdin:
  {"hook_event":"before_tool_call","tool_name":"write","tool_input":{"path":"..."}}

Exits:
  0 = allow
  2 = block (stderr is the reason)
"""
import json
import sys
import os

SENSITIVE = (".env", "id_rsa", "id_ed25519", "id_ecdsa", "id_dsa", "credentials")

data = json.load(sys.stdin)
tool = data.get("tool_name", "")
path = data.get("tool_input", {}).get("path", "")

if tool in ("write", "edit") and path:
    basename = os.path.basename(path).lower()
    for s in SENSITIVE:
        if basename == s or basename.startswith(s + ".") or basename.startswith(s + "-") or basename.startswith(s + "_"):
            sys.stderr.write(f"blocked: {basename} matches sensitive pattern '{s}'")
            sys.exit(2)

sys.exit(0)
