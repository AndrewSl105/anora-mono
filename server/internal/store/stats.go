package store

import (
	"context"
	"time"
)

type Stats struct {
	Running      int     `json:"running"`
	Queued       int     `json:"queued"`
	WaitingInput int     `json:"waiting_input"`
	CostToday    float64 `json:"cost_usd_today"`
	CostMonth    float64 `json:"cost_usd_month"`
}

// GetStats counts active work and sums run costs since local midnight and the
// first of the month (in loc).
func (s *Store) GetStats(ctx context.Context, now time.Time) (*Stats, error) {
	var st Stats
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	err := s.Pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM tasks WHERE status IN ('preparing', 'running', 'pushing')),
		(SELECT count(*) FROM tasks WHERE status = 'queued'),
		(SELECT count(*) FROM tasks WHERE status = 'waiting_input'),
		(SELECT coalesce(sum(cost_usd), 0)::float8 FROM runs WHERE started_at >= $1),
		(SELECT coalesce(sum(cost_usd), 0)::float8 FROM runs WHERE started_at >= $2)`, day, month).
		Scan(&st.Running, &st.Queued, &st.WaitingInput, &st.CostToday, &st.CostMonth)
	return &st, err
}

type StatusCount struct {
	Status string
	Count  int
}

func (s *Store) CountByStatus(ctx context.Context) ([]StatusCount, error) {
	rows, err := s.Pool.Query(ctx, `SELECT status, count(*) FROM tasks GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StatusCount
	for rows.Next() {
		var c StatusCount
		if err := rows.Scan(&c.Status, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type ProjectCost struct {
	ProjectID string
	Cost      float64
}

func (s *Store) CostByProject(ctx context.Context) ([]ProjectCost, error) {
	rows, err := s.Pool.Query(ctx, `SELECT project_id, sum(cost_usd_total)::float8 FROM tasks GROUP BY project_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProjectCost
	for rows.Next() {
		var c ProjectCost
		if err := rows.Scan(&c.ProjectID, &c.Cost); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---- retention ----

// DeleteOldEvents removes runner events of terminal tasks older than the
// retention; backend status events are kept so the timeline still makes sense.
func (s *Store) DeleteOldEvents(ctx context.Context, olderThan time.Time) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM events e USING tasks t
		WHERE e.task_id = t.id AND e.seq IS NOT NULL AND e.created_at < $1
		AND t.status IN ('completed', 'failed', 'cancelled')`, olderThan)
	return tag.RowsAffected(), err
}

// TasksToClean are terminal tasks finished before cutoff whose workspace is not yet removed.
func (s *Store) TasksToClean(ctx context.Context, cutoff time.Time) ([]*Task, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+taskCols+` FROM tasks
		WHERE status IN ('completed', 'failed', 'cancelled') AND finished_at < $1 AND cleaned_at IS NULL ORDER BY id`, cutoff)
	if err != nil {
		return nil, err
	}
	return collectTasks(rows)
}

// IdleTasks are tasks in one of statuses whose last change is before cutoff.
func (s *Store) IdleTasks(ctx context.Context, statuses []string, cutoff time.Time) ([]*Task, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+taskCols+` FROM tasks WHERE status = ANY($1) AND updated_at < $2 ORDER BY id`, statuses, cutoff)
	if err != nil {
		return nil, err
	}
	return collectTasks(rows)
}
