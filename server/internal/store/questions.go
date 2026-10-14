package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

type Question struct {
	ID                string     `json:"id"`
	TaskID            int64      `json:"task_id"`
	RunID             int64      `json:"run_id"`
	Text              string     `json:"text"`
	Options           []string   `json:"options"`
	Answer            *string    `json:"answer"`
	AnsweredVia       *string    `json:"answered_via"`
	TelegramMessageID *int64     `json:"-"`
	CreatedAt         time.Time  `json:"created_at"`
	AnsweredAt        *time.Time `json:"answered_at"`
}

const questionCols = `id, task_id, run_id, text, options, answer, answered_via, telegram_message_id, created_at, answered_at`

func scanQuestion(row interface{ Scan(...any) error }) (*Question, error) {
	var qn Question
	var opts []byte
	if err := row.Scan(&qn.ID, &qn.TaskID, &qn.RunID, &qn.Text, &opts, &qn.Answer, &qn.AnsweredVia, &qn.TelegramMessageID, &qn.CreatedAt, &qn.AnsweredAt); err != nil {
		return nil, err
	}
	if len(opts) > 0 {
		_ = json.Unmarshal(opts, &qn.Options)
	}
	return &qn, nil
}

func (s *Store) CreateQuestion(ctx context.Context, qn Question) (*Question, error) {
	var opts any
	if len(qn.Options) > 0 {
		opts, _ = json.Marshal(qn.Options)
	}
	out, err := scanQuestion(s.Pool.QueryRow(ctx, `INSERT INTO questions (id, task_id, run_id, text, options)
		VALUES ($1, $2, $3, $4, $5) ON CONFLICT (id) DO UPDATE SET text = EXCLUDED.text RETURNING `+questionCols,
		qn.ID, qn.TaskID, qn.RunID, qn.Text, opts))
	return out, err
}

func (s *Store) GetQuestion(ctx context.Context, id string) (*Question, error) {
	qn, err := scanQuestion(s.Pool.QueryRow(ctx, `SELECT `+questionCols+` FROM questions WHERE id = $1`, id))
	return qn, notFound(err)
}

// OpenQuestion is the newest unanswered question of a task.
func (s *Store) OpenQuestion(ctx context.Context, taskID int64) (*Question, error) {
	qn, err := scanQuestion(s.Pool.QueryRow(ctx, `SELECT `+questionCols+` FROM questions
		WHERE task_id = $1 AND answer IS NULL ORDER BY created_at DESC LIMIT 1`, taskID))
	return qn, notFound(err)
}

// AnswerQuestion stores the first answer; a second answer gets ErrConflict.
func (s *Store) AnswerQuestion(ctx context.Context, id string, taskID int64, answer, via string) (*Question, error) {
	qn, err := scanQuestion(s.Pool.QueryRow(ctx, `UPDATE questions SET answer = $3, answered_via = $4, answered_at = now()
		WHERE id = $1 AND task_id = $2 AND answer IS NULL RETURNING `+questionCols, id, taskID, answer, via))
	if err == pgx.ErrNoRows {
		if _, gerr := s.GetQuestion(ctx, id); gerr != nil {
			return nil, gerr
		}
		return nil, ErrConflict
	}
	return qn, err
}

// CloseOpenQuestions marks every unanswered question of a task as abandoned
// so it no longer shows as open (used when a run ends).
func (s *Store) CloseOpenQuestions(ctx context.Context, taskID int64, exceptID string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE questions SET answered_via = 'expired', answered_at = now(), answer = ''
		WHERE task_id = $1 AND answer IS NULL AND id <> $2`, taskID, exceptID)
	return err
}

func (s *Store) SetQuestionTelegramMessage(ctx context.Context, id string, msgID int64) error {
	_, err := s.Pool.Exec(ctx, `UPDATE questions SET telegram_message_id = $2 WHERE id = $1`, id, msgID)
	return err
}

func (s *Store) QuestionByTelegramMessage(ctx context.Context, msgID int64) (*Question, error) {
	qn, err := scanQuestion(s.Pool.QueryRow(ctx, `SELECT `+questionCols+` FROM questions WHERE telegram_message_id = $1`, msgID))
	return qn, notFound(err)
}

// AnsweredQuestionOfRun is the question of a run that was answered (used to
// replay the answer into the resumed run).
func (s *Store) AnsweredQuestionOfRun(ctx context.Context, runID int64) (*Question, error) {
	qn, err := scanQuestion(s.Pool.QueryRow(ctx, `SELECT `+questionCols+` FROM questions
		WHERE run_id = $1 AND answer IS NOT NULL AND answered_via <> 'expired' ORDER BY answered_at DESC LIMIT 1`, runID))
	return qn, notFound(err)
}
