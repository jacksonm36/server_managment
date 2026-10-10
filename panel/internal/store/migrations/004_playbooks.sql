CREATE TABLE playbooks (
    id          uuid PRIMARY KEY,
    name        text NOT NULL,
    description text NOT NULL DEFAULT '',
    steps       jsonb NOT NULL DEFAULT '[]',
    builtin     boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE playbook_runs (
    id            uuid PRIMARY KEY,
    playbook_id   uuid REFERENCES playbooks(id) ON DELETE SET NULL,
    playbook_name text NOT NULL,
    server_id     uuid NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    status        text NOT NULL DEFAULT 'running',
    steps         jsonb NOT NULL,
    created_by    text NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now(),
    finished_at   timestamptz
);

CREATE INDEX playbook_runs_created ON playbook_runs (created_at DESC);

INSERT INTO playbooks (id, name, description, builtin, steps) VALUES
('00000000-0000-4000-8000-000000000001', 'Security baseline',
 'Firewall on (SSH kept open), Fail2ban with an sshd jail, and all package updates.', true,
 '[{"op":"packages.upgrade","params":{"dist":"false","autoremove":"true"}},
   {"op":"firewall.install","params":{}},
   {"op":"firewall.enable","params":{}},
   {"op":"fail2ban.install","params":{}}]'),
('00000000-0000-4000-8000-000000000002', 'Web server baseline',
 'Security baseline plus nginx + PHP 8.4, MariaDB, and HTTP/HTTPS open in the firewall.', true,
 '[{"op":"packages.upgrade","params":{"dist":"false","autoremove":"true"}},
   {"op":"firewall.install","params":{}},
   {"op":"firewall.enable","params":{}},
   {"op":"firewall.rule_add","params":{"action":"allow","port":"80","proto":"tcp","comment":"HTTP"}},
   {"op":"firewall.rule_add","params":{"action":"allow","port":"443","proto":"tcp","comment":"HTTPS"}},
   {"op":"fail2ban.install","params":{}},
   {"op":"web.install","params":{"php":"8.4"}},
   {"op":"db.install","params":{}}]');
