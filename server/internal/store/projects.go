package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Project struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Instructions     string            `json:"instructions"`
	Services         []string          `json:"services"`
	Env              map[string]string `json:"env"`
	RunnerImage      *string           `json:"runner_image"`
	Model            *string           `json:"model"`
	MaxTurns         *int              `json:"max_turns"`
	MaxBudgetUSD     *float64          `json:"max_budget_usd"`
	MaxDurationSec   *int64            `json:"max_duration_sec"`
	RunnerMemory     *string           `json:"runner_memory"`
	RunnerCPUs       *float64          `json:"runner_cpus"`
	TelegramThreadID *int64            `json:"telegram_thread_id"`
	Archived         bool              `json:"archived"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
	Repositories     []Repository      `json:"repositories"`
}

type Repository struct {
	ID             int64  `json:"id"`
	ProjectID      string `json:"project_id"`
	FullName       string `json:"full_name"`
	BaseBranch     string `json:"base_branch"`
	InstallationID int64  `json:"installation_id"`
	IsDefault      bool   `json:"is_default"`
}

const projectCols = `id, name, instructions, services, env, runner_image, model, max_turns,
	max_budget_usd::float8, EXTRACT(EPOCH FROM max_duration)::bigint, runner_memory, runner_cpus::float8,
	telegram_thread_id, archived, created_at, updated_at`

func scanProject(row interface{ Scan(...any) error }) (*Project, error) {
	var p Project
	var env []byte
	err := row.Scan(&p.ID, &p.Name, &p.Instructions, &p.Services, &env, &p.RunnerImage, &p.Model, &p.MaxTurns,
		&p.MaxBudgetUSD, &p.MaxDurationSec, &p.RunnerMemory, &p.RunnerCPUs,
		&p.TelegramThreadID, &p.Archived, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	p.Env = map[string]string{}
	if len(env) > 0 {
		if err := json.Unmarshal(env, &p.Env); err != nil {
			return nil, fmt.Errorf("project %s env: %w", p.ID, err)
		}
	}
	if p.Services == nil {
		p.Services = []string{}
	}
	p.Repositories = []Repository{}
	return &p, nil
}

// ProjectFields is the writable part of a project. Nil pointers in an update
// mean "leave unchanged"; see UpdateProject.
type ProjectFields struct {
	Name             *string            `json:"name"`
	Instructions     *string            `json:"instructions"`
	Services         *[]string          `json:"services"`
	Env              *map[string]string `json:"env"`
	RunnerImage      *string            `json:"runner_image"`
	Model            *string            `json:"model"`
	MaxTurns         *int               `json:"max_turns"`
	MaxBudgetUSD     *float64           `json:"max_budget_usd"`
	MaxDurationSec   *int64             `json:"max_duration_sec"`
	RunnerMemory     *string            `json:"runner_memory"`
	RunnerCPUs       *float64           `json:"runner_cpus"`
	TelegramThreadID *int64             `json:"telegram_thread_id"`
	Archived         *bool              `json:"archived"`
}

func (s *Store) CreateProject(ctx context.Context, id string, f ProjectFields) (*Project, error) {
	_, err := s.Pool.Exec(ctx, `INSERT INTO projects (id, name) VALUES ($1, $2)`, id, deref(f.Name, id))
	if isUniqueViolation(err) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}
	f.Name = nil
	return s.UpdateProject(ctx, id, f)
}

func (s *Store) UpdateProject(ctx context.Context, id string, f ProjectFields) (*Project, error) {
	sets := []string{"updated_at = now()"}
	args := []any{id}
	add := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	if f.Name != nil {
		add("name", *f.Name)
	}
	if f.Instructions != nil {
		add("instructions", *f.Instructions)
	}
	if f.Services != nil {
		svcs := *f.Services
		if svcs == nil {
			svcs = []string{}
		}
		add("services", svcs)
	}
	if f.Env != nil {
		b, _ := json.Marshal(*f.Env)
		add("env", b)
	}
	// Empty strings and zero numbers reset an override to the global default.
	if f.RunnerImage != nil {
		add("runner_image", nilIfEmpty(*f.RunnerImage))
	}
	if f.Model != nil {
		add("model", nilIfEmpty(*f.Model))
	}
	if f.MaxTurns != nil {
		add("max_turns", nilIfZero(*f.MaxTurns))
	}
	if f.MaxBudgetUSD != nil {
		add("max_budget_usd", nilIfZero(*f.MaxBudgetUSD))
	}
	if f.MaxDurationSec != nil {
		if *f.MaxDurationSec == 0 {
			add("max_duration", nil)
		} else {
			args = append(args, *f.MaxDurationSec)
			sets = append(sets, fmt.Sprintf("max_duration = make_interval(secs => $%d)", len(args)))
		}
	}
	if f.RunnerMemory != nil {
		add("runner_memory", nilIfEmpty(*f.RunnerMemory))
	}
	if f.RunnerCPUs != nil {
		add("runner_cpus", nilIfZero(*f.RunnerCPUs))
	}
	if f.TelegramThreadID != nil {
		add("telegram_thread_id", nilIfZero(*f.TelegramThreadID))
	}
	if f.Archived != nil {
		add("archived", *f.Archived)
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE projects SET `+strings.Join(sets, ", ")+` WHERE id = $1`, args...)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return s.GetProject(ctx, id)
}

func (s *Store) GetProject(ctx context.Context, id string) (*Project, error) {
	p, err := scanProject(s.Pool.QueryRow(ctx, `SELECT `+projectCols+` FROM projects WHERE id = $1`, id))
	if err != nil {
		return nil, notFound(err)
	}
	p.Repositories, err = s.ListRepositories(ctx, id)
	return p, err
}

// ListProjects returns projects ordered by id, after cursor.
func (s *Store) ListProjects(ctx context.Context, includeArchived bool, cursor string, limit int) ([]*Project, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+projectCols+` FROM projects
		WHERE ($1 OR NOT archived) AND id > $2 ORDER BY id LIMIT $3`, includeArchived, cursor, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, p := range out {
		if p.Repositories, err = s.ListRepositories(ctx, p.ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) ListRepositories(ctx context.Context, projectID string) ([]Repository, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, project_id, full_name, base_branch, installation_id, is_default
		FROM repositories WHERE project_id = $1 ORDER BY is_default DESC, id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Repository{}
	for rows.Next() {
		var r Repository
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.FullName, &r.BaseBranch, &r.InstallationID, &r.IsDefault); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetRepository(ctx context.Context, id int64) (*Repository, error) {
	var r Repository
	err := s.Pool.QueryRow(ctx, `SELECT id, project_id, full_name, base_branch, installation_id, is_default
		FROM repositories WHERE id = $1`, id).Scan(&r.ID, &r.ProjectID, &r.FullName, &r.BaseBranch, &r.InstallationID, &r.IsDefault)
	if err != nil {
		return nil, notFound(err)
	}
	return &r, nil
}

// AddRepository adds a repo; the first repo of a project, or one with
// IsDefault set, becomes the default.
func (s *Store) AddRepository(ctx context.Context, r Repository) (*Repository, error) {
	err := s.Tx(ctx, func(tx pgxTx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM repositories WHERE project_id = $1`, r.ProjectID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			r.IsDefault = true
		}
		if r.IsDefault {
			if _, err := tx.Exec(ctx, `UPDATE repositories SET is_default = false WHERE project_id = $1`, r.ProjectID); err != nil {
				return err
			}
		}
		return tx.QueryRow(ctx, `INSERT INTO repositories (project_id, full_name, base_branch, installation_id, is_default)
			VALUES ($1, $2, $3, $4, $5) RETURNING id`, r.ProjectID, r.FullName, r.BaseBranch, r.InstallationID, r.IsDefault).Scan(&r.ID)
	})
	switch {
	case isUniqueViolation(err):
		return nil, ErrConflict
	case isFKViolation(err):
		return nil, ErrNotFound
	case err != nil:
		return nil, err
	}
	return &r, nil
}

// RepoUpdate holds the writable repository fields; nil means unchanged.
type RepoUpdate struct {
	BaseBranch *string
	IsDefault  *bool
}

// UpdateRepository changes a repository's base branch and default flag.
// Making a repo the default clears the flag on the others; un-defaulting the
// default repo is ErrConflict (a project always has one). Changing the base
// branch is ErrRepoInUse while unfinished tasks use the repo.
func (s *Store) UpdateRepository(ctx context.Context, projectID string, id int64, u RepoUpdate) (*Repository, error) {
	var r Repository
	err := s.Tx(ctx, func(tx pgxTx) error {
		err := tx.QueryRow(ctx, `SELECT id, project_id, full_name, base_branch, installation_id, is_default
			FROM repositories WHERE id = $1 AND project_id = $2 FOR UPDATE`, id, projectID).
			Scan(&r.ID, &r.ProjectID, &r.FullName, &r.BaseBranch, &r.InstallationID, &r.IsDefault)
		if err != nil {
			return notFound(err)
		}
		if u.BaseBranch != nil && *u.BaseBranch != r.BaseBranch {
			var busy bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tasks WHERE repository_id = $1
				AND status NOT IN ('completed', 'failed', 'cancelled'))`, id).Scan(&busy); err != nil {
				return err
			}
			if busy {
				return ErrRepoInUse
			}
			r.BaseBranch = *u.BaseBranch
		}
		if u.IsDefault != nil && *u.IsDefault != r.IsDefault {
			if !*u.IsDefault {
				return fmt.Errorf("%w: a project needs a default repository; make another one the default instead", ErrConflict)
			}
			if _, err := tx.Exec(ctx, `UPDATE repositories SET is_default = false WHERE project_id = $1`, projectID); err != nil {
				return err
			}
			r.IsDefault = true
		}
		_, err = tx.Exec(ctx, `UPDATE repositories SET base_branch = $2, is_default = $3 WHERE id = $1`, id, r.BaseBranch, r.IsDefault)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// DeleteRepository fails with ErrConflict while tasks reference the repo.
func (s *Store) DeleteRepository(ctx context.Context, projectID string, id int64) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM repositories WHERE id = $1 AND project_id = $2`, id, projectID)
	if isFKViolation(err) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func deref[T any](p *T, def T) T {
	if p == nil {
		return def
	}
	return *p
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nilIfZero[T int | int64 | float64](v T) any {
	if v == 0 {
		return nil
	}
	return v
}
