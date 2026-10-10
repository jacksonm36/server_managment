CREATE TABLE schedules (
    id          uuid PRIMARY KEY,
    name        text NOT NULL,
    cron        text NOT NULL,
    kind        text NOT NULL CHECK (kind IN ('op', 'playbook')),
    op          text NOT NULL DEFAULT '',
    params      jsonb NOT NULL DEFAULT '{}',
    playbook_id uuid REFERENCES playbooks(id) ON DELETE CASCADE,
    server_ids  jsonb NOT NULL DEFAULT '[]',
    all_servers boolean NOT NULL DEFAULT false,
    enabled     boolean NOT NULL DEFAULT true,
    created_by  text NOT NULL DEFAULT '',
    last_run    timestamptz,
    last_result text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
