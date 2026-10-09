CREATE TABLE admins (
    id            bigserial PRIMARY KEY,
    username      text NOT NULL UNIQUE,
    password_hash text NOT NULL,
    totp_secret   text,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
    token_hash bytea PRIMARY KEY,
    admin_id   bigint NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);

CREATE TABLE join_tokens (
    token_hash bytea PRIMARY KEY,
    name       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    server_id  uuid
);

CREATE TABLE servers (
    id            uuid PRIMARY KEY,
    name          text NOT NULL,
    hostname      text NOT NULL DEFAULT '',
    os            text NOT NULL DEFAULT '',
    os_version    text NOT NULL DEFAULT '',
    os_pretty     text NOT NULL DEFAULT '',
    kernel        text NOT NULL DEFAULT '',
    arch          text NOT NULL DEFAULT '',
    cpu_count     int  NOT NULL DEFAULT 0,
    agent_version text NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_seen_at  timestamptz
);

CREATE TABLE jobs (
    id          uuid PRIMARY KEY,
    server_id   uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    op          text NOT NULL,
    params      jsonb NOT NULL DEFAULT '{}',
    status      text NOT NULL DEFAULT 'queued',
    log         text NOT NULL DEFAULT '',
    result      jsonb,
    error       text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz
);

CREATE INDEX jobs_server_created ON jobs (server_id, created_at DESC);
