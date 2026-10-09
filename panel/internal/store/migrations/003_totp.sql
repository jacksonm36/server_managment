ALTER TABLE admins
    ADD COLUMN totp_enabled   boolean NOT NULL DEFAULT false,
    ADD COLUMN totp_pending   text,
    ADD COLUMN totp_last_step bigint  NOT NULL DEFAULT 0;

CREATE TABLE admin_recovery_codes (
    admin_id  bigint NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
    code_hash bytea  NOT NULL,
    PRIMARY KEY (admin_id, code_hash)
);
