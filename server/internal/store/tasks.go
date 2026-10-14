package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

type Task struct {
	ID           int64      `json:"id"`
	ProjectID    string     `json:"project_id"`
	RepositoryID int64      `json:"repository_id"`
	Title        string     `json:"title"`
	Prompt       string     `json:"prompt"`
	Status       string     `json:"status"`
	StatusReason *string    `json:"status_reason"`
	Branch       *string    `json:"branch"`
	SessionID    *string    `json:"session_id"`
	PRNumber     *int       `json:"pr_number"`
	PRURL        *string    `json:"pr_url"`
	CIStatus     *string    `json:"ci_status"`
	CostUSDTotal float64    `json:"cost_usd_total"`
	Source       string     `json:"source"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	FinishedAt   *time.Time `json:"finished_at"`
	PausedAt     *time.Time `json:"paused_at"`
	CleanedAt    *time.Time `json:"-"`
}

const taskCols = `id, project_id, repository_id, title, prompt, status, status_reason, branch, session_id,
	pr_number, pr_url, ci_status, cost_usd_total::float8, source, created_at, updated_at, finished_at, paused_at, cleaned_at`

func scanTask(row interface{ Scan(...any) error }) (*Task, error) {
	var t Task
	err := row.Scan(&t.ID, &t.ProjectID, &t.RepositoryID, &t.Title, &t.Prompt, &t.Status, &t.StatusReason, &t.Branch,
		&t.SessionID, &t.PRNumber, &t.PRURL, &t.CIStatus, &t.CostUSDTotal, &t.Source, &t.CreatedAt, &t.UpdatedAt,
		&t.FinishedAt, &t.PausedAt, &t.CleanedAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// CreateTask inserts a queued task, its initial pending run, and a status event.
func (s *Store) CreateTask(ctx context.Context, t Task) (*Task, error) {
	var out *Task
	err := s.Tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = scanTask(tx.QueryRow(ctx, `INSERT INTO tasks (project_id, repository_id, title, prompt, status, source)
			VALUES ($1, $2, $3, $4, 'queued', $5) RETURNING `+taskCols, t.ProjectID, t.RepositoryID, t.Title, t.Prompt, t.Source))
		if err != nil {
			return err
		}
		if _, err := insertRun(ctx, tx, out.ID, "initial", t.Prompt); err != nil {
			return err
		}
		_, err = insertBackendEvent(ctx, tx, out.ID, nil, "status", map[string]any{"status": "queued"})
		return err
	})
	if isFKViolation(err) {
		return nil, ErrNotFound
	}
	return out, err
}

func (s *Store) GetTask(ctx context.Context, id int64) (*Task, error) {
	t, err := scanTask(s.Pool.QueryRow(ctx, `SELECT `+taskCols+` FROM tasks WHERE id = $1`, id))
	return t, notFound(err)
}

type TaskFilter struct {
	ProjectID string
	Statuses  []string
	Cursor    int64 // id; list is newest first
	Limit     int
}

func (s *Store) ListTasks(ctx context.Context, f TaskFilter) ([]*Task, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+taskCols+` FROM tasks
		WHERE ($1 = '' OR project_id = $1) AND (coalesce(cardinality($2::text[]), 0) = 0 OR status = ANY($2))
		AND ($3 = 0 OR id < $3) ORDER BY id DESC LIMIT $4`, f.ProjectID, f.Statuses, f.Cursor, f.Limit)
	if err != nil {
		return nil, err
	}
	return collectTasks(rows)
}

func (s *Store) TasksByStatus(ctx context.Context, statuses ...string) ([]*Task, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+taskCols+` FROM tasks WHERE status = ANY($1) ORDER BY id`, statuses)
	if err != nil {
		return nil, err
	}
	return collectTasks(rows)
}

func (s *Store) TaskByPR(ctx context.Context, repoFullName string, prNumber int) (*Task, error) {
	t, err := scanTask(s.Pool.QueryRow(ctx, `SELECT `+prefixed("t.", taskCols)+` FROM tasks t
		JOIN repositories r ON r.id = t.repository_id
		WHERE lower(r.full_name) = lower($1) AND t.pr_number = $2 ORDER BY t.id DESC LIMIT 1`, repoFullName, prNumber))
	return t, notFound(err)
}

func (s *Store) TaskByBranch(ctx context.Context, repoFullName, branch string) (*Task, error) {
	t, err := scanTask(s.Pool.QueryRow(ctx, `SELECT `+prefixed("t.", taskCols)+` FROM tasks t
		JOIN repositories r ON r.id = t.repository_id
		WHERE lower(r.full_name) = lower($1) AND t.branch = $2 ORDER BY t.id DESC LIMIT 1`, repoFullName, branch))
	return t, notFound(err)
}

func collectTasks(rows pgx.Rows) ([]*Task, error) {
	defer rows.Close()
	out := []*Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetStatus moves a task to `to` only if it is currently in one of `from`
// (empty = any), records a status event, and returns the updated task.
// Returns ErrConflict when the task is not in an allowed state.
func (s *Store) SetStatus(ctx context.Context, id int64, from []string, to string, reason string, terminal bool) (*Task, error) {
	var out *Task
	err := s.Tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = setStatusTx(ctx, tx, id, from, to, reason, terminal)
		return err
	})
	return out, err
}

func setStatusTx(ctx context.Context, tx pgx.Tx, id int64, from []string, to, reason string, terminal bool) (*Task, error) {
	t, err := scanTask(tx.QueryRow(ctx, `UPDATE tasks SET status = $2, status_reason = NULLIF($3, ''), updated_at = now(),
			finished_at = CASE WHEN $4 THEN now() ELSE NULL END,
			paused_at = CASE WHEN $2 = 'paused' THEN now() ELSE NULL END
		WHERE id = $1 AND (coalesce(cardinality($5::text[]), 0) = 0 OR status = ANY($5)) RETURNING `+taskCols,
		id, to, reason, terminal, from))
	if err == pgx.ErrNoRows {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tasks WHERE id = $1)`, id).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, ErrNotFound
		}
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}
	payload := map[string]any{"status": to}
	if reason != "" {
		payload["reason"] = reason
	}
	_, err = insertBackendEvent(ctx, tx, id, nil, "status", payload)
	return t, err
}

// Requeue queues a new pending run and moves the task to queued, atomically.
func (s *Store) Requeue(ctx context.Context, id int64, from []string, kind, input string) (*Task, error) {
	var out *Task
	err := s.Tx(ctx, func(tx pgx.Tx) error {
		var err error
		if out, err = setStatusTx(ctx, tx, id, from, "queued", "", false); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM runs WHERE task_id = $1 AND started_at IS NULL`, id); err != nil {
			return err
		}
		_, err = insertRun(ctx, tx, id, kind, input)
		return err
	})
	return out, err
}

func (s *Store) SetBranch(ctx context.Context, id int64, branch string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE tasks SET branch = $2, updated_at = now() WHERE id = $1`, id, branch)
	return err
}

func (s *Store) SetSessionID(ctx context.Context, id int64, sessionID string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE tasks SET session_id = $2, updated_at = now() WHERE id = $1`, id, sessionID)
	return err
}

func (s *Store) SetPR(ctx context.Context, id int64, number int, url string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE tasks SET pr_number = $2, pr_url = $3, updated_at = now() WHERE id = $1`, id, number, url)
	return err
}

func (s *Store) SetCIStatus(ctx context.Context, id int64, status string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE tasks SET ci_status = $2, updated_at = now() WHERE id = $1`, id, status)
	return err
}

func (s *Store) MarkCleaned(ctx context.Context, id int64) error {
	_, err := s.Pool.Exec(ctx, `UPDATE tasks SET cleaned_at = now() WHERE id = $1`, id)
	return err
}

// ClaimNext moves the oldest queued task to preparing when fewer than max
// tasks are active. Returns nil when nothing was claimed.
func (s *Store) ClaimNext(ctx context.Context, max int) (*Task, error) {
	var out *Task
	err := s.Tx(ctx, func(tx pgx.Tx) error {
		// Serialise claimers so the active count cannot race.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7413001)`); err != nil {
			return err
		}
		var active int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE status IN ('preparing', 'running', 'waiting_input', 'pushing')`).Scan(&active); err != nil {
			return err
		}
		if active >= max {
			return nil
		}
		var id int64
		err := tx.QueryRow(ctx, `SELECT id FROM tasks WHERE status = 'queued' ORDER BY updated_at, id LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&id)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		out, err = setStatusTx(ctx, tx, id, []string{"queued"}, "preparing", "", false)
		return err
	})
	return out, err
}

func prefixed(p, cols string) string {
	out := []byte{}
	start := true
	for i := 0; i < len(cols); i++ {
		c := cols[i]
		if start && c != ' ' && c != '\n' && c != '\t' {
			out = append(out, p...)
			start = false
		}
		out = append(out, c)
		if c == ',' {
			start = true
		}
	}
	return string(out)
}

// ---- runs ----

type Run struct {
	ID          int64      `json:"id"`
	TaskID      int64      `json:"task_id"`
	Kind        string     `json:"kind"`
	Input       string     `json:"input"`
	ContainerID *string    `json:"container_id"`
	ExitCode    *int       `json:"exit_code"`
	LastSeq     int64      `json:"last_seq"`
	CostUSD     float64    `json:"cost_usd"`
	StartedAt   *time.Time `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
}

const runCols = `id, task_id, kind, input, container_id, exit_code, last_seq, cost_usd::float8, started_at, finished_at`

func scanRun(row interface{ Scan(...any) error }) (*Run, error) {
	var r Run
	if err := row.Scan(&r.ID, &r.TaskID, &r.Kind, &r.Input, &r.ContainerID, &r.ExitCode, &r.LastSeq, &r.CostUSD, &r.StartedAt, &r.FinishedAt); err != nil {
		return nil, err
	}
	return &r, nil
}

func insertRun(ctx context.Context, tx q, taskID int64, kind, input string) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `INSERT INTO runs (task_id, kind, input) VALUES ($1, $2, $3) RETURNING id`, taskID, kind, input).Scan(&id)
	return id, err
}

// PendingRun is the not-yet-started run of a task, if any.
func (s *Store) PendingRun(ctx context.Context, taskID int64) (*Run, error) {
	r, err := scanRun(s.Pool.QueryRow(ctx, `SELECT `+runCols+` FROM runs WHERE task_id = $1 AND started_at IS NULL ORDER BY id DESC LIMIT 1`, taskID))
	return r, notFound(err)
}

func (s *Store) LatestRun(ctx context.Context, taskID int64) (*Run, error) {
	r, err := scanRun(s.Pool.QueryRow(ctx, `SELECT `+runCols+` FROM runs WHERE task_id = $1 AND started_at IS NOT NULL ORDER BY id DESC LIMIT 1`, taskID))
	return r, notFound(err)
}

func (s *Store) GetRun(ctx context.Context, id int64) (*Run, error) {
	r, err := scanRun(s.Pool.QueryRow(ctx, `SELECT `+runCols+` FROM runs WHERE id = $1`, id))
	return r, notFound(err)
}

func (s *Store) StartRun(ctx context.Context, id int64, containerID string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE runs SET container_id = $2, started_at = now() WHERE id = $1`, id, containerID)
	return err
}

// MarkRunStarting stamps started_at before the container exists so the run
// is no longer "pending".
func (s *Store) MarkRunStarting(ctx context.Context, id int64) error {
	_, err := s.Pool.Exec(ctx, `UPDATE runs SET started_at = now() WHERE id = $1`, id)
	return err
}

func (s *Store) SetRunContainer(ctx context.Context, id int64, containerID string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE runs SET container_id = $2 WHERE id = $1`, id, containerID)
	return err
}

// FinishRun records the exit code once; later calls are no-ops.
func (s *Store) FinishRun(ctx context.Context, id int64, exitCode int) (bool, error) {
	tag, err := s.Pool.Exec(ctx, `UPDATE runs SET exit_code = $2, finished_at = now() WHERE id = $1 AND finished_at IS NULL`, id, exitCode)
	return err == nil && tag.RowsAffected() == 1, err
}

// SetRunCost records a run's cumulative cost and recomputes the task total.
func (s *Store) SetRunCost(ctx context.Context, runID int64, cost float64) error {
	_, err := s.Pool.Exec(ctx, `WITH r AS (UPDATE runs SET cost_usd = $2 WHERE id = $1 RETURNING task_id)
		UPDATE tasks SET cost_usd_total = (SELECT coalesce(sum(cost_usd), 0) FROM runs WHERE task_id = (SELECT task_id FROM r)
			AND id <> $1) + $2, updated_at = now()
		WHERE id = (SELECT task_id FROM r)`, runID, cost)
	return err
}

// UnfinishedRuns are runs that started but have no exit code (reconciliation).
func (s *Store) UnfinishedRuns(ctx context.Context) ([]*Run, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+runCols+` FROM runs WHERE started_at IS NOT NULL AND finished_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---- events ----

type Event struct {
	ID        int64           `json:"id"`
	TaskID    int64           `json:"task_id"`
	RunID     *int64          `json:"run_id"`
	Seq       *int64          `json:"seq"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

func insertBackendEvent(ctx context.Context, tx q, taskID int64, runID *int64, typ string, payload any) (*Event, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	ev := Event{TaskID: taskID, RunID: runID, Type: typ, Payload: b}
	err = tx.QueryRow(ctx, `INSERT INTO events (task_id, run_id, type, payload) VALUES ($1, $2, $3, $4) RETURNING id, created_at`,
		taskID, runID, typ, b).Scan(&ev.ID, &ev.CreatedAt)
	return &ev, err
}

// AddBackendEvent records a backend-generated event (seq NULL).
func (s *Store) AddBackendEvent(ctx context.Context, taskID int64, runID *int64, typ string, payload any) (*Event, error) {
	return insertBackendEvent(ctx, s.Pool, taskID, runID, typ, payload)
}

// AddRunnerEvent persists a runner event and advances runs.last_seq. Returns
// nil (no error) when this (run, seq) was already stored.
func (s *Store) AddRunnerEvent(ctx context.Context, taskID, runID, seq int64, typ string, raw json.RawMessage) (*Event, error) {
	var ev *Event
	err := s.Tx(ctx, func(tx pgx.Tx) error {
		e := Event{TaskID: taskID, RunID: &runID, Seq: &seq, Type: typ, Payload: raw}
		err := tx.QueryRow(ctx, `INSERT INTO events (task_id, run_id, seq, type, payload) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (run_id, seq) DO NOTHING RETURNING id, created_at`, taskID, runID, seq, typ, raw).Scan(&e.ID, &e.CreatedAt)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		ev = &e
		_, err = tx.Exec(ctx, `UPDATE runs SET last_seq = GREATEST(last_seq, $2) WHERE id = $1`, runID, seq)
		return err
	})
	return ev, err
}

// MaxSeq is the highest runner seq persisted for a task (seq continues across runs).
func (s *Store) MaxSeq(ctx context.Context, taskID int64) (int64, error) {
	var n int64
	err := s.Pool.QueryRow(ctx, `SELECT coalesce(max(last_seq), 0) FROM runs WHERE task_id = $1`, taskID).Scan(&n)
	return n, err
}

func (s *Store) ListEvents(ctx context.Context, taskID, afterID int64, limit int) ([]*Event, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, task_id, run_id, seq, type, payload, created_at FROM events
		WHERE task_id = $1 AND id > $2 AND type <> 'heartbeat' ORDER BY id LIMIT $3`, taskID, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.TaskID, &e.RunID, &e.Seq, &e.Type, &e.Payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

// LastEvents returns the newest n events of the given types, oldest first.
func (s *Store) LastEvents(ctx context.Context, taskID int64, types []string, n int) ([]*Event, error) {
	rows, err := s.Pool.Query(ctx, `SELECT * FROM (SELECT id, task_id, run_id, seq, type, payload, created_at FROM events
		WHERE task_id = $1 AND type = ANY($2) ORDER BY id DESC LIMIT $3) e ORDER BY id`, taskID, types, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.TaskID, &e.RunID, &e.Seq, &e.Type, &e.Payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

// SessionBaselineCost is the cost already reported by earlier runs of the
// same Claude session: runs since the last fresh (initial/retry) run. The
// runner reports cumulative session cost, so a resumed run's own cost is
// its reported total minus this baseline.
func (s *Store) SessionBaselineCost(ctx context.Context, taskID, runID int64) (float64, error) {
	var c float64
	err := s.Pool.QueryRow(ctx, `SELECT coalesce(sum(cost_usd), 0)::float8 FROM runs
		WHERE task_id = $1 AND id < $2 AND started_at IS NOT NULL
		AND id >= coalesce((SELECT max(id) FROM runs WHERE task_id = $1 AND id < $2 AND kind IN ('initial', 'retry')), 0)`,
		taskID, runID).Scan(&c)
	return c, err
}

// ResetRunStart makes a run pending again (it never got a container).
func (s *Store) ResetRunStart(ctx context.Context, runID int64) error {
	_, err := s.Pool.Exec(ctx, `UPDATE runs SET started_at = NULL, container_id = NULL WHERE id = $1 AND finished_at IS NULL`, runID)
	return err
}

// PreviousRun is the last started run before runID.
func (s *Store) PreviousRun(ctx context.Context, taskID, runID int64) (*Run, error) {
	r, err := scanRun(s.Pool.QueryRow(ctx, `SELECT `+runCols+` FROM runs WHERE task_id = $1 AND id < $2 AND started_at IS NOT NULL ORDER BY id DESC LIMIT 1`, taskID, runID))
	return r, notFound(err)
}
