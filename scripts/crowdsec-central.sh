#!/bin/sh
# Central CrowdSec LAPI for servermgr, as a Docker container on the panel host.
#
#   - LAPI only (no local agent); agents on managed servers report to it and
#     every server's bouncer pulls decisions from it, so a ban anywhere
#     applies everywhere.
#   - TLS with a server certificate signed by the panel CA (data/pki), which
#     agents already trust; machines/bouncers authenticate with credentials.
#   - State lives in /var/lib/servermgr/crowdsec.
#
# Usage: scripts/crowdsec-central.sh [public-ip ...]   (idempotent)
set -eu
cd "$(dirname "$0")/.."
IMAGE=crowdsecurity/crowdsec:v1.8.1-slim
NAME=servermgr-crowdsec
PORT=${SM_CROWDSEC_PORT:-8085}
BASE=/var/lib/servermgr/crowdsec
IPS="${*:-$(. ./.env; echo "${SM_PUBLIC_HOST:-127.0.0.1}")}"

sudo mkdir -p "$BASE/config" "$BASE/data" "$BASE/ssl"
if ! sudo test -f "$BASE/ssl/cert.pem"; then
  san="DNS:localhost,IP:127.0.0.1"
  for ip in $IPS $(ip -4 -o addr show incusbr0 2>/dev/null | awk '{print $4}' | cut -d/ -f1); do san="$san,IP:$ip"; done
  tmp=$(mktemp -d)
  openssl ecparam -name prime256v1 -genkey -noout -out "$tmp/key.pem"
  openssl req -new -key "$tmp/key.pem" -subj "/CN=servermgr-crowdsec-lapi/OU=servermgr" -out "$tmp/req.csr"
  printf 'subjectAltName=%s\nextendedKeyUsage=serverAuth\n' "$san" > "$tmp/ext.cnf"
  openssl x509 -req -in "$tmp/req.csr" -CA data/pki/ca.crt -CAkey data/pki/ca.key -CAcreateserial \
    -days 825 -sha256 -extfile "$tmp/ext.cnf" -out "$tmp/cert.pem" 2>/dev/null
  sudo cp "$tmp/key.pem" "$tmp/cert.pem" "$BASE/ssl/"
  sudo cp data/pki/ca.crt "$BASE/ssl/ca.pem"
  rm -rf "$tmp"
  echo "issued LAPI certificate for $san"
fi

if sudo docker inspect "$NAME" >/dev/null 2>&1; then
  sudo docker start "$NAME" >/dev/null
else
  sudo docker run -d --name "$NAME" --restart unless-stopped \
    -p "$PORT:8080" \
    -e DISABLE_AGENT=true -e LOCAL_API_URL=https://127.0.0.1:8080 \
    -e USE_TLS=true \
    -e CACERT_FILE=/ssl/ca.pem -e LAPI_CERT_FILE=/ssl/cert.pem -e LAPI_KEY_FILE=/ssl/key.pem \
    -v "$BASE/config:/etc/crowdsec" -v "$BASE/data:/var/lib/crowdsec/data" -v "$BASE/ssl:/ssl:ro" \
    "$IMAGE" >/dev/null
fi
for i in $(seq 1 30); do
  sudo docker exec "$NAME" cscli lapi status >/dev/null 2>&1 && break
  sleep 2
done
sudo docker exec "$NAME" cscli version 2>/dev/null | head -1
echo "LAPI: https://$(echo "$IPS" | cut -d' ' -f1):$PORT"
