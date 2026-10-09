#!/bin/sh
# Render every vhost variant and validate them with a real `nginx -t`
# (in the official nginx Docker image). Needs docker (via sudo) and openssl.
set -eu
cd "$(dirname "$0")/.."
OUT=$(mktemp -d)
trap "rm -rf $OUT" EXIT
mkdir -p "$OUT/conf.d" "$OUT/certs"
SM_NGINX_DUMP="$OUT/conf.d" go test ./agent/internal/ops -run TestDumpNginx -count=1 >/dev/null
cp agent/internal/ops/testdata/nginx-check-servermgr.conf "$OUT/conf.d/00-servermgr.conf"
# Throwaway certificates standing in for the ones the agent issues.
for d in static.test php.test app.test; do
  mkdir -p "$OUT/certs/$d"
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 1 -subj "/CN=$d" \
    -keyout "$OUT/certs/$d/privkey.pem" -out "$OUT/certs/$d/fullchain.pem" 2>/dev/null
done
chmod -R a+rX "$OUT"
sudo docker run --rm -v "$OUT/conf.d:/etc/nginx/conf.d:ro" -v "$OUT/certs:/etc/servermgr-agent/certs:ro" nginx:1.27-alpine \
  sh -c "for d in static.test php.test app.test; do mkdir -p /srv/sites/\$d/logs; done; nginx -t"
