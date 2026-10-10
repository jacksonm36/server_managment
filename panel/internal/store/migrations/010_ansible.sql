-- Servers prepared for Ansible: where to SSH and the pinned host keys.
CREATE TABLE ansible_hosts (
    server_id   uuid PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    address     text NOT NULL,
    port        int NOT NULL DEFAULT 22,
    host_keys   jsonb NOT NULL DEFAULT '[]',
    root_login  text NOT NULL DEFAULT '',
    prepared_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE ansible_playbooks (
    id          uuid PRIMARY KEY,
    name        text NOT NULL UNIQUE,
    description text NOT NULL DEFAULT '',
    source      text NOT NULL DEFAULT 'inline' CHECK (source IN ('inline', 'git')),
    content     text NOT NULL DEFAULT '',
    git_url     text NOT NULL DEFAULT '',
    git_ref     text NOT NULL DEFAULT '',
    git_path    text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE ansible_runs (
    id            uuid PRIMARY KEY,
    playbook_id   uuid REFERENCES ansible_playbooks(id) ON DELETE SET NULL,
    playbook_name text NOT NULL,
    server_ids    jsonb NOT NULL DEFAULT '[]',
    check_mode    boolean NOT NULL DEFAULT false,
    status        text NOT NULL DEFAULT 'running',
    output        text NOT NULL DEFAULT '',
    recap         jsonb,
    created_by    text NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now(),
    finished_at   timestamptz
);

CREATE INDEX ansible_runs_created ON ansible_runs (created_at DESC);

ALTER TABLE schedules DROP CONSTRAINT IF EXISTS schedules_kind_check;
ALTER TABLE schedules ADD CONSTRAINT schedules_kind_check CHECK (kind IN ('op', 'playbook', 'ansible'));
ALTER TABLE schedules ADD COLUMN ansible_playbook_id uuid REFERENCES ansible_playbooks(id) ON DELETE CASCADE;

INSERT INTO ansible_playbooks (id, name, description, content) VALUES
('00000000-0000-4000-8000-0000000000a1', 'Ping and facts',
 'Checks that Ansible can reach each server and prints its OS.',
 '- name: Check connectivity
  hosts: all
  gather_facts: true
  tasks:
    - name: Ping
      ansible.builtin.ping:

    - name: Show the operating system
      ansible.builtin.debug:
        msg: "{{ ansible_facts.distribution }} {{ ansible_facts.distribution_version }}, kernel {{ ansible_facts.kernel }}"
'),
('00000000-0000-4000-8000-0000000000a2', 'Common tools',
 'Installs curl, git, tmux and rsync on Debian/Ubuntu and RHEL-family servers.',
 '- name: Install common tools
  hosts: all
  become: false
  tasks:
    - name: Install packages
      ansible.builtin.package:
        name: [curl, git, tmux, rsync]
        state: present
');
