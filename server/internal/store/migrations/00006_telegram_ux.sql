-- +goose Up
-- Every bot message associated with a task is addressable by replies. Keeping
-- whether it still has buttons lets a state transition remove stale actions.
CREATE TABLE telegram_task_messages (
  chat_id       bigint NOT NULL,
  message_id    bigint NOT NULL,
  task_id       bigint NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  kind          text NOT NULL,
  has_buttons   boolean NOT NULL DEFAULT false,
  created_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (chat_id, message_id)
);
CREATE INDEX ON telegram_task_messages (task_id, has_buttons);

-- Callback payloads contain only an opaque short id; all mutable action data
-- stays server-side and callback_data remains safely below Telegram's 64 bytes.
CREATE TABLE telegram_callbacks (
  id            text PRIMARY KEY,
  action        text NOT NULL,
  task_id       bigint REFERENCES tasks(id) ON DELETE CASCADE,
  draft_id      bigint,
  question_id   text REFERENCES questions(id) ON DELETE CASCADE,
  value         text NOT NULL DEFAULT '',
  expires_at    timestamptz NOT NULL
);

-- Draft media stores Telegram file identifiers. The files are downloaded,
-- validated and stripped of metadata only when the owner confirms creation.
CREATE TABLE telegram_drafts (
  id              bigserial PRIMARY KEY,
  chat_id         bigint NOT NULL,
  thread_id       bigint NOT NULL DEFAULT 0,
  project_id      text REFERENCES projects(id),
  repository_id   bigint REFERENCES repositories(id),
  text            text NOT NULL DEFAULT '',
  media           jsonb NOT NULL DEFAULT '[]',
  card_message_id bigint,
  source_message_id bigint NOT NULL,
  expires_at      timestamptz NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ON telegram_drafts (chat_id, thread_id, updated_at DESC);

CREATE TABLE telegram_chat_state (
  chat_id       bigint NOT NULL,
  thread_id     bigint NOT NULL DEFAULT 0,
  project_id    text NOT NULL REFERENCES projects(id),
  repository_id bigint REFERENCES repositories(id),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (chat_id, thread_id)
);

-- ForceReply prompts need durable routing after a service restart.
CREATE TABLE telegram_reply_prompts (
  chat_id       bigint NOT NULL,
  message_id    bigint NOT NULL,
  kind          text NOT NULL,
  draft_id      bigint REFERENCES telegram_drafts(id) ON DELETE CASCADE,
  task_id       bigint REFERENCES tasks(id) ON DELETE CASCADE,
  question_id   text REFERENCES questions(id) ON DELETE CASCADE,
  expires_at    timestamptz NOT NULL,
  PRIMARY KEY (chat_id, message_id)
);

ALTER TABLE telegram_callbacks
  ADD CONSTRAINT telegram_callbacks_draft_fk
  FOREIGN KEY (draft_id) REFERENCES telegram_drafts(id) ON DELETE CASCADE;

-- Backfill the status cards created by older versions.
INSERT INTO telegram_task_messages (chat_id, message_id, task_id, kind, has_buttons)
SELECT telegram_chat_id, telegram_message_id, id, 'card', true
FROM tasks
WHERE telegram_chat_id IS NOT NULL AND telegram_message_id IS NOT NULL
ON CONFLICT DO NOTHING;

-- +goose Down
DROP TABLE telegram_reply_prompts;
DROP TABLE telegram_chat_state;
DROP TABLE telegram_callbacks;
DROP TABLE telegram_drafts;
DROP TABLE telegram_task_messages;
