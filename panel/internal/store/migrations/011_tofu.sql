-- OpenTofu workspaces. vars is sealed JSON (AES-GCM with the panel key).
CREATE TABLE tofu_workspaces (
    id          uuid PRIMARY KEY,
    name        text NOT NULL UNIQUE,
    description text NOT NULL DEFAULT '',
    source      text NOT NULL DEFAULT 'inline' CHECK (source IN ('inline', 'git')),
    files       jsonb NOT NULL DEFAULT '{}',
    git_url     text NOT NULL DEFAULT '',
    git_ref     text NOT NULL DEFAULT '',
    git_path    text NOT NULL DEFAULT '',
    vars        text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- Remote state (http backend served by the panel), sealed.
CREATE TABLE tofu_states (
    workspace_id uuid PRIMARY KEY REFERENCES tofu_workspaces(id) ON DELETE CASCADE,
    state        text NOT NULL,
    serial       bigint NOT NULL DEFAULT 0,
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE tofu_state_versions (
    id           bigserial PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES tofu_workspaces(id) ON DELETE CASCADE,
    state        text NOT NULL,
    serial       bigint NOT NULL DEFAULT 0,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX tofu_state_versions_ws ON tofu_state_versions (workspace_id, id DESC);

CREATE TABLE tofu_locks (
    workspace_id uuid PRIMARY KEY REFERENCES tofu_workspaces(id) ON DELETE CASCADE,
    lock_id      text NOT NULL,
    info         jsonb NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE tofu_runs (
    id             uuid PRIMARY KEY,
    workspace_id   uuid NOT NULL REFERENCES tofu_workspaces(id) ON DELETE CASCADE,
    workspace_name text NOT NULL,
    kind           text NOT NULL CHECK (kind IN ('plan', 'destroy-plan', 'apply')),
    plan_run_id    uuid,
    status         text NOT NULL DEFAULT 'running',
    output         text NOT NULL DEFAULT '',
    summary        jsonb,
    applied        boolean NOT NULL DEFAULT false,
    created_by     text NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now(),
    finished_at    timestamptz
);
CREATE INDEX tofu_runs_ws ON tofu_runs (workspace_id, created_at DESC);

INSERT INTO tofu_workspaces (id, name, description, files) VALUES
('00000000-0000-4000-8000-0000000000b1', 'example-random',
 'Self-contained example: needs no cloud account. Shows plan/apply/destroy, outputs and the servermgr_servers variable.',
 jsonb_build_object('main.tf', 'terraform {
  required_providers {
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
  }
}

variable "prefix" {
  type    = string
  default = "sm"
}

# servermgr fills this in on every run: server name => address and OS.
variable "servermgr_servers" {
  type = map(object({
    id      = string
    address = string
    os      = string
  }))
  default = {}
}

resource "random_pet" "name" {
  prefix = var.prefix
  length = 2
}

output "pet_name" {
  value = random_pet.name.id
}

output "managed_servers" {
  value = { for name, s in var.servermgr_servers : name => s.address }
}
'));
