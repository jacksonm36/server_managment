-- DNS hosting accounts (Cloudflare, Hetzner, …). config is the sealed
-- JSON of the provider's settings (API tokens).
CREATE TABLE dns_providers (
    id         uuid PRIMARY KEY,
    name       text NOT NULL,
    type       text NOT NULL,
    config     text NOT NULL,
    zones      jsonb NOT NULL DEFAULT '[]', -- last seen zone names, for lookups
    last_error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
