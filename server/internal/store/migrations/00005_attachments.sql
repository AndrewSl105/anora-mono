-- +goose Up
-- Images attached to a task (its `created` event) or to a message. Files live
-- at <ATTACHMENTS_DIR>/<task_id>/<id>.<ext>. run_id is the run that received
-- the attachment in its prompt (NULL until a run starts with it).
CREATE TABLE attachments (
  id            bigserial PRIMARY KEY,
  task_id       bigint NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  event_id      bigint REFERENCES events(id) ON DELETE SET NULL,
  run_id        bigint REFERENCES runs(id) ON DELETE SET NULL,
  original_name text NOT NULL,
  mime          text NOT NULL,
  size_bytes    bigint NOT NULL,
  width         int NOT NULL,
  height        int NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),
  deleted_at    timestamptz
);
CREATE INDEX ON attachments (task_id, id);

-- +goose Down
DROP TABLE attachments;
