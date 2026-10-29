-- +goose Up
-- Computers that run tasks next to the server (docs/design/remote-nodes.md).
-- token_hash is the SHA-256 of the node token's secret part; the token itself
-- is shown once at registration.
CREATE TABLE nodes (
  id              text PRIMARY KEY,
  name            text NOT NULL,
  token_hash      bytea NOT NULL,
  max_concurrent  int NOT NULL DEFAULT 2 CHECK (max_concurrent BETWEEN 1 AND 32),
  agents          text[] NOT NULL DEFAULT '{}',
  platform        text NOT NULL DEFAULT '',
  version         text NOT NULL DEFAULT '',
  last_seen_at    timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now()
);

-- Where a project's tasks run (NULL: the server) and where a task runs, fixed
-- when it is created. tasks.node has no foreign key: a removed node's tasks
-- keep their history.
ALTER TABLE projects ADD COLUMN node text REFERENCES nodes(id) ON DELETE SET NULL;
ALTER TABLE tasks ADD COLUMN node text;
CREATE INDEX ON tasks (node, status);

-- The host picked on a Telegram draft card; '' runs on the project's host.
ALTER TABLE telegram_drafts ADD COLUMN node text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE telegram_drafts DROP COLUMN node;
DROP INDEX IF EXISTS tasks_node_status_idx;
ALTER TABLE tasks DROP COLUMN node;
ALTER TABLE projects DROP COLUMN node;
DROP TABLE nodes;
