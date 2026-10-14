package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/andrewsl105/anora-runner/internal/api"
	"github.com/andrewsl105/anora-runner/internal/auth"
	"github.com/andrewsl105/anora-runner/internal/config"
	"github.com/andrewsl105/anora-runner/internal/events"
	"github.com/andrewsl105/anora-runner/internal/projects"
	"github.com/andrewsl105/anora-runner/internal/store"
	"github.com/andrewsl105/anora-runner/internal/tasks"
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

	hub := events.NewHub()
	taskSvc := &tasks.Service{Store: st, Hub: hub}
	srv := &api.Server{
		Cfg:      cfg,
		Store:    st,
		Tasks:    taskSvc,
		Projects: &projects.Service{Store: st},
		Auth: &auth.Manager{
			Store: st, PasswordHash: cfg.AdminPasswordHash, TOTPSecret: cfg.AdminTOTPSecret,
			SessionSecret: []byte(cfg.SessionSecret), CookieInsecure: cfg.CookieInsecure,
		},
		Hub: hub,
		Health: func(ctx context.Context) map[string]error {
			return map[string]error{"database": st.Ping(ctx)}
		},
	}
	return serveHTTP(ctx, cfg.HTTPAddr, srv.Handler())
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
