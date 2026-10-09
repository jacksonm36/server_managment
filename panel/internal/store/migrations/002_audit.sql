CREATE TABLE audit_log (
    id      bigserial PRIMARY KEY,
    ts      timestamptz NOT NULL DEFAULT now(),
    actor   text NOT NULL DEFAULT '',
    ip      text NOT NULL DEFAULT '',
    action  text NOT NULL,
    target  text NOT NULL DEFAULT '',
    detail  jsonb NOT NULL DEFAULT '{}',
    success boolean NOT NULL DEFAULT true
);

CREATE INDEX audit_log_action ON audit_log (action, id DESC);

ALTER TABLE jobs ADD COLUMN created_by text NOT NULL DEFAULT '';
CREATE INDEX jobs_created ON jobs (created_at DESC);
