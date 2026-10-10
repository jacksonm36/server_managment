ALTER TABLE admins
    ADD COLUMN role       text NOT NULL DEFAULT 'admin' CHECK (role IN ('admin', 'operator', 'viewer')),
    ADD COLUMN disabled   boolean NOT NULL DEFAULT false,
    ADD COLUMN last_login timestamptz;
