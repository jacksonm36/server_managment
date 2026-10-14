-- Tenants: customers or teams kept apart from each other. A tenant user
-- only reaches the tenant's Proxmox pool (through a Proxmox token that can
-- see nothing else) and the servers of the tenant's server group.
CREATE TABLE tenants (
    id              bigserial PRIMARY KEY,
    name            text NOT NULL UNIQUE,
    -- slug names the tenant's Proxmox user (sm-<slug>@pve) and default pool.
    slug            text NOT NULL UNIQUE,
    description     text NOT NULL DEFAULT '',
    server_group_id bigint REFERENCES server_groups (id) ON DELETE SET NULL,
    -- Quotas over all of the tenant's guests; 0 means no limit.
    max_guests      int NOT NULL DEFAULT 0,
    max_cores       int NOT NULL DEFAULT 0,
    max_memory_mb   bigint NOT NULL DEFAULT 0,
    max_disk_gb     bigint NOT NULL DEFAULT 0,
    created_at      timestamptz NOT NULL DEFAULT now()
);

-- A tenant's slice of a Proxmox cluster: its pool, what it may use, and
-- the Proxmox API token (sealed) limited to exactly that by Proxmox ACLs.
CREATE TABLE tenant_clusters (
    tenant_id    bigint NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    cluster_id   uuid NOT NULL REFERENCES proxmox_clusters (id) ON DELETE CASCADE,
    pool         text NOT NULL,
    storages     text[] NOT NULL DEFAULT '{}',
    bridges      text[] NOT NULL DEFAULT '{}',
    templates    int[] NOT NULL DEFAULT '{}',
    pve_user     text NOT NULL DEFAULT '',
    token_id     text NOT NULL DEFAULT '',
    token_secret text NOT NULL DEFAULT '',
    set_up_at    timestamptz,
    PRIMARY KEY (tenant_id, cluster_id),
    UNIQUE (cluster_id, pool)
);

ALTER TABLE admins ADD COLUMN tenant_id bigint REFERENCES tenants (id) ON DELETE RESTRICT;
ALTER TABLE proxmox_provisions ADD COLUMN tenant_id bigint REFERENCES tenants (id) ON DELETE CASCADE;
-- No foreign key: the audit trail outlives a deleted tenant.
ALTER TABLE audit_log ADD COLUMN tenant_id bigint;
CREATE INDEX audit_log_tenant ON audit_log (tenant_id, id DESC) WHERE tenant_id IS NOT NULL;

-- Creating and deleting guests, and the console, become their own
-- permissions; proxmox.manage now means clusters and tenants. Custom roles
-- keep what they could do.
UPDATE roles SET permissions = array_append(permissions, 'proxmox.guests')
    WHERE 'proxmox.manage' = ANY (permissions) AND NOT 'proxmox.guests' = ANY (permissions);
UPDATE roles SET permissions = array_append(permissions, 'proxmox.console')
    WHERE 'proxmox.operate' = ANY (permissions) AND NOT 'proxmox.console' = ANY (permissions);
