-- SSH public key library. The key itself is sealed by the secrets vault;
-- name, type and fingerprint stay readable so the list works while locked.
CREATE TABLE ssh_keys (
    id          bigserial PRIMARY KEY,
    name        text NOT NULL UNIQUE,
    key_type    text NOT NULL,
    bits        int  NOT NULL DEFAULT 0,
    fingerprint text NOT NULL UNIQUE,
    comment     text NOT NULL DEFAULT '',
    public_key  text NOT NULL,
    source      text NOT NULL DEFAULT '',
    created_by  text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now()
);
