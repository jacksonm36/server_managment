#!/bin/sh
# Builds servermgr from this checkout and runs it as the systemd service
# servermgr-panel. Run it as your normal user (it uses sudo for systemd).
#
#   scripts/deploy-panel.sh                 build and (re)start
#   scripts/deploy-panel.sh --pull          git pull first
#   scripts/deploy-panel.sh --host 1.2.3.4  address agents use (first run only)
#
# First run: creates the database and .env (scripts/setup-db.sh), installs
# and enables the service. Later runs: rebuild and restart. An existing
# service unit is never overwritten.
set -eu
cd "$(dirname "$0")/.."
ROOT=$(pwd)
UNIT=/etc/systemd/system/servermgr-panel.service
PULL=0
HOST=""

die() { echo "deploy: $*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --pull) PULL=1; shift ;;
    --host) HOST="${2:-}"; shift 2 ;;
    -h|--help) sed -n '2,11p' "$0"; exit 0 ;;
    *) die "unknown argument: $1 (see --help)" ;;
  esac
done

if [ "$(id -u)" -eq 0 ] && [ -z "${SUDO_USER:-}" ]; then
  die "run as the user that owns this checkout, not as root"
fi
RUN_USER=${SUDO_USER:-$(id -un)}
command -v systemctl >/dev/null 2>&1 || die "systemd is required"
for t in go npm git curl; do
  command -v "$t" >/dev/null 2>&1 || die "$t is not installed"
done

if [ "$PULL" -eq 1 ]; then
  echo "==> Pulling the latest code"
  git pull --ff-only
fi

if [ ! -f .env ]; then
  echo "==> First run: creating the database and .env"
  sh scripts/setup-db.sh ${HOST:+"$HOST"}
fi

echo "==> Building UI, panel and agents ($(git describe --always --dirty 2>/dev/null || echo dev))"
make all

if [ ! -f "$UNIT" ]; then
  echo "==> Installing $UNIT (runs as $RUN_USER)"
  sudo tee "$UNIT" >/dev/null <<EOF
[Unit]
Description=servermgr panel
After=network-online.target postgresql.service
Wants=network-online.target

[Service]
User=$RUN_USER
WorkingDirectory=$ROOT
EnvironmentFile=$ROOT/.env
ExecStart=$ROOT/bin/panel
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF
  sudo systemctl daemon-reload
  sudo systemctl enable servermgr-panel >/dev/null 2>&1
fi

echo "==> Restarting servermgr-panel"
sudo systemctl restart servermgr-panel

# Wait until the API answers (401 = up, not logged in).
ADDR=$(sed -n 's/^SM_HTTP_ADDR=//p' .env | tail -n 1)
PORT=${ADDR##*:}
PORT=${PORT:-8090}
i=0
code=000
while [ $i -lt 30 ]; do
  code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/api/v1/me" || true)
  [ "$code" = 401 ] && break
  i=$((i + 1))
  sleep 1
done
if [ "$code" != 401 ]; then
  echo "deploy: the panel did not come up on port $PORT. Last log lines:" >&2
  sudo journalctl -u servermgr-panel -n 30 --no-pager >&2 || true
  exit 1
fi

HOSTNAME_SHOWN=$(sed -n 's/^SM_PUBLIC_HOST=//p' .env | tail -n 1)
echo "==> servermgr is running: http://${HOSTNAME_SHOWN:-127.0.0.1}:$PORT (https on :8443)"
echo "    If you use the secrets vault, unlock it in the UI after this restart."
