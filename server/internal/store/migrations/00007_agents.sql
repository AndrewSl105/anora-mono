-- +goose Up
-- The coding agent a task runs with. A project may pick one (NULL: the server's
-- DEFAULT_AGENT); a task stores the agent it was created with, so its follow-ups
-- and resumes stay with the session's agent.
ALTER TABLE projects ADD COLUMN agent text CHECK (agent IN ('claude', 'codex', 'cursor'));
ALTER TABLE tasks ADD COLUMN agent text NOT NULL DEFAULT 'claude' CHECK (agent IN ('claude', 'codex', 'cursor'));

-- +goose Down
ALTER TABLE tasks DROP COLUMN agent;
ALTER TABLE projects DROP COLUMN agent;
