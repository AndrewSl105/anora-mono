// Package api serves the REST API, the WebSocket and the GitHub webhook.
package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/andrewsl105/fleetpocket/server/internal/attachments"
	"github.com/andrewsl105/fleetpocket/server/internal/auth"
	"github.com/andrewsl105/fleetpocket/server/internal/config"
	"github.com/andrewsl105/fleetpocket/server/internal/events"
	"github.com/andrewsl105/fleetpocket/server/internal/projects"
	"github.com/andrewsl105/fleetpocket/server/internal/store"
	"github.com/andrewsl105/fleetpocket/server/internal/tasks"
)

var errGitHubDisabled = errors.New("GitHub App is not configured")

// GitHubRepo is one entry of the "add repo" picker.
type GitHubRepo struct {
	FullName       string `json:"full_name"`
	DefaultBranch  string `json:"default_branch"`
	Private        bool   `json:"private"`
	InstallationID int64  `json:"installation_id"`
}

// GitHub lists repositories the App can see. Nil when the App is not configured.
type GitHub interface {
	ListRepositories(ctx context.Context) ([]GitHubRepo, error)
}

// DiffFunc returns `git diff base...branch` for a task, capped by the caller.
type DiffFunc func(ctx context.Context, t *store.Task, repo *store.Repository, limit int) (diff string, truncated bool, err error)

type Server struct {
	Cfg      *config.Config
	Store    *store.Store
	Tasks    *tasks.Service
	Projects *projects.Service
	Auth     *auth.Manager
	Hub      *events.Hub
	GitHub   GitHub
	Diff     DiffFunc
	Webhook  http.Handler
	Metrics  http.Handler
	Health   func(ctx context.Context) map[string]error
	Limiter  *auth.FailLimiter
	Tickets  *auth.Tickets
	// Files serves attachments; nil disables the download endpoint.
	Files *attachments.Files
	Now   func() time.Time
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Server) Handler() http.Handler {
	if s.Limiter == nil {
		s.Limiter = auth.NewFailLimiter(5, 15*time.Minute)
	}
	if s.Tickets == nil {
		s.Tickets = auth.NewTickets()
	}
	mux := http.NewServeMux()
	a := s.requireSession

	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.HandleFunc("POST /api/v1/auth/telegram", s.loginTelegram)
	mux.HandleFunc("POST /api/v1/auth/logout", s.logout)
	mux.HandleFunc("GET /api/v1/me", a(s.me))

	mux.HandleFunc("GET /api/v1/projects", a(s.listProjects))
	mux.HandleFunc("POST /api/v1/projects", a(s.createProject))
	mux.HandleFunc("GET /api/v1/projects/{id}", a(s.getProject))
	mux.HandleFunc("PATCH /api/v1/projects/{id}", a(s.updateProject))
	mux.HandleFunc("DELETE /api/v1/projects/{id}", a(s.archiveProject))
	mux.HandleFunc("POST /api/v1/projects/{id}/repositories", a(s.addRepository))
	mux.HandleFunc("PATCH /api/v1/projects/{id}/repositories/{repoId}", a(s.updateRepository))
	mux.HandleFunc("DELETE /api/v1/projects/{id}/repositories/{repoId}", a(s.deleteRepository))
	mux.HandleFunc("GET /api/v1/github/repositories", a(s.githubRepositories))

	mux.HandleFunc("GET /api/v1/tasks", a(s.listTasks))
	mux.HandleFunc("POST /api/v1/tasks", a(s.createTask))
	mux.HandleFunc("GET /api/v1/tasks/{id}", a(s.getTask))
	mux.HandleFunc("GET /api/v1/tasks/{id}/events", a(s.taskEvents))
	mux.HandleFunc("GET /api/v1/tasks/{id}/runs", a(s.taskRuns))
	mux.HandleFunc("GET /api/v1/tasks/{id}/diff", a(s.taskDiff))
	mux.HandleFunc("GET /api/v1/tasks/{id}/attachments/{attachmentId}", a(s.attachment))
	mux.HandleFunc("POST /api/v1/tasks/{id}/answer", a(s.answer))
	mux.HandleFunc("POST /api/v1/tasks/{id}/message", a(s.message))
	mux.HandleFunc("POST /api/v1/tasks/{id}/interrupt", a(s.control(s.Tasks.Interrupt)))
	mux.HandleFunc("POST /api/v1/tasks/{id}/cancel", a(s.control(s.Tasks.Cancel)))
	mux.HandleFunc("POST /api/v1/tasks/{id}/retry", a(s.control(s.Tasks.Retry)))
	mux.HandleFunc("POST /api/v1/tasks/{id}/merge", a(s.control(s.Tasks.Merge)))
	mux.HandleFunc("GET /api/v1/stats", a(s.stats))
	mux.HandleFunc("POST /api/v1/ws/ticket", a(s.wsTicket))
	mux.HandleFunc("GET /api/v1/ws", s.requireSessionOrTicket(s.websocket))

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
	})

	if s.Webhook != nil {
		mux.Handle("POST /webhooks/github", s.Webhook)
	}
	mux.HandleFunc("GET /healthz", s.healthz)
	if s.Metrics != nil {
		mux.Handle("GET /metrics", internalOnly(s.Metrics))
	}

	// The webhook carries its own signature auth and must not need the CSRF header.
	api := s.cors(csrf(mux))
	root := http.NewServeMux()
	root.Handle("/webhooks/", mux)
	root.Handle("/", api)
	return logRequests(root)
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	checks := map[string]string{}
	ok := true
	if s.Health != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		for name, err := range s.Health(ctx) {
			if err != nil {
				ok = false
				checks[name] = err.Error()
			} else {
				checks[name] = "ok"
			}
		}
	}
	status := http.StatusOK
	if !ok {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, map[string]any{"ok": ok, "checks": checks})
}
