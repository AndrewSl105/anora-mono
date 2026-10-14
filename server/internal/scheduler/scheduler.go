// Package scheduler claims queued tasks within the concurrency limit and
// drives each run: workspace, network, sidecars, runner container, event
// stream, and the state transition when the runner exits.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andrewsl105/anora/server/internal/config"
	"github.com/andrewsl105/anora/server/internal/events"
	"github.com/andrewsl105/anora/server/internal/runnermgr"
	"github.com/andrewsl105/anora/server/internal/store"
	"github.com/andrewsl105/anora/server/internal/tasks"
	"github.com/andrewsl105/anora/server/internal/workspace"
)

const (
	pollInterval = 5 * time.Second
	cancelGrace  = 15 * time.Second
	commitAuthor = "anora[bot]"
	commitEmail  = "anora[bot]@users.noreply.github.com"
)

// ErrRejected marks a diff validation failure: the task fails and the
// workspace is kept for inspection.
var ErrRejected = errors.New("diff rejected")

// PublishInput is what the publisher needs to push and open/update the PR.
type PublishInput struct {
	Task    *store.Task
	Repo    *store.Repository
	Project *store.Project
	Branch  string
	Summary string
	CostUSD float64
	Turns   int
	Limit   string // non-empty when the run stopped at a limit
}

type PublishResult struct {
	PRNumber int
	PRURL    string
	Created  bool
}

// Publisher validates the diff, pushes the branch and opens or updates the PR.
type Publisher interface {
	Publish(ctx context.Context, in PublishInput) (*PublishResult, error)
}

// Hooks lets other parts (metrics) observe runs without coupling.
type Hooks struct {
	RunFinished func(project string, exitCode int, dur time.Duration)
}

type Scheduler struct {
	Cfg       *config.Config
	Store     *store.Store
	Hub       *events.Hub
	Tasks     *tasks.Service
	Docker    *runnermgr.Manager
	Workspace *workspace.Manager
	Publisher Publisher // nil: commits stay local (phase 3 / GitHub not configured)
	Hooks     Hooks

	wake chan struct{}
	mu   sync.Mutex
	live map[int64]*liveRun
	wg   sync.WaitGroup
}

type liveRun struct {
	runner *runnermgr.Runner
	runID  int64
}

// New initialises a Scheduler whose dependencies are already set.
func New(s *Scheduler) *Scheduler {
	s.wake = make(chan struct{}, 1)
	s.live = map[int64]*liveRun{}
	return s
}

// ---- tasks.Executor ----

func (s *Scheduler) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Scheduler) liveRunner(taskID int64) (*runnermgr.Runner, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.live[taskID]
	if !ok {
		return nil, false
	}
	return l.runner, true
}

func (s *Scheduler) Answer(taskID int64, runnerQuestionID, text string) error {
	r, ok := s.liveRunner(taskID)
	if !ok {
		return tasks.ErrNotRunning
	}
	return r.Send(runnermgr.NewAnswer(runnerQuestionID, text))
}

func (s *Scheduler) Interrupt(taskID int64) error {
	r, ok := s.liveRunner(taskID)
	if !ok {
		return tasks.ErrNotRunning
	}
	return r.Send(runnermgr.Interrupt())
}

// Cancel asks a live runner to stop (SIGTERM after a grace period) and removes
// the task's containers; a task without a runner just loses its sidecars.
func (s *Scheduler) Cancel(ctx context.Context, taskID int64) error {
	r, ok := s.liveRunner(taskID)
	if !ok {
		return s.Docker.RemoveAll(ctx, taskID)
	}
	_ = r.Send(runnermgr.Cancel())
	go func() {
		select {
		case <-r.Done():
		case <-time.After(cancelGrace):
			_ = s.Docker.Stop(context.Background(), r.ContainerID, 5*time.Second)
		}
	}()
	return nil
}

// ---- loop ----

// Run reconciles leftover state, then claims tasks until ctx ends.
func (s *Scheduler) Run(ctx context.Context) error {
	if err := s.Reconcile(ctx); err != nil {
		slog.Error("reconcile", "err", err)
	}
	t := time.NewTicker(pollInterval)
	defer t.Stop()
	for {
		s.claim(ctx)
		select {
		case <-ctx.Done():
			s.wg.Wait()
			return nil
		case <-s.wake:
		case <-t.C:
		}
	}
}

func (s *Scheduler) claim(ctx context.Context) {
	for {
		t, err := s.Store.ClaimNext(ctx, s.Cfg.MaxConcurrentTasks)
		if err != nil {
			if ctx.Err() == nil {
				slog.Error("claim", "err", err)
			}
			return
		}
		if t == nil {
			return
		}
		s.Hub.PublishTask(t)
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.execute(context.WithoutCancel(ctx), t)
		}()
	}
}

func (s *Scheduler) log(t *store.Task, runID int64) *slog.Logger {
	return slog.With("task_id", t.ID, "project", t.ProjectID, "run_id", runID)
}

// fail marks the task failed and removes its containers (workspace kept).
func (s *Scheduler) fail(ctx context.Context, t *store.Task, reason string) {
	s.log(t, 0).Warn("task failed", "reason", reason)
	_ = s.Docker.RemoveAll(ctx, t.ID)
	_ = s.Store.CloseOpenQuestions(ctx, t.ID, "")
	if _, err := s.Tasks.SetStatus(ctx, t.ID, tasks.Failed, reason); err != nil && !errors.Is(err, store.ErrConflict) {
		slog.Error("set failed", "task_id", t.ID, "err", err)
	}
	s.Wake()
}

// execute runs one claimed task (status preparing).
func (s *Scheduler) execute(ctx context.Context, t *store.Task) {
	run, err := s.Store.PendingRun(ctx, t.ID)
	if err != nil {
		s.fail(ctx, t, "no pending run")
		return
	}
	log := s.log(t, run.ID)
	project, err := s.Store.GetProject(ctx, t.ProjectID)
	if err != nil {
		s.fail(ctx, t, "project: "+err.Error())
		return
	}
	repo, err := s.Store.GetRepository(ctx, t.RepositoryID)
	if err != nil {
		s.fail(ctx, t, "repository: "+err.Error())
		return
	}

	branch := workspace.Branch(t.ID, t.Title)
	if t.Branch != nil {
		branch = *t.Branch
	} else if err := s.Store.SetBranch(ctx, t.ID, branch); err != nil {
		s.fail(ctx, t, err.Error())
		return
	}
	wsRepo := workspace.Repo{FullName: repo.FullName, InstallationID: repo.InstallationID, BaseBranch: repo.BaseBranch}
	if err := s.Workspace.Prepare(ctx, t.ID, wsRepo, branch); err != nil {
		s.fail(ctx, t, "prepare workspace: "+err.Error())
		return
	}
	image := deref(project.RunnerImage, s.Cfg.RunnerImage)
	if err := s.Docker.HasImage(ctx, image); err != nil {
		s.fail(ctx, t, err.Error())
		return
	}
	if _, err := s.Docker.EnsureNetwork(ctx, t.ID, t.ProjectID); err != nil {
		s.fail(ctx, t, "network: "+err.Error())
		return
	}
	if _, err := s.Docker.EnsureVolume(ctx, t.ID, t.ProjectID); err != nil {
		s.fail(ctx, t, "volume: "+err.Error())
		return
	}
	svcEnv, err := s.Docker.EnsureSidecars(ctx, t.ID, t.ProjectID, project.Services)
	if err != nil {
		s.fail(ctx, t, err.Error())
		return
	}

	env := map[string]string{}
	for k, v := range project.Env {
		env[k] = v
	}
	for k, v := range svcEnv {
		env[k] = v
	}
	if s.Cfg.ClaudeOAuthToken != "" {
		env["CLAUDE_CODE_OAUTH_TOKEN"] = s.Cfg.ClaudeOAuthToken
	} else if s.Cfg.AnthropicAPIKey != "" {
		env["ANTHROPIC_API_KEY"] = s.Cfg.AnthropicAPIKey
	}
	if s.Cfg.RunnerFake {
		env["ANORA_RUNNER_FAKE"] = "1"
	}

	cmd := s.runCommand(t, run, project, repo, branch)
	if run.Kind == tasks.RunResume || run.Kind == tasks.RunFollowup {
		if t.SessionID != nil {
			cmd.ResumeSessionID = *t.SessionID
		}
	}
	_ = s.Store.CloseOpenQuestions(ctx, t.ID, "")

	if err := s.Store.MarkRunStarting(ctx, run.ID); err != nil {
		s.fail(ctx, t, err.Error())
		return
	}
	r, err := s.Docker.StartRunner(ctx, runnermgr.RunnerSpec{
		TaskID: t.ID, RunID: run.ID, Project: t.ProjectID, Image: image,
		Workspace: s.Workspace.Path(t.ID), Env: env,
		Memory: deref(project.RunnerMemory, s.Cfg.RunnerMemory),
		CPUs:   deref(project.RunnerCPUs, s.Cfg.RunnerCPUs),
		User:   s.Cfg.RunnerUser,
	})
	if err != nil {
		_, _ = s.Store.FinishRun(ctx, run.ID, -1)
		s.fail(ctx, t, err.Error())
		return
	}
	_ = s.Store.SetRunContainer(ctx, run.ID, r.ContainerID)

	nt, err := s.Tasks.SetStatus(ctx, t.ID, tasks.Running, "")
	if err != nil {
		// Cancelled while preparing.
		log.Info("task left preparing before start", "err", err)
		_ = r.Send(runnermgr.Cancel())
		r.Close()
		_ = s.Docker.Remove(ctx, r.ContainerID)
		_, _ = s.Store.FinishRun(ctx, run.ID, runnermgr.ExitCancelled)
		_ = s.Docker.RemoveAll(ctx, t.ID)
		return
	}
	t = nt
	s.addEvent(ctx, t.ID, &run.ID, "run_started", map[string]any{"run_id": run.ID, "kind": run.Kind})
	s.register(t.ID, run.ID, r)
	log.Info("runner started", "container", r.ContainerID[:12], "kind", run.Kind)

	if err := r.Send(cmd); err != nil {
		log.Error("send run", "err", err)
	}
	if run.Kind == tasks.RunResume {
		s.replayAnswer(ctx, t.ID, run.ID, r)
	}
	s.supervise(ctx, t, run, r, time.Now())
}

// replayAnswer sends the answer of the question that paused the previous run.
// A real session gets the answer in its prompt; the fake runner re-asks the
// same question id and consumes this.
func (s *Scheduler) replayAnswer(ctx context.Context, taskID, runID int64, r *runnermgr.Runner) {
	prev, err := s.Store.PreviousRun(ctx, taskID, runID)
	if err != nil {
		return
	}
	q, err := s.Store.AnsweredQuestionOfRun(ctx, prev.ID)
	if err != nil || q.Answer == nil {
		return
	}
	_ = r.Send(runnermgr.NewAnswer(tasks.RunnerQuestionID(q.ID), *q.Answer))
}

func (s *Scheduler) register(taskID, runID int64, r *runnermgr.Runner) {
	s.mu.Lock()
	s.live[taskID] = &liveRun{runner: r, runID: runID}
	s.mu.Unlock()
}

func (s *Scheduler) unregister(taskID int64) {
	s.mu.Lock()
	delete(s.live, taskID)
	s.mu.Unlock()
}

func (s *Scheduler) runCommand(t *store.Task, run *store.Run, p *store.Project, repo *store.Repository, branch string) runnermgr.RunCommand {
	maxTurns := deref(p.MaxTurns, s.Cfg.DefaultMaxTurns)
	budget := deref(p.MaxBudgetUSD, s.Cfg.DefaultMaxBudgetUSD)
	dur := s.Cfg.DefaultMaxDuration
	if p.MaxDurationSec != nil {
		dur = time.Duration(*p.MaxDurationSec) * time.Second
	}
	msg := t.Title
	if run.Kind == tasks.RunFollowup || run.Kind == tasks.RunResume {
		msg = "Follow-up: " + tasks.TitleFromPrompt(run.Input)
	}
	return runnermgr.NewRun(runnermgr.RunCommand{
		TaskID:             strconv.FormatInt(t.ID, 10),
		Prompt:             run.Input,
		AppendSystemPrompt: SystemPrompt(t, repo, branch, p.Instructions),
		Model:              deref(p.Model, s.Cfg.DefaultModel),
		Limits: runnermgr.Limits{
			MaxTurns:           maxTurns,
			MaxBudgetUSD:       budget,
			MaxDurationSec:     int(dur.Seconds()),
			QuestionTimeoutSec: int(s.Cfg.QuestionTimeout.Seconds()),
		},
		Commit: runnermgr.CommitInfo{
			Message:     fmt.Sprintf("%s\n\nAnora task #%d", msg, t.ID),
			AuthorName:  commitAuthor,
			AuthorEmail: commitEmail,
		},
	})
}

// SystemPrompt is appended to Claude Code's default system prompt.
func SystemPrompt(t *store.Task, repo *store.Repository, branch, projectInstructions string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are running unattended inside Anora Runner on task #%d.\n", t.ID)
	fmt.Fprintf(&b, "Repository %s is checked out in /workspace on branch %s (base %s).\n", repo.FullName, branch, repo.BaseBranch)
	b.WriteString("Do not push, change git remotes or touch .github/: Anora Runner commits your changes and opens the pull request.\n")
	b.WriteString("If a decision needs the owner, ask with the AskUserQuestion tool and keep options short.\n")
	b.WriteString("Finish with a short summary of what you changed and anything the reviewer should check.\n")
	if strings.TrimSpace(projectInstructions) != "" {
		b.WriteString("\nProject instructions:\n")
		b.WriteString(projectInstructions)
		b.WriteString("\n")
	}
	return b.String()
}

// runOutcome collects what the event stream told us.
type runOutcome struct {
	done      *runnermgr.Event
	paused    *runnermgr.Event
	lastError *runnermgr.Event
	usage     *runnermgr.Event
	committed bool
}

// maxReattach bounds re-attaching to a runner whose output stream was cut
// while the container kept running (e.g. the Docker proxy's idle timeout).
const maxReattach = 50

// supervise streams events until the runner exits, then waits for the exit
// code and applies it. A stream that ends while the container still runs is
// re-attached; events.jsonl fills any gap.
func (s *Scheduler) supervise(ctx context.Context, t *store.Task, run *store.Run, r *runnermgr.Runner, started time.Time) {
	var baseline float64
	if run.Kind == tasks.RunResume || run.Kind == tasks.RunFollowup {
		baseline, _ = s.Store.SessionBaselineCost(ctx, t.ID, run.ID)
	}
	var out runOutcome
	reattached := 0
	for {
		select {
		case ev, ok := <-r.Events:
			if !ok {
				running, err := s.Docker.IsRunning(ctx, r.ContainerID)
				if err != nil || !running || reattached >= maxReattach {
					goto exited
				}
				r.Close()
				nr, err := s.Docker.Attach(ctx, r.ContainerID)
				if err != nil {
					s.log(t, run.ID).Error("re-attach after stream end", "err", err)
					goto exited
				}
				reattached++
				s.log(t, run.ID).Warn("runner output stream ended while running; re-attached", "count", reattached)
				r = nr
				s.register(t.ID, run.ID, r)
				s.replayFile(ctx, t, run.ID, &out)
				continue
			}
			s.handleEvent(ctx, t, run.ID, baseline, ev, &out)
		case line, ok := <-r.BadLines:
			if ok {
				s.log(t, run.ID).Warn("invalid runner output", "line", line)
			}
		}
	}
exited:
	r.Close()
	code, err := s.Docker.Wait(ctx, r.ContainerID)
	if err != nil {
		s.log(t, run.ID).Error("wait runner", "err", err)
	}
	// Anything the stream missed (a cut connection) is in the event log.
	s.replayFile(ctx, t, run.ID, &out)
	s.finishRun(ctx, t.ID, run, code, out, started)
}

// handleEvent persists one runner event, publishes it and applies its side effects.
func (s *Scheduler) handleEvent(ctx context.Context, t *store.Task, runID int64, baseline float64, ev runnermgr.Event, out *runOutcome) {
	switch ev.Type {
	case runnermgr.EvDone:
		out.done = &ev
	case runnermgr.EvPaused:
		out.paused = &ev
	case runnermgr.EvError:
		out.lastError = &ev
	case runnermgr.EvUsage:
		out.usage = &ev
	case runnermgr.EvCommitted:
		out.committed = true
	}
	if ev.Type == runnermgr.EvHeartbeat {
		return
	}
	stored, err := s.Store.AddRunnerEvent(ctx, t.ID, runID, ev.Seq, ev.Type, ev.Raw)
	if err != nil {
		s.log(t, runID).Error("persist event", "err", err, "type", ev.Type)
		return
	}
	if stored == nil {
		return // already persisted (replay)
	}
	s.Hub.PublishEvent(stored)

	switch ev.Type {
	case runnermgr.EvStarted, runnermgr.EvDone, runnermgr.EvPaused:
		if ev.SessionID != "" {
			_ = s.Store.SetSessionID(ctx, t.ID, ev.SessionID)
		}
	}
	switch ev.Type {
	case runnermgr.EvToolUse:
		if ev.Summary != "" {
			if nt, err := s.Store.SetLastActivity(ctx, t.ID, ev.Summary); err == nil {
				s.Hub.PublishTask(nt)
			}
		}
	case runnermgr.EvQuestion:
		q := store.Question{ID: tasks.QuestionID(runID, ev.QuestionID), TaskID: t.ID, RunID: runID, Text: ev.Text, Options: ev.Options}
		if _, err := s.Store.CreateQuestion(ctx, q); err != nil {
			s.log(t, runID).Error("store question", "err", err)
		}
		if nt, err := s.Store.SetStatus(ctx, t.ID, []string{tasks.Running}, tasks.WaitingInput, "", false); err == nil {
			s.Hub.PublishTask(nt)
		}
	case runnermgr.EvUsage, runnermgr.EvDone:
		cost := ev.CostUSDTotal - baseline
		if cost < 0 {
			cost = 0
		}
		if err := s.Store.SetRunCost(ctx, runID, cost); err != nil {
			s.log(t, runID).Error("set cost", "err", err)
		}
		if ev.Type == runnermgr.EvUsage {
			if nt, err := s.Store.SetRunTurns(ctx, runID, ev.Turns); err != nil {
				s.log(t, runID).Error("set turns", "err", err)
			} else {
				s.Hub.PublishTask(nt)
			}
		}
	}
}

// finishRun records the exit and moves the task on.
func (s *Scheduler) finishRun(ctx context.Context, taskID int64, run *store.Run, code int, out runOutcome, started time.Time) {
	s.unregister(taskID)
	first, _ := s.Store.FinishRun(ctx, run.ID, code)
	if first {
		s.addEvent(ctx, taskID, &run.ID, "run_finished", map[string]any{"run_id": run.ID, "exit_code": code})
	}
	if run.ContainerID != nil {
		_ = s.Docker.Remove(ctx, *run.ContainerID)
	}
	_ = s.Docker.Remove(ctx, runnermgr.RunnerName(taskID))
	t, err := s.Store.GetTask(ctx, taskID)
	if err != nil {
		return
	}
	if s.Hooks.RunFinished != nil {
		s.Hooks.RunFinished(t.ProjectID, code, time.Since(started))
	}
	log := s.log(t, run.ID)
	log.Info("runner exited", "exit_code", code)
	defer s.Wake()

	if t.Status == tasks.Cancelled {
		_ = s.Docker.RemoveAll(ctx, taskID)
		return
	}
	// The owner may have acted already (e.g. cancel then retry): the exit of
	// this run must not touch a newer run's state or containers.
	if !tasks.IsLive(t.Status) {
		log.Info("task moved on before the runner exit was handled", "status", t.Status)
		return
	}

	switch code {
	case runnermgr.ExitPaused:
		// An answer that arrived while the runner was exiting joins the queued
		// input, so moving to paused starts the resume run right away.
		if out.paused != nil {
			qid := tasks.QuestionID(run.ID, out.paused.QuestionID)
			if q, err := s.Store.GetQuestion(ctx, qid); err == nil && q.Answer != nil && q.AnsweredVia != nil && *q.AnsweredVia != "expired" {
				if _, _, err := s.Store.QueueMessage(ctx, taskID, []string{tasks.Running, tasks.WaitingInput}, tasks.ResumeInput(q.Text, *q.Answer), false); err != nil {
					log.Error("queue late answer", "err", err)
				}
			}
		}
		if _, err := s.Tasks.SetStatus(ctx, taskID, tasks.Paused, "no answer within the question timeout"); err != nil {
			log.Error("set paused", "err", err)
		}
	case runnermgr.ExitDone, runnermgr.ExitLimitExceeded:
		_ = s.Store.CloseOpenQuestions(ctx, taskID, "")
		limit := ""
		if code == runnermgr.ExitLimitExceeded {
			limit = "limit exceeded"
			if out.lastError != nil {
				limit = out.lastError.Message
			}
		}
		summary := ""
		if out.done != nil {
			summary = out.done.Summary
		}
		turns := 0
		if out.usage != nil {
			turns = out.usage.Turns
		}
		s.afterRun(ctx, t, summary, turns, limit)
	case runnermgr.ExitCancelled:
		_ = s.Docker.RemoveAll(ctx, taskID)
		nt, err := s.Store.SetStatus(ctx, taskID, []string{tasks.Running, tasks.WaitingInput}, tasks.Cancelled, "runner cancelled", true)
		if err != nil && !errors.Is(err, store.ErrConflict) {
			log.Error("set cancelled", "err", err)
		}
		s.Hub.PublishTask(nt)
	default:
		reason := fmt.Sprintf("runner exited with code %d", code)
		if out.lastError != nil {
			reason = out.lastError.Code + ": " + out.lastError.Message
		}
		s.fail(ctx, t, reason)
	}
}

// afterRun pushes new commits (opening or updating the PR) or completes the
// task when there is nothing to push. Also used to resume a crashed push.
func (s *Scheduler) afterRun(ctx context.Context, t *store.Task, summary string, turns int, limit string) {
	repo, err := s.Store.GetRepository(ctx, t.RepositoryID)
	if err != nil {
		s.fail(ctx, t, "repository: "+err.Error())
		return
	}
	branch := deref(t.Branch, "")
	unpushed, err := s.Workspace.UnpushedCommits(ctx, t.ID, repo.BaseBranch, branch)
	if err != nil {
		s.fail(ctx, t, "inspect workspace: "+err.Error())
		return
	}
	if unpushed == 0 {
		switch {
		case limit != "" && t.PRNumber == nil:
			s.fail(ctx, t, limit)
		case t.PRNumber != nil:
			if _, err := s.Tasks.SetStatus(ctx, t.ID, tasks.PROpen, limit); err != nil {
				s.log(t, 0).Error("set pr_open", "err", err)
			}
		default:
			_ = s.Docker.RemoveAll(ctx, t.ID)
			if _, err := s.Tasks.SetStatus(ctx, t.ID, tasks.Completed, "finished with no changes"); err != nil {
				s.log(t, 0).Error("set completed", "err", err)
			}
		}
		return
	}

	if t.Status != tasks.Pushing {
		if _, err := s.Tasks.SetStatus(ctx, t.ID, tasks.Pushing, ""); err != nil {
			s.log(t, 0).Error("set pushing", "err", err)
			return
		}
	}
	if s.Publisher == nil {
		_ = s.Docker.RemoveAll(ctx, t.ID)
		if _, err := s.Tasks.SetStatus(ctx, t.ID, tasks.Completed, "committed locally (GitHub App not configured)"); err != nil {
			s.log(t, 0).Error("set completed", "err", err)
		}
		return
	}
	project, err := s.Store.GetProject(ctx, t.ProjectID)
	if err != nil {
		s.fail(ctx, t, err.Error())
		return
	}
	latest, _ := s.Store.GetTask(ctx, t.ID)
	if latest != nil {
		t = latest
	}
	res, err := s.Publisher.Publish(ctx, PublishInput{
		Task: t, Repo: repo, Project: project, Branch: branch,
		Summary: summary, CostUSD: t.CostUSDTotal, Turns: turns, Limit: limit,
	})
	if err != nil {
		if errors.Is(err, ErrRejected) {
			s.addEvent(ctx, t.ID, nil, "diff_rejected", map[string]any{"reason": err.Error()})
		}
		s.fail(ctx, t, err.Error())
		return
	}
	if err := s.Store.SetPR(ctx, t.ID, res.PRNumber, res.PRURL); err != nil {
		s.log(t, 0).Error("set pr", "err", err)
	}
	typ := "pr_updated"
	if res.Created {
		typ = "pr_opened"
	}
	s.addEvent(ctx, t.ID, nil, typ, map[string]any{"number": res.PRNumber, "url": res.PRURL})
	if _, err := s.Tasks.SetStatus(ctx, t.ID, tasks.PROpen, limit); err != nil {
		s.log(t, 0).Error("set pr_open", "err", err)
	}
}

func (s *Scheduler) addEvent(ctx context.Context, taskID int64, runID *int64, typ string, payload any) {
	ev, err := s.Store.AddBackendEvent(ctx, taskID, runID, typ, payload)
	if err != nil {
		slog.Error("add event", "task_id", taskID, "type", typ, "err", err)
		return
	}
	s.Hub.PublishEvent(ev)
}

func deref[T any](p *T, def T) T {
	if p == nil {
		return def
	}
	return *p
}
