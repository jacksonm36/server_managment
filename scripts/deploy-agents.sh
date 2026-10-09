#!/bin/sh
# Push the freshly built agent into the dev Incus containers and restart it.
# Usage: scripts/deploy-agents.sh [container...]   (default: sm-node1 sm-node2)
set -eu
cd "$(dirname "$0")/.."
BIN=bin/servermgr-agent-linux-amd64
[ -f "$BIN" ] || { echo "build the agent first: make agent" >&2; exit 1; }
[ $# -gt 0 ] || set -- sm-node1 sm-node2
for c in "$@"; do
  sudo incus file push "$BIN" "$c/usr/local/bin/servermgr-agent.new" --mode 0755 >/dev/null
  sudo incus exec "$c" -- sh -c 'mv /usr/local/bin/servermgr-agent.new /usr/local/bin/servermgr-agent && systemctl restart servermgr-agent'
  echo "$c: $(sudo incus exec "$c" -- servermgr-agent version)"
done
