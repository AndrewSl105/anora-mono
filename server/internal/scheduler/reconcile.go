package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/andrewsl105/fleetpocket/server/internal/hosts"
	"github.com/andrewsl105/fleetpocket/server/internal/runnermgr"
	"github.com/andrewsl105/fleetpocket/server/internal/store"
	"github.com/andrewsl105/fleetpocket/server/internal/tasks"
)

// Reconcile brings the database and the server's Docker back in line after
// a restart: re-attaches running runners, applies exits that happened while
// we were down, fails runs whose container is gone, re-queues interrupted
// preparation, resumes interrupted pushes and removes orphaned resources.
// Tasks placed on nodes are reconciled when their node connects
// (ReconcileNode); interrupted preparation is re-queued for every host.
func (s *Scheduler) Reconcile(ctx context.Context) error {
	local := s.Hosts.Local
	runners, err := local.ListRunners(ctx)
	if err != nil {
		return err
	}
	seen := map[int64]bool{}
	for _, c := range runners {
		seen[c.TaskID] = true
		s.reconcileRunner(ctx, local, c)
	}

	live, err := s.Store.TasksByStatus(ctx, tasks.Running, tasks.WaitingInput)
	if err != nil {
		return err
	}
	for _, t := range live {
		if seen[t.ID] || t.Node != nil {
			continue
		}
		s.lost(ctx, local, t)
	}

	preparing, err := s.Store.TasksByStatus(ctx, tasks.Preparing)
	if err != nil {
		return err
	}
	for _, t := range preparing {
		if run, err := s.Store.LatestRun(ctx, t.ID); err == nil && run.FinishedAt == nil && !seen[t.ID] {
			_ = s.Store.ResetRunStart(ctx, run.ID)
		}
		if nt, err := s.Store.SetStatus(ctx, t.ID, []string{tasks.Preparing}, tasks.Queued, "requeued after restart", false); err == nil {
			s.Hub.PublishTask(nt)
		}
	}

	s.resumePushes(ctx, nil)
	return s.removeOrphans(ctx, local)
}

// ReconcileNode runs when a node connects: runners the server did not know
// about (a server restart) are re-attached or their exit applied; live tasks
// of the node without a runner are lost; cancelled tasks whose runner kept
// going while the node was away are cancelled again; interrupted pushes resume.
func (s *Scheduler) ReconcileNode(ctx context.Context, h *hosts.Remote, unknown []runnermgr.ContainerInfo) {
	node := h.ID()
	seen := map[int64]bool{}
	for _, c := range unknown {
		seen[c.TaskID] = true
		s.reconcileRunner(ctx, h, c)
	}
	live, err := s.Store.TasksByStatus(ctx, tasks.Running, tasks.WaitingInput)
	if err != nil {
		slog.Error("reconcile node", "node", node, "err", err)
		return
	}
	for _, t := range live {
		if t.Node == nil || *t.Node != node || seen[t.ID] {
			continue
		}
		if _, ok := s.liveRunner(t.ID); ok {
			continue
		}
		s.lost(ctx, h, t)
	}
	s.mu.Lock()
	var cancel []*runnermgr.Runner
	for id, l := range s.live {
		if l.host != hosts.Host(h) {
			continue
		}
		if t, err := s.Store.GetTask(ctx, id); err == nil && t.Status == tasks.Cancelled {
			cancel = append(cancel, l.runner)
		}
	}
	s.mu.Unlock()
	for _, r := range cancel {
		_ = r.Send(runnermgr.Cancel())
	}
	s.resumePushes(ctx, &node)
	if err := s.removeOrphans(ctx, h); err != nil {
		slog.Warn("reconcile node: orphans", "node", node, "err", err)
	}
	s.Wake()
}

// lost fails a live task whose runner is gone, after saving what its event
// log has.
func (s *Scheduler) lost(ctx context.Context, h hosts.Host, t *store.Task) {
	run, err := s.Store.LatestRun(ctx, t.ID)
	if err == nil {
		s.replayFile(ctx, h, t, run.ID, &runOutcome{})
		_, _ = s.Store.FinishRun(ctx, run.ID, -1)
	}
	s.fail(ctx, t, "lost")
}

// resumePushes restarts afterRun for tasks stuck in pushing on one host
// (node nil: the server).
func (s *Scheduler) resumePushes(ctx context.Context, node *string) {
	pushing, err := s.Store.TasksByStatus(ctx, tasks.Pushing)
	if err != nil {
		slog.Error("resume pushes", "err", err)
		return
	}
	for _, t := range pushing {
		if (node == nil) != (t.Node == nil) || (node != nil && *node != *t.Node) {
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.afterRun(context.WithoutCancel(ctx), t, s.lastSummary(ctx, t.ID), 0, "")
		}()
	}
}

func (s *Scheduler) reconcileRunner(ctx context.Context, h hosts.Host, c runnermgr.ContainerInfo) {
	t, err := s.Store.GetTask(ctx, c.TaskID)
	if err != nil {
		_ = h.Remove(ctx, c.ID)
		return
	}
	run, err := s.Store.GetRun(ctx, c.RunID)
	// A queued or preparing task has no runner yet: this one is left over from
	// a start whose result was lost (the run was reset).
	if err != nil || run.FinishedAt != nil || run.TaskID != t.ID || placement(t) != h.ID() ||
		t.Status == tasks.Queued || t.Status == tasks.Preparing {
		_ = h.Remove(ctx, c.ID)
		return
	}
	log := s.log(t, run.ID)
	if c.Running {
		// Attach first, then replay the file: nothing emitted in between is lost,
		// and duplicates are dropped by UNIQUE (run_id, seq).
		r, err := h.Attach(ctx, c.ID)
		if err != nil {
			log.Error("re-attach", "err", err)
			_ = h.Stop(ctx, c.ID, 5*time.Second)
			return
		}
		var out runOutcome
		s.replayFile(ctx, h, t, run.ID, &out)
		s.register(t.ID, run.ID, h, r)
		log.Info("re-attached runner", "container", short(c.ID), "node", h.ID())
		if t.Status == tasks.Cancelled {
			_ = r.Send(runnermgr.Cancel())
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.supervise(context.WithoutCancel(ctx), h, t, run, r, time.Now())
		}()
		return
	}
	var out runOutcome
	s.replayFile(ctx, h, t, run.ID, &out)
	log.Info("applying exit that happened while down", "exit_code", c.ExitCode)
	run.ContainerID = &c.ID
	s.finishRun(ctx, h, t.ID, run, c.ExitCode, out, time.Now())
}

// placement is the task's host id ("" for the server).
func placement(t *store.Task) string {
	if t.Node == nil {
		return ""
	}
	return *t.Node
}

// replayFile persists events from <workspace>/.fleetpocket/events.jsonl that are
// newer than what the database has, and fills out from the run's events.
func (s *Scheduler) replayFile(ctx context.Context, h hosts.Host, t *store.Task, runID int64, out *runOutcome) {
	maxSeq, err := s.Store.MaxSeq(ctx, t.ID)
	if err != nil {
		return
	}
	lines, err := h.EventLog(ctx, t.ID, maxSeq)
	if err != nil || len(lines) == 0 {
		return
	}
	run, err := s.Store.GetRun(ctx, runID)
	if err != nil {
		return
	}
	var baseline float64
	if run.Kind == tasks.RunResume || run.Kind == tasks.RunFollowup {
		baseline, _ = s.Store.SessionBaselineCost(ctx, t.ID, runID)
	}
	n := 0
	for _, line := range lines {
		ev, err := runnermgr.ParseEvent(line)
		if err != nil || ev.Seq <= maxSeq {
			continue
		}
		s.handleEvent(ctx, t, runID, baseline, ev, out)
		n++
	}
	if n > 0 {
		s.log(t, runID).Info("replayed events", "count", n)
	}
}

func (s *Scheduler) lastSummary(ctx context.Context, taskID int64) string {
	evs, err := s.Store.LastEvents(ctx, taskID, []string{runnermgr.EvDone}, 1)
	if err != nil || len(evs) == 0 {
		return ""
	}
	ev, err := runnermgr.ParseEvent(evs[0].Payload)
	if err != nil {
		return ""
	}
	return ev.Summary
}

// removeOrphans deletes a host's containers and networks of tasks that are
// terminal, no longer exist or belong to another host.
func (s *Scheduler) removeOrphans(ctx context.Context, h hosts.Host) error {
	ids, err := h.TaskIDsWithResources(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for id := range ids {
		t, err := s.Store.GetTask(ctx, id)
		if errors.Is(err, store.ErrNotFound) || (err == nil && (tasks.IsTerminal(t.Status) || placement(t) != h.ID())) {
			slog.Info("removing orphaned resources", "task_id", id, "node", h.ID())
			errs = append(errs, h.RemoveAll(ctx, id))
		}
	}
	return errors.Join(errs...)
}
