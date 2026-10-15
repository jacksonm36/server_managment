-- A workspace can be tied to a Proxmox cluster: its runs reach that
-- cluster's API through the panel, which adds the cluster's token.
ALTER TABLE tofu_workspaces ADD COLUMN proxmox_cluster uuid REFERENCES proxmox_clusters (id) ON DELETE SET NULL;
