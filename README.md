# servermgr

A self-hosted control panel for managing Linux servers, in the spirit of
CloudPanel / aaPanel but built around a lightweight agent on each managed
server.

```
 browser ──HTTP──▶ panel (Go) ──▶ PostgreSQL
                     ▲
                     │ gRPC over mTLS (agent dials out, port 9443)
                     │
          agent (Go) on each managed server
```

- **panel/**: HTTP JSON API and UI (`:8090`), agent enrollment, an internal CA,
  and a gRPC listener with mutual TLS (`:9443`). Connected agents are tracked in
  memory, and the last 15 minutes of metrics are kept per server.
- **agent/**: a static Go binary. It enrolls once with a one-time join token,
  generates its own key and CSR, and gets the CSR signed by the panel CA. After
  that it keeps a single outbound mTLS stream open, sends metrics every 5 s and
  runs jobs. Managed servers need no inbound ports.
- **proto/**: the `agent.v1.AgentService` contract (`Connect` bidi stream).
  Generated code is in `gen/`.
- **web/**: React + TypeScript + Vite + Tailwind v4 with shadcn/ui-style
  components. It is built into `web/dist` and embedded in the panel binary.

## Security model

- Agents authenticate with client certificates issued by the panel's internal
  CA (`data/pki/`). The agent's server ID is the certificate CommonName.
  Deleting a server in the UI makes the panel refuse that agent.
- The install command carries the CA's SHA-256 fingerprint. The agent refuses
  to enroll if the CA it receives doesn't match.
- Join tokens are single-use, expire after 1 hour and are stored only as
  SHA-256 hashes.
- The admin password is hashed with argon2id. Sessions use an HttpOnly,
  SameSite=Strict cookie, and mutating API calls also require an
  `X-Requested-With` header. Failed logins and enrollments are rate-limited
  per IP.
- TLS 1.3 only on the gRPC port.

## Running on a dev server (Debian 13 / Ubuntu)

Requirements: Go 1.24+ (the toolchain auto-downloads 1.26 because of gRPC),
Node 20.19+, PostgreSQL, and protoc (only needed to regenerate `gen/`).

```sh
./scripts/setup-db.sh 10.0.1.240   # creates DB + role, writes .env (secrets stay in .env)
make web panel agent               # UI, panel, agent binaries into bin/
make run                           # or use the systemd unit below
```

On first start the panel creates the admin user from `SM_ADMIN_USER` /
`SM_ADMIN_PASSWORD` in `.env`. After that you can remove `SM_ADMIN_PASSWORD`
from `.env`.

Systemd unit used on the dev server (`/etc/systemd/system/servermgr-panel.service`):

```ini
[Service]
User=jackson
WorkingDirectory=/home/jackson/servermgr
EnvironmentFile=/home/jackson/servermgr/.env
ExecStart=/home/jackson/servermgr/bin/panel
Restart=on-failure
```

### Adding a server

In the UI, click **Add server**. That gives a one-line command like:

```sh
curl -fsSL http://10.0.1.240:8090/install.sh | sudo sh -s -- --token <one-time> --ca-sha256 <fingerprint>
```

It downloads the agent, enrolls it and installs `servermgr-agent.service`.

### Test servers with Incus

```sh
sudo incus launch images:debian/13 sm-node1 -c limits.memory=512MiB
sudo incus exec sm-node1 -- sh -c 'apt-get update && apt-get install -y curl'
```

On hosts that also run Docker, Docker sets the iptables `FORWARD` policy to
`DROP`, which cuts Incus containers off from the internet. Allow the bridge in
`DOCKER-USER`:

```sh
sudo iptables -I DOCKER-USER -i incusbr0 -j ACCEPT
sudo iptables -I DOCKER-USER -o incusbr0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
```

## Agent ops

| op                       | what it does                                          |
| ------------------------ | ----------------------------------------------------- |
| `system.info`            | OS, kernel, CPU model, virtualization                 |
| `packages.check_updates` | `apt-get update` + upgradable list, security count    |

## Roadmap

TOTP 2FA, HTTPS for the UI, package upgrades, SSH key deployment, scheduled
automation, firewall/fail2ban hardening, web server and site management
(nginx, PHP-FPM, TLS via ACME), databases, backups.
