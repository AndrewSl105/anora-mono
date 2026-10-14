-- +goose Up
CREATE TABLE projects (
  id              text PRIMARY KEY,
  name            text NOT NULL,
  instructions    text NOT NULL DEFAULT '',
  services        text[] NOT NULL DEFAULT '{}',
  env             jsonb NOT NULL DEFAULT '{}',
  runner_image    text,
  model           text,
  max_turns       int,
  max_budget_usd  numeric(8,2),
  max_duration    interval,
  runner_memory   text,
  runner_cpus     numeric(4,2),
  telegram_thread_id bigint,
  archived        boolean NOT NULL DEFAULT false,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE repositories (
  id              bigserial PRIMARY KEY,
  project_id      text NOT NULL REFERENCES projects(id),
  full_name       text NOT NULL,
  base_branch     text NOT NULL DEFAULT 'dev',
  installation_id bigint NOT NULL,
  is_default      boolean NOT NULL DEFAULT false,
  UNIQUE (project_id, full_name)
);

CREATE TABLE tasks (
  id              bigserial PRIMARY KEY,
  project_id      text NOT NULL REFERENCES projects(id),
  repository_id   bigint NOT NULL REFERENCES repositories(id),
  title           text NOT NULL,
  prompt          text NOT NULL,
  status          text NOT NULL,
  status_reason   text,
  branch          text,
  session_id      text,
  pr_number       int,
  pr_url          text,
  ci_status       text,
  cost_usd_total  numeric(10,4) NOT NULL DEFAULT 0,
  source          text NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  finished_at     timestamptz,
  -- Not in the spec's starting schema; see docs/DECISIONS.md.
  paused_at       timestamptz,
  cleaned_at      timestamptz
);
CREATE INDEX ON tasks (status, created_at);

CREATE TABLE runs (
  id              bigserial PRIMARY KEY,
  task_id         bigint NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  kind            text NOT NULL,
  input           text NOT NULL,
  container_id    text,
  exit_code       int,
  last_seq        bigint NOT NULL DEFAULT 0,
  cost_usd        numeric(10,4) NOT NULL DEFAULT 0,
  started_at      timestamptz,
  finished_at     timestamptz,
  -- queued runs wait here until the scheduler starts them.
  created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ON runs (task_id, id);

CREATE TABLE events (
  id              bigserial PRIMARY KEY,
  task_id         bigint NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  run_id          bigint REFERENCES runs(id) ON DELETE CASCADE,
  seq             bigint,
  type            text NOT NULL,
  payload         jsonb NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (run_id, seq)
);
CREATE INDEX ON events (task_id, id);

CREATE TABLE questions (
  id              text PRIMARY KEY,
  task_id         bigint NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  run_id          bigint NOT NULL REFERENCES runs(id),
  text            text NOT NULL,
  options         jsonb,
  answer          text,
  answered_via    text,
  telegram_message_id bigint,
  created_at      timestamptz NOT NULL DEFAULT now(),
  answered_at     timestamptz
);
CREATE INDEX ON questions (task_id) WHERE answer IS NULL;

CREATE TABLE auth_sessions (
  id              text PRIMARY KEY,
  kind            text NOT NULL,
  expires_at      timestamptz NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  last_seen_at    timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE auth_sessions;
DROP TABLE questions;
DROP TABLE events;
DROP TABLE runs;
DROP TABLE tasks;
DROP TABLE repositories;
DROP TABLE projects;
