#!/bin/sh
# Creates the servermgr PostgreSQL role and database on the local server and
# writes .env with generated secrets. Safe to re-run: it never overwrites an
# existing .env. Secrets are written to .env only, never printed.
set -eu
cd "$(dirname "$0")/.."

if [ -f .env ]; then
  echo ".env already exists, leaving it alone."
  exit 0
fi

PUBLIC_HOST="${1:-$(hostname -I | awk '{print $1}')}"
DB_PASS=$(head -c 32 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 32)
ADMIN_PASS=$(head -c 32 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 20)

sudo -u postgres psql -v ON_ERROR_STOP=1 -q <<SQL
DO \$\$ BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'servermgr') THEN
    CREATE ROLE servermgr LOGIN PASSWORD '$DB_PASS';
  ELSE
    ALTER ROLE servermgr PASSWORD '$DB_PASS';
  END IF;
END \$\$;
SQL
if ! sudo -u postgres psql -tAc "SELECT 1 FROM pg_database WHERE datname='servermgr'" | grep -q 1; then
  sudo -u postgres createdb -O servermgr servermgr
fi

umask 077
cat > .env <<EOF
SM_DATABASE_URL=postgres://servermgr:$DB_PASS@127.0.0.1:5432/servermgr?sslmode=disable
SM_HTTP_ADDR=:8090
SM_GRPC_ADDR=:9443
SM_PUBLIC_HOST=$PUBLIC_HOST
SM_DATA_DIR=./data
SM_AGENT_BIN_DIR=./bin
SM_ADMIN_USER=admin
# Only used on the very first start to create the admin account.
SM_ADMIN_PASSWORD=$ADMIN_PASS
EOF
echo "Created database 'servermgr' and wrote .env (admin password is in SM_ADMIN_PASSWORD)."
