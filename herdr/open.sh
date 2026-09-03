#!/usr/bin/env bash
# The Vincent pane launcher, behind the `open` and `open-split` actions.
#
#   open.sh tab     Vincent in its own tab: open if absent, switch to its tab
#                   if it is elsewhere in this workspace, close it if it is the
#                   focused pane.
#   open.sh split   Vincent beside the current pane: open, focus, or close,
#                   scoped to the current tab.
#
# A Vincent pane is one in this workspace whose label is the manifest title
# "Vincent". Any failure to read pane state degrades to OPEN, so a broken
# `herdr pane list` costs a duplicate pane rather than a dead key.
#
# The pane opens in the directory of the focused pane, else the workspace's
# directory, else $HOME — that becomes Vincent's root. bash 3.2 compatible;
# JSON is read with python3 because macOS ships it and does not ship jq.
set -euo pipefail

mode="${1:-tab}"
herdr_bin="${HERDR_BIN_PATH:-herdr}"
plugin_id="chasereyn.vincent"

target_dir="$(python3 - <<'PY'
import json, os
d = ""
try:
    ctx = json.loads(os.environ.get("HERDR_PLUGIN_CONTEXT_JSON") or "")
    for key in ("focused_pane_cwd", "workspace_cwd"):
        v = ctx.get(key) if isinstance(ctx, dict) else None
        if isinstance(v, str) and v:
            d = v
            break
except Exception:
    pass
print(d or os.environ.get("HOME") or os.getcwd())
PY
)"

panes_json="$("$herdr_bin" pane list 2>/dev/null || true)"
current_json="$("$herdr_bin" pane current 2>/dev/null || true)"

decision="$(HERDR_PANES_JSON="$panes_json" HERDR_CURRENT_JSON="$current_json" VINCENT_MODE="$mode" python3 - <<'PY' || echo OPEN
import json, os, re, sys

SAFE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9:_-]*$")
mode = os.environ.get("VINCENT_MODE") or "tab"
try:
    panes = json.loads(os.environ.get("HERDR_PANES_JSON") or "")["result"]["panes"]
    cur = json.loads(os.environ.get("HERDR_CURRENT_JSON") or "")["result"]["pane"]
except Exception:
    print("OPEN"); sys.exit(0)

def ours(p):
    if not isinstance(p, dict) or p.get("label") != "Vincent":
        return False
    if not SAFE.match(p.get("pane_id") or ""):
        return False
    if p.get("workspace_id") != cur.get("workspace_id"):
        return False
    # A split is scoped to the current tab; a tab is scoped to the workspace.
    return mode != "split" or p.get("tab_id") == cur.get("tab_id")

matches = [p for p in panes if ours(p)]
for p in matches:
    if p.get("focused"):
        print("CLOSE " + p["pane_id"]); sys.exit(0)
if matches:
    print("FOCUS " + matches[0]["pane_id"]); sys.exit(0)
print("OPEN")
PY
)"

case "$decision" in
  "FOCUS "*) exec "$herdr_bin" plugin pane focus "${decision#FOCUS }" ;;
  "CLOSE "*) exec "$herdr_bin" plugin pane close "${decision#CLOSE }" ;;
esac

if [ "$mode" = "split" ]; then
  exec "$herdr_bin" plugin pane open --plugin "$plugin_id" --entrypoint vincent \
    --placement split --direction right --cwd "$target_dir" --focus
fi
exec "$herdr_bin" plugin pane open --plugin "$plugin_id" --entrypoint vincent \
  --placement tab --cwd "$target_dir" --focus
