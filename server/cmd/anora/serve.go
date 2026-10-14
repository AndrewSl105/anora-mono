package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/mymmrac/telego"

	"github.com/andrewsl105/anora-runner/internal/api"
	"github.com/andrewsl105/anora-runner/internal/auth"
	"github.com/andrewsl105/anora-runner/internal/config"
	"github.com/andrewsl105/anora-runner/internal/events"
	"github.com/andrewsl105/anora-runner/internal/githubapp"
	"github.com/andrewsl105/anora-runner/internal/maintenance"
	"github.com/andrewsl105/anora-runner/internal/metrics"
	"github.com/andrewsl105/anora-runner/internal/projects"
	"github.com/andrewsl105/anora-runner/internal/runnermgr"
	"github.com/andrewsl105/anora-runner/internal/scheduler"
	"github.com/andrewsl105/anora-runner/internal/store"
	"github.com/andrewsl105/anora-runner/internal/tasks"
	"github.com/andrewsl105/anora-runner/internal/telegram"
	"github.com/andrewsl105/anora-runner/internal/workspace"
)

func runServe() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	ctx, cancel := signalContext()
	defer cancel()

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return err
	}

	dc, err := runnermgr.NewDockerClient(cfg.DockerHost)
	if err != nil {
		return err
	}
	docker := &runnermgr.Manager{Docker: dc, SidecarMemory: cfg.SidecarMemory}
	ws := &workspace.Manager{Dir: cfg.WorkspacesDir}

	met := metrics.New(st)
	hub := events.NewHub()
	taskSvc := &tasks.Service{Store: st, Hub: hub}
	sched := scheduler.New(&scheduler.Scheduler{
		Cfg: cfg, Store: st, Hub: hub, Tasks: taskSvc, Docker: docker, Workspace: ws,
		Hooks: scheduler.Hooks{RunFinished: met.RunFinished},
	})
	taskSvc.Executor = sched

	var gh *githubapp.App
	var webhook http.Handler
	if cfg.GitHubEnabled() {
		gh, err = githubapp.New(cfg.GitHubAppID, cfg.GitHubPrivateKeyPath, cfg.GitHubAPIURL)
		if err != nil {
			return err
		}
		gh.OnError = met.GitHubError
		ws.Auth = gh.Token
		var scanner workspace.SecretsScanner
		if cfg.SecretsScan == "gitleaks" {
			scanner = workspace.Gitleaks("gitleaks")
		}
		sched.Publisher = &githubapp.Publisher{App: gh, Workspace: ws, Scanner: scanner, TaskURL: taskURL(cfg)}
		webhook = &githubapp.Webhook{
			Secret: []byte(cfg.GitHubWebhookSecret), Store: st, Hub: hub, Tasks: taskSvc,
			Cleanup: func(ctx context.Context, id int64) { _ = docker.RemoveAll(ctx, id) },
			OnCIFailed: func(ctx context.Context, f githubapp.CIFailure) {
				// Hook point for automatic CI fixing (not in v1).
				slog.Info("CI failed", "task_id", f.Task.ID, "check", f.Name)
			},
		}
	} else {
		slog.Warn("GitHub App not configured: commits stay in local workspaces")
	}

	srv := &api.Server{
		Webhook:  webhook,
		Metrics:  met.Handler(),
		Cfg:      cfg,
		Store:    st,
		Tasks:    taskSvc,
		Projects: &projects.Service{Store: st},
		Auth: &auth.Manager{
			Store: st, PasswordHash: cfg.AdminPasswordHash, TOTPSecret: cfg.AdminTOTPSecret,
			SessionSecret: []byte(cfg.SessionSecret), CookieInsecure: cfg.CookieInsecure,
		},
		Hub: hub,
		Diff: func(ctx context.Context, t *store.Task, repo *store.Repository, limit int) (string, bool, error) {
			return ws.Diff(ctx, t.ID, repo.BaseBranch, limit)
		},
		Health: func(ctx context.Context) map[string]error {
			return map[string]error{"database": st.Ping(ctx), "docker": docker.Ping(ctx)}
		},
	}

	if gh != nil {
		srv.GitHub = gh
	}

	var wg sync.WaitGroup
	if cfg.TelegramEnabled() {
		tg, err := telego.NewBot(cfg.TelegramBotToken, telego.WithDiscardLogger())
		if err != nil {
			return fmt.Errorf("telegram: %w", err)
		}
		bot := &telegram.Bot{
			API: tg, Store: st, Tasks: taskSvc, Hub: hub, OwnerID: cfg.TelegramAllowedUserID,
			WebAppURL: cfg.WebAppURL, MiniAppLink: cfg.TelegramMiniAppLink, OnError: met.TelegramError,
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := bot.Run(ctx, tg); err != nil {
				slog.Error("telegram bot stopped", "err", err)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		(&maintenance.Maintainer{
			Store: st, Docker: docker, Workspaces: ws,
			EventRetention: cfg.EventRetention, WorkspaceRetention: cfg.WorkspaceRetention, SidecarIdleTTL: cfg.SidecarIdleTTL,
		}).Run(ctx)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := sched.Run(ctx); err != nil {
			slog.Error("scheduler stopped", "err", err)
		}
	}()
	err = serveHTTP(ctx, cfg.HTTPAddr, srv.Handler())
	cancel()
	wg.Wait()
	return err
}

func serveHTTP(ctx context.Context, addr string, h http.Handler) error {
	hs := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- hs.ListenAndServe() }()
	slog.Info("listening", "addr", addr)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := hs.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// taskURL links PRs and Telegram messages to the task in the web app.
func taskURL(cfg *config.Config) func(int64) string {
	return func(id int64) string {
		base := cfg.WebAppURL
		if base == "" {
			base = cfg.PublicBaseURL
		}
		if base == "" {
			return ""
		}
		return fmt.Sprintf("%s/tasks/%d", base, id)
	}
}
