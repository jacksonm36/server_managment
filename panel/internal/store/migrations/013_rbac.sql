-- Custom roles, user groups, server groups and role grants.
CREATE TABLE roles (
    id          bigserial PRIMARY KEY,
    name        text NOT NULL UNIQUE,
    description text NOT NULL DEFAULT '',
    -- Built-in roles keep their permissions in code; this stays empty.
    permissions text[] NOT NULL DEFAULT '{}',
    builtin     boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now()
);

INSERT INTO roles (name, description, builtin) VALUES
    ('admin', 'Everything, including users and panel settings', true),
    ('operator', 'Run and change anything on servers and automation, but not users or panel settings', true),
    ('viewer', 'Read-only: servers, status and automation', true),
    ('none', 'No access of its own; use groups and grants', true);

ALTER TABLE admins DROP CONSTRAINT IF EXISTS admins_role_check;
ALTER TABLE admins
    ADD CONSTRAINT admins_role_fk FOREIGN KEY (role) REFERENCES roles (name) ON UPDATE CASCADE,
    ADD COLUMN display_name         text NOT NULL DEFAULT '',
    ADD COLUMN email                text NOT NULL DEFAULT '',
    ADD COLUMN must_change_password boolean NOT NULL DEFAULT false;

CREATE TABLE user_groups (
    id          bigserial PRIMARY KEY,
    name        text NOT NULL UNIQUE,
    description text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE user_group_members (
    group_id bigint NOT NULL REFERENCES user_groups (id) ON DELETE CASCADE,
    admin_id bigint NOT NULL REFERENCES admins (id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, admin_id)
);

CREATE TABLE server_groups (
    id          bigserial PRIMARY KEY,
    name        text NOT NULL UNIQUE,
    description text NOT NULL DEFAULT '',
    color       text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE server_group_members (
    group_id  bigint NOT NULL REFERENCES server_groups (id) ON DELETE CASCADE,
    server_id uuid NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, server_id)
);

-- A grant gives a user or a user group a role, on all servers (and
-- panel-wide) when server_group_id is NULL, or only on one server group.
CREATE TABLE role_grants (
    id              bigserial PRIMARY KEY,
    admin_id        bigint REFERENCES admins (id) ON DELETE CASCADE,
    user_group_id   bigint REFERENCES user_groups (id) ON DELETE CASCADE,
    role_id         bigint NOT NULL REFERENCES roles (id) ON DELETE RESTRICT,
    server_group_id bigint REFERENCES server_groups (id) ON DELETE CASCADE,
    created_at      timestamptz NOT NULL DEFAULT now(),
    CHECK ((admin_id IS NULL) <> (user_group_id IS NULL)),
    UNIQUE NULLS NOT DISTINCT (admin_id, user_group_id, role_id, server_group_id)
);

ALTER TABLE schedules ADD COLUMN server_group_id bigint REFERENCES server_groups (id) ON DELETE SET NULL;
