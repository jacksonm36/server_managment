-- Proxmox VE clusters: API token secret sealed with the panel key, TLS
-- certificate pinned by SHA-256 fingerprint.
CREATE TABLE proxmox_clusters (
    id           uuid PRIMARY KEY,
    name         text NOT NULL UNIQUE,
    url          text NOT NULL,
    token_id     text NOT NULL,
    token_secret text NOT NULL,
    fingerprint  text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- Guests enrolled as servermgr servers.
CREATE TABLE proxmox_links (
    cluster_id uuid NOT NULL REFERENCES proxmox_clusters(id) ON DELETE CASCADE,
    vmid       int NOT NULL,
    server_id  uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    PRIMARY KEY (cluster_id, vmid)
);

-- VM provisioning from a cloud-init template (clone, configure, start,
-- optionally enroll the agent).
CREATE TABLE proxmox_provisions (
    id          uuid PRIMARY KEY,
    cluster_id  uuid NOT NULL REFERENCES proxmox_clusters(id) ON DELETE CASCADE,
    name        text NOT NULL,
    vmid        int,
    status      text NOT NULL DEFAULT 'running',
    log         text NOT NULL DEFAULT '',
    server_id   uuid,
    created_by  text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz
);
CREATE INDEX proxmox_provisions_created ON proxmox_provisions (created_at DESC);
