package store

import (
	"context"
	"time"
)

type AuthSession struct {
	ID        string    `json:"-"`
	Kind      string    `json:"kind"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Store) CreateSession(ctx context.Context, id, kind string, expires time.Time) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO auth_sessions (id, kind, expires_at) VALUES ($1, $2, $3)`, id, kind, expires)
	return err
}

// TouchSession returns a live session and slides its expiry.
func (s *Store) TouchSession(ctx context.Context, id string, ttl time.Duration) (*AuthSession, error) {
	var a AuthSession
	err := s.Pool.QueryRow(ctx, `UPDATE auth_sessions SET last_seen_at = now(), expires_at = now() + make_interval(secs => $2)
		WHERE id = $1 AND expires_at > now() RETURNING id, kind, expires_at, created_at`, id, ttl.Seconds()).
		Scan(&a.ID, &a.Kind, &a.ExpiresAt, &a.CreatedAt)
	return &a, notFound(err)
}

func (s *Store) DeleteSession(ctx context.Context, id string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM auth_sessions WHERE id = $1`, id)
	return err
}

func (s *Store) DeleteExpiredSessions(ctx context.Context) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM auth_sessions WHERE expires_at < now()`)
	return tag.RowsAffected(), err
}
