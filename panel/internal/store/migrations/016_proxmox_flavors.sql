-- Flavors: named VM sizes offered when creating a VM, for every cluster
-- and tenant. disk_gb is the disk size the VM gets (0 keeps the
-- template's).
CREATE TABLE proxmox_flavors (
    id          bigserial PRIMARY KEY,
    name        text NOT NULL UNIQUE,
    cores       int NOT NULL CHECK (cores BETWEEN 1 AND 128),
    memory_mb   int NOT NULL CHECK (memory_mb BETWEEN 256 AND 1048576),
    disk_gb     int NOT NULL DEFAULT 0 CHECK (disk_gb BETWEEN 0 AND 10000),
    description text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now()
);

INSERT INTO proxmox_flavors (name, cores, memory_mb, disk_gb, description) VALUES
    ('small', 1, 1024, 16, 'Small services, DNS, a proxy'),
    ('medium', 2, 4096, 32, 'Web apps and small databases'),
    ('large', 4, 8192, 64, 'Busy apps and databases'),
    ('xlarge', 8, 16384, 128, 'Heavy workloads');
