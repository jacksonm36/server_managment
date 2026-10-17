#!/bin/sh
# servermgr agent installer. The panel serves this script at /install.sh with
# its own address filled in; the copy in the repository needs --panel.
#
#   Install:    curl -fsSLk --pinnedpubkey 'PIN' https://PANEL:8443/install.sh | sudo sh -s -- --token TOKEN --ca-sha256 SHA
#   From git:   sudo sh install-agent.sh --panel https://PANEL:8443 --pin 'PIN' --token TOKEN --ca-sha256 SHA
#   Update:     run it again without --token (keeps the enrollment)
#   Uninstall:  sudo sh install-agent.sh --uninstall
#
# Get the token, CA fingerprint and key pin from the panel: Servers -> Add server.
# Over HTTPS every download pins the panel's server key (or, when updating,
# trusts the panel CA saved at enrollment), so nothing comes from elsewhere.
set -eu

PANEL_URL='__PANEL_URL__'
PANEL_PIN='__PANEL_PIN__'
TOKEN=""
CA_SHA256=""
UNINSTALL=0
STATE_DIR=/etc/servermgr-agent
BIN=/usr/local/bin/servermgr-agent
UNIT=/etc/systemd/system/servermgr-agent.service

die() { echo "servermgr: $*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --panel) PANEL_URL="${2:-}"; shift 2 ;;
    --token) TOKEN="${2:-}"; shift 2 ;;
    --ca-sha256) CA_SHA256="${2:-}"; shift 2 ;;
    --pin) PANEL_PIN="${2:-}"; shift 2 ;;
    --uninstall) UNINSTALL=1; shift ;;
    -h|--help) sed -n '2,10p' "$0" 2>/dev/null || true; exit 0 ;;
    *) die "unknown argument: $1 (see --help)" ;;
  esac
done

[ "$(id -u)" -eq 0 ] || die "run as root (sudo)"
command -v systemctl >/dev/null 2>&1 || die "systemd is required"

if [ "$UNINSTALL" -eq 1 ]; then
  systemctl disable --now servermgr-agent 2>/dev/null || true
  rm -f "$UNIT" "$BIN"
  rm -rf /etc/systemd/system/servermgr-agent.service.d "$STATE_DIR"
  systemctl daemon-reload
  echo "servermgr agent removed. Remove the server in the panel too."
  exit 0
fi

case "$PANEL_URL" in
  __PANEL_URL__|"") die "--panel is required, e.g. --panel https://10.0.1.240:8443" ;;
  http://*|https://*) ;;
  *) die "--panel must start with http:// or https://" ;;
esac
PANEL_URL="${PANEL_URL%/}"

ENROLLED=0
[ -f "$STATE_DIR/state.json" ] && ENROLLED=1
if [ -z "$TOKEN" ] && [ "$ENROLLED" -eq 0 ]; then
  die "--token and --ca-sha256 are required for a new install (Servers -> Add server in the panel)"
fi
if [ -n "$TOKEN" ]; then
  [ -n "$CA_SHA256" ] || die "--ca-sha256 is required with --token"
  [ "$ENROLLED" -eq 0 ] || die "already enrolled; run with --uninstall first to enroll again"
fi
[ "$PANEL_PIN" = "__PANEL_PIN__" ] && PANEL_PIN=""
CURL_TLS=""
case "$PANEL_URL" in
  https://*)
    if [ -n "$PANEL_PIN" ]; then
      CURL_TLS="-k --pinnedpubkey $PANEL_PIN"
    elif [ "$ENROLLED" -eq 1 ] && [ -f "$STATE_DIR/ca.crt" ]; then
      CURL_TLS="--cacert $STATE_DIR/ca.crt"
    else
      die "--pin is required with an https panel (copy the install command from the panel)"
    fi ;;
esac

command -v curl >/dev/null 2>&1 || {
  echo "Installing curl..."
  if command -v apt-get >/dev/null 2>&1; then apt-get update -qq && apt-get install -y -qq curl
  elif command -v dnf >/dev/null 2>&1; then dnf install -y -q curl
  else die "curl is required"; fi
}

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "unsupported architecture: $(uname -m)" ;;
esac

TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT
echo "Downloading the agent ($ARCH) from $PANEL_URL..."
# shellcheck disable=SC2086 # CURL_TLS holds separate options
curl -fsSL $CURL_TLS "$PANEL_URL/download/servermgr-agent-linux-$ARCH" -o "$TMP" || die "download failed; is $PANEL_URL reachable from here?"
[ -s "$TMP" ] || die "downloaded an empty file"
install -m 0755 "$TMP" "$BIN"

if [ -n "$TOKEN" ]; then
  "$BIN" enroll --panel "$PANEL_URL" --token "$TOKEN" --ca-sha256 "$CA_SHA256"
else
  # An agent enrolled over plain HTTP moves to this (HTTPS) address.
  sed -i "s#\"panel_url\": *\"[^\"]*\"#\"panel_url\": \"$PANEL_URL\"#" "$STATE_DIR/state.json"
fi

cat > "$UNIT" <<'UNIT'
[Unit]
Description=servermgr agent
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/servermgr-agent run
Restart=always
RestartSec=5
OOMPolicy=continue

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable servermgr-agent >/dev/null 2>&1
systemctl restart servermgr-agent

if [ -n "$TOKEN" ]; then
  echo "servermgr agent installed and running. It shows up in the panel within a few seconds."
else
  echo "servermgr agent updated and restarted."
fi
