package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// TelegramTarget is where a task's Telegram messages go.
type TelegramTarget struct {
	ChatID    int64
	ThreadID  int64
	MessageID int64 // status message, 0 if not sent yet
}

func (s *Store) TaskTelegram(ctx context.Context, taskID int64) (TelegramTarget, error) {
	var t TelegramTarget
	var chat, thread, msg *int64
	err := s.Pool.QueryRow(ctx, `SELECT telegram_chat_id, telegram_thread_id, telegram_message_id FROM tasks WHERE id = $1`, taskID).
		Scan(&chat, &thread, &msg)
	if err != nil {
		return t, notFound(err)
	}
	t.ChatID, t.ThreadID, t.MessageID = deref(chat, 0), deref(thread, 0), deref(msg, 0)
	return t, nil
}

func (s *Store) SetTaskTelegram(ctx context.Context, taskID int64, t TelegramTarget) error {
	_, err := s.Pool.Exec(ctx, `UPDATE tasks SET telegram_chat_id = $2, telegram_thread_id = NULLIF($3, 0),
		telegram_message_id = NULLIF($4, 0) WHERE id = $1`, taskID, t.ChatID, t.ThreadID, t.MessageID)
	return err
}

// ProjectTelegram returns the forum chat and topic a project is bound to.
func (s *Store) ProjectTelegram(ctx context.Context, projectID string) (chatID, threadID int64, err error) {
	var chat, thread *int64
	err = s.Pool.QueryRow(ctx, `SELECT telegram_chat_id, telegram_thread_id FROM projects WHERE id = $1`, projectID).Scan(&chat, &thread)
	return deref(chat, 0), deref(thread, 0), notFound(err)
}

// BindProjectTopic links a forum topic to a project (and unlinks it from any other).
func (s *Store) BindProjectTopic(ctx context.Context, projectID string, chatID, threadID int64) error {
	_, err := s.Pool.Exec(ctx, `UPDATE projects SET telegram_chat_id = NULL, telegram_thread_id = NULL
		WHERE telegram_chat_id = $1 AND telegram_thread_id = $2`, chatID, threadID)
	if err != nil {
		return err
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE projects SET telegram_chat_id = $2, telegram_thread_id = $3, updated_at = now() WHERE id = $1`,
		projectID, chatID, threadID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ProjectByTopic finds the project bound to a forum topic.
func (s *Store) ProjectByTopic(ctx context.Context, chatID, threadID int64) (*Project, error) {
	var id string
	err := s.Pool.QueryRow(ctx, `SELECT id FROM projects WHERE telegram_chat_id = $1 AND telegram_thread_id = $2 AND NOT archived`,
		chatID, threadID).Scan(&id)
	if err != nil {
		return nil, notFound(err)
	}
	return s.GetProject(ctx, id)
}

type TelegramMessage struct {
	ChatID, MessageID, TaskID int64
	Kind                      string
	HasButtons                bool
}

func (s *Store) AddTelegramTaskMessage(ctx context.Context, m TelegramMessage) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO telegram_task_messages (chat_id, message_id, task_id, kind, has_buttons)
		VALUES ($1, $2, $3, $4, $5) ON CONFLICT (chat_id, message_id) DO UPDATE
		SET task_id = excluded.task_id, kind = excluded.kind, has_buttons = excluded.has_buttons`,
		m.ChatID, m.MessageID, m.TaskID, m.Kind, m.HasButtons)
	return err
}

func (s *Store) TelegramTaskByMessage(ctx context.Context, chatID, messageID int64) (int64, error) {
	var id int64
	err := s.Pool.QueryRow(ctx, `SELECT task_id FROM telegram_task_messages WHERE chat_id = $1 AND message_id = $2`, chatID, messageID).Scan(&id)
	return id, notFound(err)
}

// TelegramMessagesWithButtons returns non-card task messages whose buttons
// must be removed when a new run starts or task state changes.
func (s *Store) TelegramMessagesWithButtons(ctx context.Context, taskID int64) ([]TelegramMessage, error) {
	rows, err := s.Pool.Query(ctx, `SELECT chat_id, message_id, task_id, kind, has_buttons
		FROM telegram_task_messages WHERE task_id = $1 AND has_buttons AND kind <> 'card'`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TelegramMessage
	for rows.Next() {
		var m TelegramMessage
		if err := rows.Scan(&m.ChatID, &m.MessageID, &m.TaskID, &m.Kind, &m.HasButtons); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) ClearTelegramMessageButtons(ctx context.Context, chatID, messageID int64) error {
	_, err := s.Pool.Exec(ctx, `UPDATE telegram_task_messages SET has_buttons = false WHERE chat_id = $1 AND message_id = $2`, chatID, messageID)
	return err
}

type TelegramCallback struct {
	ID, Action, QuestionID, Value string
	TaskID, DraftID               int64
}

func (s *Store) AddTelegramCallback(ctx context.Context, c TelegramCallback, expires time.Time) error {
	_, _ = s.Pool.Exec(ctx, `DELETE FROM telegram_callbacks WHERE expires_at <= now()`)
	_, err := s.Pool.Exec(ctx, `INSERT INTO telegram_callbacks (id, action, task_id, draft_id, question_id, value, expires_at)
		VALUES ($1, $2, NULLIF($3, 0), NULLIF($4, 0), NULLIF($5, ''), $6, $7)`,
		c.ID, c.Action, c.TaskID, c.DraftID, c.QuestionID, c.Value, expires)
	return err
}

func (s *Store) FindTelegramCallback(ctx context.Context, c TelegramCallback) (string, error) {
	var id string
	err := s.Pool.QueryRow(ctx, `SELECT id FROM telegram_callbacks
		WHERE action = $1 AND task_id IS NOT DISTINCT FROM NULLIF($2, 0)
		AND draft_id IS NOT DISTINCT FROM NULLIF($3, 0)
		AND question_id IS NOT DISTINCT FROM NULLIF($4, '')
		AND value = $5 AND expires_at > now() ORDER BY expires_at DESC LIMIT 1`,
		c.Action, c.TaskID, c.DraftID, c.QuestionID, c.Value).Scan(&id)
	return id, notFound(err)
}

func (s *Store) TelegramCallback(ctx context.Context, id string) (TelegramCallback, error) {
	var c TelegramCallback
	var taskID, draftID *int64
	var questionID *string
	err := s.Pool.QueryRow(ctx, `SELECT id, action, task_id, draft_id, question_id, value
		FROM telegram_callbacks WHERE id = $1 AND expires_at > now()`, id).
		Scan(&c.ID, &c.Action, &taskID, &draftID, &questionID, &c.Value)
	c.TaskID, c.DraftID = deref(taskID, 0), deref(draftID, 0)
	c.QuestionID = deref(questionID, "")
	return c, notFound(err)
}

type TelegramDraftMedia struct {
	FileID string `json:"file_id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
}

type TelegramDraft struct {
	ID, ChatID, ThreadID, RepositoryID, CardMessageID, SourceMessageID int64
	ProjectID, Text                                                    string
	Media                                                              []TelegramDraftMedia
	ExpiresAt, CreatedAt, UpdatedAt                                    time.Time
}

const draftCols = `id, chat_id, thread_id, project_id, repository_id, text, media,
	card_message_id, source_message_id, expires_at, created_at, updated_at`

func scanTelegramDraft(row interface{ Scan(...any) error }) (*TelegramDraft, error) {
	var d TelegramDraft
	var project *string
	var repo, card *int64
	var raw []byte
	if err := row.Scan(&d.ID, &d.ChatID, &d.ThreadID, &project, &repo, &d.Text, &raw,
		&card, &d.SourceMessageID, &d.ExpiresAt, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return nil, err
	}
	d.ProjectID, d.RepositoryID, d.CardMessageID = deref(project, ""), deref(repo, 0), deref(card, 0)
	if err := json.Unmarshal(raw, &d.Media); err != nil {
		return nil, err
	}
	return &d, nil
}

// MergeTelegramDraft appends a message to a draft updated within mergeAfter,
// or creates a new durable draft. Text messages are separated by blank lines.
func (s *Store) MergeTelegramDraft(ctx context.Context, chatID, threadID, sourceMessageID int64, projectID string, repositoryID int64, text string, media []TelegramDraftMedia, mergeAfter time.Time) (*TelegramDraft, error) {
	var out *TelegramDraft
	err := s.Tx(ctx, func(tx pgx.Tx) error {
		d, err := scanTelegramDraft(tx.QueryRow(ctx, `SELECT `+draftCols+` FROM telegram_drafts
			WHERE chat_id = $1 AND thread_id = $2 AND expires_at > now() AND updated_at >= $3
			ORDER BY updated_at DESC LIMIT 1 FOR UPDATE`, chatID, threadID, mergeAfter))
		if err != nil && err != pgx.ErrNoRows {
			return err
		}
		if err == pgx.ErrNoRows {
			b, _ := json.Marshal(media)
			out, err = scanTelegramDraft(tx.QueryRow(ctx, `INSERT INTO telegram_drafts
				(chat_id, thread_id, project_id, repository_id, text, media, source_message_id, expires_at)
				VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, 0), $5, $6, $7, now() + interval '24 hours')
				RETURNING `+draftCols, chatID, threadID, projectID, repositoryID, text, b, sourceMessageID))
			return err
		}
		if text != "" {
			if d.Text != "" {
				d.Text += "\n\n"
			}
			d.Text += text
		}
		d.Media = append(d.Media, media...)
		b, _ := json.Marshal(d.Media)
		out, err = scanTelegramDraft(tx.QueryRow(ctx, `UPDATE telegram_drafts SET text = $2, media = $3,
			project_id = COALESCE(project_id, NULLIF($4, '')), repository_id = COALESCE(repository_id, NULLIF($5, 0)),
			updated_at = now(), expires_at = now() + interval '24 hours' WHERE id = $1 RETURNING `+draftCols,
			d.ID, d.Text, b, projectID, repositoryID))
		return err
	})
	return out, err
}

func (s *Store) TelegramDraft(ctx context.Context, id int64) (*TelegramDraft, error) {
	d, err := scanTelegramDraft(s.Pool.QueryRow(ctx, `SELECT `+draftCols+` FROM telegram_drafts WHERE id = $1 AND expires_at > now()`, id))
	return d, notFound(err)
}

func (s *Store) SetTelegramDraftCard(ctx context.Context, id, messageID int64) error {
	_, err := s.Pool.Exec(ctx, `UPDATE telegram_drafts SET card_message_id = $2 WHERE id = $1`, id, messageID)
	return err
}

func (s *Store) SetTelegramDraftText(ctx context.Context, id int64, text string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE telegram_drafts SET text = $2, updated_at = now() WHERE id = $1`, id, text)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) SetTelegramDraftProject(ctx context.Context, id int64, projectID string, repositoryID int64) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE telegram_drafts SET project_id = $2, repository_id = NULLIF($3, 0), updated_at = now() WHERE id = $1`, id, projectID, repositoryID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) DeleteTelegramDraft(ctx context.Context, id int64) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM telegram_drafts WHERE id = $1`, id)
	return err
}

func (s *Store) TelegramChatProject(ctx context.Context, chatID, threadID int64) (string, int64, error) {
	var project string
	var repo *int64
	err := s.Pool.QueryRow(ctx, `SELECT project_id, repository_id FROM telegram_chat_state WHERE chat_id = $1 AND thread_id = $2`, chatID, threadID).Scan(&project, &repo)
	return project, deref(repo, 0), notFound(err)
}

func (s *Store) SetTelegramChatProject(ctx context.Context, chatID, threadID int64, projectID string, repositoryID int64) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO telegram_chat_state (chat_id, thread_id, project_id, repository_id)
		VALUES ($1, $2, $3, NULLIF($4, 0)) ON CONFLICT (chat_id, thread_id) DO UPDATE
		SET project_id = excluded.project_id, repository_id = excluded.repository_id, updated_at = now()`, chatID, threadID, projectID, repositoryID)
	return err
}

type TelegramReplyPrompt struct {
	Kind, QuestionID string
	DraftID, TaskID  int64
}

func (s *Store) AddTelegramReplyPrompt(ctx context.Context, chatID, messageID int64, p TelegramReplyPrompt) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO telegram_reply_prompts
		(chat_id, message_id, kind, draft_id, task_id, question_id, expires_at)
		VALUES ($1, $2, $3, NULLIF($4, 0), NULLIF($5, 0), NULLIF($6, ''), now() + interval '24 hours')
		ON CONFLICT (chat_id, message_id) DO UPDATE SET kind = excluded.kind, draft_id = excluded.draft_id,
		task_id = excluded.task_id, question_id = excluded.question_id, expires_at = excluded.expires_at`,
		chatID, messageID, p.Kind, p.DraftID, p.TaskID, p.QuestionID)
	return err
}

func (s *Store) TelegramReplyPrompt(ctx context.Context, chatID, messageID int64) (TelegramReplyPrompt, error) {
	var p TelegramReplyPrompt
	var draft, task *int64
	var q *string
	err := s.Pool.QueryRow(ctx, `SELECT kind, draft_id, task_id, question_id FROM telegram_reply_prompts
		WHERE chat_id = $1 AND message_id = $2 AND expires_at > now()`, chatID, messageID).Scan(&p.Kind, &draft, &task, &q)
	p.DraftID, p.TaskID, p.QuestionID = deref(draft, 0), deref(task, 0), deref(q, "")
	return p, notFound(err)
}
