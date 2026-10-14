// Package maintenance runs periodic housekeeping: event retention, workspace
// and Claude state cleanup, idle sidecars and expired sessions.
package maintenance

import (
	"context"
	"log/slog"
	"time"

	"github.com/andrewsl105/anora-runner/internal/store"
	"github.com/andrewsl105/anora-runner/internal/tasks"
)

// Docker is the part of the runner manager cleanup needs.
type Docker interface {
	RemoveAll(ctx context.Context, taskID int64) error
	RemoveSidecars(ctx context.Context, taskID int64) error
	RemoveVolume(ctx context.Context, taskID int64) error
}

type Workspaces interface {
	Remove(taskID int64) error
}

type Maintainer struct {
	Store              *store.Store
	Docker             Docker
	Workspaces         Workspaces
	EventRetention     time.Duration
	WorkspaceRetention time.Duration
	SidecarIdleTTL     time.Duration
	Interval           time.Duration
	Now                func() time.Time
}

func (m *Maintainer) Run(ctx context.Context) {
	if m.Interval == 0 {
		m.Interval = 10 * time.Minute
	}
	t := time.NewTicker(m.Interval)
	defer t.Stop()
	for {
		m.Once(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (m *Maintainer) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// Once runs every housekeeping step one time.
func (m *Maintainer) Once(ctx context.Context) {
	now := m.now()
	if n, err := m.Store.DeleteOldEvents(ctx, now.Add(-m.EventRetention)); err != nil {
		slog.Error("retention: events", "err", err)
	} else if n > 0 {
		slog.Info("retention: deleted events", "count", n)
	}

	old, err := m.Store.TasksToClean(ctx, now.Add(-m.WorkspaceRetention))
	if err != nil {
		slog.Error("retention: tasks to clean", "err", err)
	}
	for _, t := range old {
		log := slog.With("task_id", t.ID, "project", t.ProjectID)
		if err := m.Docker.RemoveAll(ctx, t.ID); err != nil {
			log.Warn("cleanup containers", "err", err)
		}
		if err := m.Docker.RemoveVolume(ctx, t.ID); err != nil {
			log.Warn("cleanup volume", "err", err)
			continue
		}
		if err := m.Workspaces.Remove(t.ID); err != nil {
			log.Warn("cleanup workspace", "err", err)
			continue
		}
		if err := m.Store.MarkCleaned(ctx, t.ID); err == nil {
			log.Info("cleaned workspace and Claude state")
		}
	}

	idle, err := m.Store.IdleTasks(ctx, []string{tasks.Paused, tasks.PROpen}, now.Add(-m.SidecarIdleTTL))
	if err != nil {
		slog.Error("retention: idle tasks", "err", err)
	}
	for _, t := range idle {
		if err := m.Docker.RemoveSidecars(ctx, t.ID); err != nil {
			slog.Warn("remove idle sidecars", "task_id", t.ID, "err", err)
		}
	}

	if n, err := m.Store.DeleteExpiredSessions(ctx); err != nil {
		slog.Error("retention: sessions", "err", err)
	} else if n > 0 {
		slog.Info("retention: deleted expired sessions", "count", n)
	}
}
