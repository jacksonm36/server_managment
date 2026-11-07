-- The SMBIOS UUID of a linked VM, so a reused VMID is told apart from the
-- VM the server was enrolled from ('' = not known yet, containers).
ALTER TABLE proxmox_links ADD COLUMN vm_uuid text NOT NULL DEFAULT '';
