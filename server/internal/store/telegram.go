package store

import "context"

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
