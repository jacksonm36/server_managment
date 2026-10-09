#!/bin/sh
# Run the MariaDB integration test against a throwaway mariadb container.
set -eu
cd "$(dirname "$0")/.."
BIN=$(mktemp)
trap 'rm -f "$BIN"; sudo docker rm -f sm-mariadb-it >/dev/null 2>&1 || true' EXIT
CGO_ENABLED=0 go test -c -o "$BIN" ./agent/internal/ops
sudo docker run -d --name sm-mariadb-it -e MARIADB_ALLOW_EMPTY_ROOT_PASSWORD=1 mariadb:11.4 >/dev/null
for i in $(seq 1 60); do
  sudo docker exec sm-mariadb-it mariadb -u root -e "SELECT 1" >/dev/null 2>&1 && break
  sleep 1
done
sudo docker cp "$BIN" sm-mariadb-it:/tmp/ops.test
sudo docker exec -e SM_MARIADB_IT=1 sm-mariadb-it /tmp/ops.test -test.run TestMariaDB -test.v
