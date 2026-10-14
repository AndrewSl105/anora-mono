-- +goose Up
-- Where a project's forum topic lives, and each task's status message, so the
-- bot can keep editing it across restarts.
ALTER TABLE projects ADD COLUMN telegram_chat_id bigint;
ALTER TABLE tasks ADD COLUMN telegram_chat_id bigint;
ALTER TABLE tasks ADD COLUMN telegram_thread_id bigint;
ALTER TABLE tasks ADD COLUMN telegram_message_id bigint;

-- +goose Down
ALTER TABLE tasks DROP COLUMN telegram_message_id;
ALTER TABLE tasks DROP COLUMN telegram_thread_id;
ALTER TABLE tasks DROP COLUMN telegram_chat_id;
ALTER TABLE projects DROP COLUMN telegram_chat_id;
