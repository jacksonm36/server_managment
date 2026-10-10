CREATE TABLE settings (
    key        text PRIMARY KEY,
    value      jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Playbook to run when the server enrolled with this token first connects.
ALTER TABLE join_tokens ADD COLUMN playbook_id uuid REFERENCES playbooks(id) ON DELETE SET NULL;
ALTER TABLE servers ADD COLUMN baseline_applied_at timestamptz;
-- Servers that already exist never get a baseline retroactively.
UPDATE servers SET baseline_applied_at = now();
