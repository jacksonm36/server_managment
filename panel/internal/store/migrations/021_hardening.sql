-- When the agent's client certificate expires (agents renew it themselves).
ALTER TABLE servers ADD COLUMN cert_expires_at timestamptz;

-- Channels that also get agent events (failed certificate renewals,
-- failed scheduled backups), not only alert rules.
ALTER TABLE notify_channels ADD COLUMN notify_events boolean NOT NULL DEFAULT false;

-- Idle sessions end after a while (see api.sessionIdle).
ALTER TABLE sessions ADD COLUMN last_used_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE sessions ADD COLUMN ip text NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN user_agent text NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS jobs_created_at_idx ON jobs (created_at);
CREATE INDEX IF NOT EXISTS audit_log_ts_idx ON audit_log (ts);
