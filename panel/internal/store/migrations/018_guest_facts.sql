-- What servermgr last learned about a Proxmox guest's operating system,
-- so a VM keeps its logo while it is stopped or its guest agent is quiet.
-- vm_uuid is the VM's SMBIOS UUID: a new VM that reuses the VMID has a
-- different one, so it never inherits an old guest's answer.
CREATE TABLE proxmox_guest_facts (
    cluster_id uuid NOT NULL REFERENCES proxmox_clusters(id) ON DELETE CASCADE,
    vmid       int NOT NULL,
    vm_uuid    text NOT NULL DEFAULT '',
    os         text NOT NULL,
    pretty     text NOT NULL DEFAULT '',
    hostname   text NOT NULL DEFAULT '',
    seen_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (cluster_id, vmid)
);
