-- +goose Up
-- The agent picked on a Telegram draft card; '' runs with the project's agent.
ALTER TABLE telegram_drafts ADD COLUMN agent text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE telegram_drafts DROP COLUMN agent;
