CREATE TABLE notify_channels (
    id           uuid PRIMARY KEY,
    name         text NOT NULL,
    type         text NOT NULL CHECK (type IN ('email', 'telegram', 'discord', 'webhook')),
    config       text NOT NULL,  -- AES-GCM sealed JSON
    enabled      boolean NOT NULL DEFAULT true,
    last_sent_at timestamptz,
    last_error   text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE alert_rules (
    id           uuid PRIMARY KEY,
    name         text NOT NULL,
    metric       text NOT NULL CHECK (metric IN ('cpu', 'mem', 'disk', 'load', 'offline')),
    threshold    double precision NOT NULL DEFAULT 0,
    duration_sec integer NOT NULL DEFAULT 300,
    server_ids   jsonb NOT NULL DEFAULT '[]',
    all_servers  boolean NOT NULL DEFAULT true,
    channel_ids  jsonb NOT NULL DEFAULT '[]',
    enabled      boolean NOT NULL DEFAULT true,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE alert_events (
    id          bigserial PRIMARY KEY,
    rule_id     uuid NOT NULL REFERENCES alert_rules(id) ON DELETE CASCADE,
    rule_name   text NOT NULL,
    server_id   uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    server_name text NOT NULL,
    metric      text NOT NULL,
    value       double precision NOT NULL,
    threshold   double precision NOT NULL,
    state       text NOT NULL CHECK (state IN ('firing', 'resolved')),
    started_at  timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz
);

-- At most one open alert per rule and server.
CREATE UNIQUE INDEX alert_events_open ON alert_events (rule_id, server_id) WHERE state = 'firing';
CREATE INDEX alert_events_started ON alert_events (started_at DESC);
