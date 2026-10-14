-- +goose Up
-- Turn counts per run and per task, and a one-line "what is it doing now"
-- for the task list, kept on the row so list views need no event queries.
ALTER TABLE runs ADD COLUMN turns int NOT NULL DEFAULT 0;
ALTER TABLE tasks ADD COLUMN turns int NOT NULL DEFAULT 0;
ALTER TABLE tasks ADD COLUMN last_activity text;
ALTER TABLE tasks ADD COLUMN last_activity_at timestamptz;

-- Backfill from the events that retention has kept.
UPDATE runs r SET turns = u.turns
FROM (SELECT DISTINCT ON (run_id) run_id, (payload->>'turns')::int AS turns
      FROM events WHERE type = 'usage' AND run_id IS NOT NULL AND payload ? 'turns'
      ORDER BY run_id, id DESC) u
WHERE u.run_id = r.id;

UPDATE tasks t SET turns = s.turns
FROM (SELECT task_id, sum(turns)::int AS turns FROM runs GROUP BY task_id) s
WHERE s.task_id = t.id;

UPDATE tasks t SET
  last_activity = left(CASE WHEN e.type = 'tool_use' THEN e.payload->>'summary'
                            WHEN e.payload ? 'reason' THEN (e.payload->>'status') || ': ' || (e.payload->>'reason')
                            ELSE e.payload->>'status' END, 120),
  last_activity_at = e.created_at
FROM (SELECT DISTINCT ON (task_id) task_id, type, payload, created_at
      FROM events WHERE type IN ('tool_use', 'status')
      ORDER BY task_id, id DESC) e
WHERE e.task_id = t.id;

-- +goose Down
ALTER TABLE tasks DROP COLUMN last_activity_at;
ALTER TABLE tasks DROP COLUMN last_activity;
ALTER TABLE tasks DROP COLUMN turns;
ALTER TABLE runs DROP COLUMN turns;
