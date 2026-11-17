#!/usr/bin/env bash
# macOS/Linux twin of setup-chat-files-mcp.ps1: create the .venv for
# examples/mcp-servers/chat-files-mcp, install the MCP SDK, print the config.
#   scripts/setup-chat-files-mcp.sh [--test] [--zotero-auth]
set -euo pipefail

mcp_dir="$(cd "$(dirname "$0")/.." && pwd)/examples/mcp-servers/chat-files-mcp"
py="$mcp_dir/.venv/bin/python"

if [[ ! -x "$py" ]]; then
  echo "==> creating .venv"
  if command -v uv >/dev/null; then uv venv "$mcp_dir/.venv"; else python3 -m venv "$mcp_dir/.venv"; fi
fi
if ! "$py" -c 'import mcp' 2>/dev/null; then
  echo "==> installing requirements"
  if command -v uv >/dev/null; then
    uv pip install --python "$py" -r "$mcp_dir/requirements.txt"
  else
    "$py" -m pip install -q -r "$mcp_dir/requirements.txt"
  fi
fi
echo "[OK] chat-files-mcp ready ($py)"

for arg in "$@"; do
  case "$arg" in
    --test) (cd "$mcp_dir" && "$py" -m unittest discover -s tests) ;;
    --zotero-auth)
      echo "==> click Allow (or Always Allow) in the Zotero window"
      (cd "$mcp_dir" && "$py" -m chat_files zotero-auth) ;;
    *) echo "unknown option $arg" >&2; exit 2 ;;
  esac
done

echo
echo "Multica: Settings -> MCP -> add server, then assign it on the agent's MCP tab:"
printf '  {"command":"%s","args":["%s"]}\n' "$py" "$mcp_dir/server.py"
