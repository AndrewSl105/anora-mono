package store

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

// NewAttachment is the metadata of a validated upload.
type NewAttachment struct {
	Name          string
	Mime          string
	Size          int64
	Width, Height int
}

// Upload is the attachments of one task creation or message.
type Upload struct {
	Items []NewAttachment
	// Place writes the files once their rows exist. It runs inside the
	// transaction, so a failure leaves no rows; files it already wrote are
	// orphans that the attachment sweep removes.
	Place func(taskID int64, rows []*Attachment) error
}

func (u *Upload) empty() bool { return u == nil || len(u.Items) == 0 }

// insertUploadTx inserts the rows of an upload for the event that carries
// them and places their files. runID is the run whose prompt carries them,
// nil for a queued message (linked when the queue is drained). A nil or
// empty upload inserts nothing.
func insertUploadTx(ctx context.Context, tx pgx.Tx, taskID, eventID int64, runID *int64, u *Upload) ([]*Attachment, error) {
	out := []*Attachment{}
	if u.empty() {
		return out, nil
	}
	for _, it := range u.Items {
		a, err := scanAttachment(tx.QueryRow(ctx, `INSERT INTO attachments
			(task_id, event_id, run_id, original_name, mime, size_bytes, width, height)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING `+attachmentCols,
			taskID, eventID, runID, it.Name, it.Mime, it.Size, it.Width, it.Height))
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	if u.Place != nil {
		if err := u.Place(taskID, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// insertEventWithUploadTx records a backend event whose payload lists the
// upload's attachments under "attachments" (omitted for a message without
// attachments, always present for `created`).
func insertEventWithUploadTx(ctx context.Context, tx pgx.Tx, taskID int64, runID *int64, typ string, payload map[string]any, u *Upload, always bool) (*Event, error) {
	ev, err := insertBackendEvent(ctx, tx, taskID, nil, typ, payload)
	if err != nil || (u.empty() && !always) {
		return ev, err
	}
	rows, err := insertUploadTx(ctx, tx, taskID, ev.ID, runID, u)
	if err != nil {
		return nil, err
	}
	payload["attachments"] = rows
	return updateEventPayload(ctx, tx, ev, payload)
}

func updateEventPayload(ctx context.Context, tx pgx.Tx, ev *Event, payload any) (*Event, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE events SET payload = $2 WHERE id = $1`, ev.ID, b); err != nil {
		return nil, err
	}
	ev.Payload = b
	return ev, nil
}

// linkQueuedAttachmentsTx hands the attachments of queued messages (not yet
// given to a run) to the run that the drained queue starts.
func linkQueuedAttachmentsTx(ctx context.Context, tx pgx.Tx, taskID, runID int64) error {
	_, err := tx.Exec(ctx, `UPDATE attachments SET run_id = $2 WHERE task_id = $1 AND run_id IS NULL AND deleted_at IS NULL
		AND event_id IN (SELECT id FROM events WHERE task_id = $1 AND type = 'message')`, taskID, runID)
	return err
}
