-- +goose Up
-- Text the owner sent while a run was active. It starts the next run when the
-- current one ends (see setStatusTx); a run never receives it mid-session.
ALTER TABLE tasks ADD COLUMN queued_message text;

-- +goose Down
ALTER TABLE tasks DROP COLUMN queued_message;
