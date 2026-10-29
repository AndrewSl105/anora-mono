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

	"github.com/andrewsl105/fleetpocket/server/internal/api"
	"github.com/andrewsl105/fleetpocket/server/internal/attachments"
	"github.com/andrewsl105/fleetpocket/server/internal/auth"
	"github.com/andrewsl105/fleetpocket/server/internal/config"
	"github.com/andrewsl105/fleetpocket/server/internal/events"
	"github.com/andrewsl105/fleetpocket/server/internal/githubapp"
	"github.com/andrewsl105/fleetpocket/server/internal/hosts"
	"github.com/andrewsl105/fleetpocket/server/internal/maintenance"
	"github.com/andrewsl105/fleetpocket/server/internal/metrics"
	"github.com/andrewsl105/fleetpocket/server/internal/nodes"
	"github.com/andrewsl105/fleetpocket/server/internal/projects"
	"github.com/andrewsl105/fleetpocket/server/internal/runnermgr"
	"github.com/andrewsl105/fleetpocket/server/internal/scheduler"
	"github.com/andrewsl105/fleetpocket/server/internal/store"
	"github.com/andrewsl105/fleetpocket/server/internal/tasks"
	"github.com/andrewsl105/fleetpocket/server/internal/telegram"
	"github.com/andrewsl105/fleetpocket/server/internal/workspace"
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
	files := &attachments.Files{Dir: cfg.AttachmentsDir}

	local := &hosts.Local{
		Docker: docker, Workspace: ws, AttachmentsDir: files.TaskDir, AgentEnv: cfg.AgentEnv,
	}
	if cfg.SecretsScan == "gitleaks" {
		local.Scanner = workspace.Gitleaks("gitleaks")
	}
	registry := &hosts.Registry{Local: local, MaxSeq: st.MaxSeq}

	met := metrics.New(st)
	hub := events.NewHub()
	taskSvc := &tasks.Service{Store: st, Hub: hub, Files: files, DefaultAgent: cfg.DefaultAgent, AgentEnabled: cfg.AgentEnabled}
	sched := scheduler.New(&scheduler.Scheduler{
		Cfg: cfg, Store: st, Hub: hub, Tasks: taskSvc, Hosts: registry, Files: files,
		Hooks: scheduler.Hooks{RunFinished: met.RunFinished},
	})
	taskSvc.Executor = sched
	nodeSvc := &nodes.Service{Store: st, Hub: hub, Registry: registry, RunnerImage: cfg.RunnerImage}
	registry.OnChange = func(id string, online bool) {
		nodeSvc.Changed(id, online)
		if online {
			sched.Wake() // its queued tasks may start now
		}
	}
	registry.OnConnect = func(ctx context.Context, h *hosts.Remote, runners []runnermgr.ContainerInfo) {
		sched.ReconcileNode(ctx, h, runners)
	}
	// hostOf resolves a task's host; offline nodes report false.
	hostOf := func(t *store.Task) (maintenance.Host, bool) {
		if t.Node != nil && !registry.IsOnline(*t.Node) {
			return nil, false
		}
		return registry.For(t.Node), true
	}

	var gh *githubapp.App
	var webhook http.Handler
	if cfg.GitHubEnabled() {
		gh, err = githubapp.New(cfg.GitHubAppID, cfg.GitHubPrivateKeyPath, cfg.GitHubAPIURL)
		if err != nil {
			return err
		}
		gh.OnError = met.GitHubError
		taskSvc.GitHub = gh
		ws.Auth = gh.Token
		registry.Auth = gh.Token
		sched.Publisher = &githubapp.Publisher{App: gh, RequireScanner: cfg.SecretsScan == "gitleaks", TaskURL: taskURL(cfg)}
		webhook = &githubapp.Webhook{
			Secret: []byte(cfg.GitHubWebhookSecret), Store: st, Hub: hub, Tasks: taskSvc,
			Cleanup: func(ctx context.Context, id int64) {
				if t, err := st.GetTask(ctx, id); err == nil {
					if h, ok := hostOf(t); ok {
						_ = h.RemoveAll(ctx, id)
					}
				}
			},
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
		Hub:   hub,
		Nodes: nodeSvc,
		Files: files,
		Diff: func(ctx context.Context, t *store.Task, repo *store.Repository, limit int) (string, bool, error) {
			return registry.For(t.Node).Diff(ctx, t.ID, repo.BaseBranch, limit)
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
			STTEnabled: cfg.STTProvider != "off",
		}
		switch cfg.STTProvider {
		case "openai":
			bot.STT = &telegram.OpenAITranscriber{APIKey: cfg.OpenAIAPIKey}
		case "whispercpp":
			bot.STT = &telegram.WhisperCPPTranscriber{URL: cfg.WhisperCPPURL}
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
			Store: st, Host: hostOf, Attachments: files,
			EventRetention: cfg.EventRetention, WorkspaceRetention: cfg.WorkspaceRetention, SidecarIdleTTL: cfg.SidecarIdleTTL,
		}).Run(ctx)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		files.Watch(ctx, st, hub)
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
