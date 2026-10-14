package scheduler

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/andrewsl105/anora-runner/internal/runnermgr"
	"github.com/andrewsl105/anora-runner/internal/store"
	"github.com/andrewsl105/anora-runner/internal/tasks"
)

// Reconcile brings the database and Docker back in line after a restart:
// re-attaches running runners, applies exits that happened while we were
// down, fails runs whose container is gone, re-queues interrupted
// preparation, resumes interrupted pushes and removes orphaned resources.
func (s *Scheduler) Reconcile(ctx context.Context) error {
	runners, err := s.Docker.ListRunners(ctx)
	if err != nil {
		return err
	}
	seen := map[int64]bool{}
	for _, c := range runners {
		seen[c.TaskID] = true
		s.reconcileRunner(ctx, c)
	}

	live, err := s.Store.TasksByStatus(ctx, tasks.Running, tasks.WaitingInput)
	if err != nil {
		return err
	}
	for _, t := range live {
		if seen[t.ID] {
			continue
		}
		run, err := s.Store.LatestRun(ctx, t.ID)
		if err == nil {
			s.replayFile(ctx, t, run.ID, &runOutcome{})
			_, _ = s.Store.FinishRun(ctx, run.ID, -1)
		}
		s.fail(ctx, t, "lost")
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

	pushing, err := s.Store.TasksByStatus(ctx, tasks.Pushing)
	if err != nil {
		return err
	}
	for _, t := range pushing {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.afterRun(context.WithoutCancel(ctx), t, s.lastSummary(ctx, t.ID), 0, "")
		}()
	}

	return s.removeOrphans(ctx)
}

func (s *Scheduler) reconcileRunner(ctx context.Context, c runnermgr.ContainerInfo) {
	t, err := s.Store.GetTask(ctx, c.TaskID)
	if err != nil {
		_ = s.Docker.Remove(ctx, c.ID)
		return
	}
	run, err := s.Store.GetRun(ctx, c.RunID)
	if err != nil || run.FinishedAt != nil || run.TaskID != t.ID {
		_ = s.Docker.Remove(ctx, c.ID)
		return
	}
	log := s.log(t, run.ID)
	if c.Running {
		// Attach first, then replay the file: nothing emitted in between is lost,
		// and duplicates are dropped by UNIQUE (run_id, seq).
		r, err := s.Docker.Attach(ctx, c.ID)
		if err != nil {
			log.Error("re-attach", "err", err)
			_ = s.Docker.Stop(ctx, c.ID, 5*time.Second)
			return
		}
		var out runOutcome
		s.replayFile(ctx, t, run.ID, &out)
		s.register(t.ID, run.ID, r)
		log.Info("re-attached runner", "container", c.ID[:12])
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.supervise(context.WithoutCancel(ctx), t, run, r, time.Now())
		}()
		return
	}
	var out runOutcome
	s.replayFile(ctx, t, run.ID, &out)
	log.Info("applying exit that happened while down", "exit_code", c.ExitCode)
	run.ContainerID = &c.ID
	s.finishRun(ctx, t.ID, run, c.ExitCode, out, time.Now())
}

// replayFile persists events from <workspace>/.anora/events.jsonl that are
// newer than what the database has, and fills out from the run's events.
func (s *Scheduler) replayFile(ctx context.Context, t *store.Task, runID int64, out *runOutcome) {
	f, err := os.Open(filepath.Join(s.Workspace.Path(t.ID), ".anora", "events.jsonl"))
	if err != nil {
		return
	}
	defer f.Close()
	maxSeq, err := s.Store.MaxSeq(ctx, t.ID)
	if err != nil {
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
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 8<<20)
	n := 0
	for sc.Scan() {
		ev, err := runnermgr.ParseEvent(append([]byte(nil), sc.Bytes()...))
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

// removeOrphans deletes containers and networks of tasks that are terminal
// or no longer exist.
func (s *Scheduler) removeOrphans(ctx context.Context) error {
	ids, err := s.Docker.TaskIDsWithResources(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for id := range ids {
		t, err := s.Store.GetTask(ctx, id)
		if errors.Is(err, store.ErrNotFound) || (err == nil && tasks.IsTerminal(t.Status)) {
			slog.Info("removing orphaned resources", "task_id", id)
			errs = append(errs, s.Docker.RemoveAll(ctx, id))
		}
	}
	return errors.Join(errs...)
}
