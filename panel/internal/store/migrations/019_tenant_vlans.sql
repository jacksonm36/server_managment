-- VLAN tags a tenant may put its guests on, on its bridges. Empty means
-- untagged only.
ALTER TABLE tenant_clusters ADD COLUMN vlans integer[] NOT NULL DEFAULT '{}';
