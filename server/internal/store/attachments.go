package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Attachment is an image attached to a task or a message. Its JSON form is
// what timeline events carry.
type Attachment struct {
	ID        int64      `json:"id"`
	TaskID    int64      `json:"-"`
	EventID   *int64     `json:"-"`
	RunID     *int64     `json:"-"`
	Name      string     `json:"name"`
	Mime      string     `json:"mime"`
	Size      int64      `json:"size"`
	Width     int        `json:"width"`
	Height    int        `json:"height"`
	CreatedAt time.Time  `json:"-"`
	DeletedAt *time.Time `json:"-"`
}

const attachmentCols = `id, task_id, event_id, run_id, original_name, mime, size_bytes, width, height, created_at, deleted_at`

func scanAttachment(row interface{ Scan(...any) error }) (*Attachment, error) {
	var a Attachment
	if err := row.Scan(&a.ID, &a.TaskID, &a.EventID, &a.RunID, &a.Name, &a.Mime, &a.Size, &a.Width, &a.Height, &a.CreatedAt, &a.DeletedAt); err != nil {
		return nil, err
	}
	return &a, nil
}

func collectAttachments(rows pgx.Rows) ([]*Attachment, error) {
	defer rows.Close()
	out := []*Attachment{}
	for rows.Next() {
		a, err := scanAttachment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetAttachment returns an attachment of a task, deleted or not.
func (s *Store) GetAttachment(ctx context.Context, taskID, id int64) (*Attachment, error) {
	a, err := scanAttachment(s.Pool.QueryRow(ctx, `SELECT `+attachmentCols+` FROM attachments WHERE id = $1 AND task_id = $2`, id, taskID))
	return a, notFound(err)
}

// LiveAttachments are a task's attachments that are not deleted, oldest first.
func (s *Store) LiveAttachments(ctx context.Context, taskID int64) ([]*Attachment, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+attachmentCols+` FROM attachments WHERE task_id = $1 AND deleted_at IS NULL ORDER BY id`, taskID)
	if err != nil {
		return nil, err
	}
	return collectAttachments(rows)
}

// AttachmentsForRun returns the attachments a starting run receives (those
// linked to it when it was queued; for a retry, the attachments of the task's
// creation again, which are relinked to it) and the task's other live
// attachments that earlier runs received. Queued messages' attachments that
// no run has received yet are in neither list. Both lists are oldest first.
func (s *Store) AttachmentsForRun(ctx context.Context, taskID, runID int64, retry bool) (current, earlier []*Attachment, err error) {
	err = s.Tx(ctx, func(tx pgx.Tx) error {
		if retry {
			if _, err := tx.Exec(ctx, `UPDATE attachments SET run_id = $2 WHERE task_id = $1 AND deleted_at IS NULL
				AND event_id IN (SELECT id FROM events WHERE task_id = $1 AND type = 'created')`, taskID, runID); err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, `SELECT `+attachmentCols+` FROM attachments
			WHERE task_id = $1 AND deleted_at IS NULL AND run_id IS NOT NULL ORDER BY id`, taskID)
		if err != nil {
			return err
		}
		all, err := collectAttachments(rows)
		if err != nil {
			return err
		}
		current, earlier = []*Attachment{}, []*Attachment{}
		for _, a := range all {
			if *a.RunID == runID {
				current = append(current, a)
			} else {
				earlier = append(earlier, a)
			}
		}
		return nil
	})
	return current, earlier, err
}

// DeleteAttachments marks all of a task's attachments deleted (the files are
// removed by the caller or the sweep). Returns how many were live.
func (s *Store) DeleteAttachments(ctx context.Context, taskID int64) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `UPDATE attachments SET deleted_at = now() WHERE task_id = $1 AND deleted_at IS NULL`, taskID)
	return tag.RowsAffected(), err
}
