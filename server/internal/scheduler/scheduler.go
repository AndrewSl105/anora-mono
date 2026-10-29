// Package scheduler claims queued tasks within the concurrency limit and
// drives each run: workspace, network, sidecars, runner container, event
// stream, and the state transition when the runner exits.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andrewsl105/fleetpocket/server/internal/attachments"
	"github.com/andrewsl105/fleetpocket/server/internal/config"
	"github.com/andrewsl105/fleetpocket/server/internal/events"
	"github.com/andrewsl105/fleetpocket/server/internal/hosts"
	"github.com/andrewsl105/fleetpocket/server/internal/runnermgr"
	"github.com/andrewsl105/fleetpocket/server/internal/store"
	"github.com/andrewsl105/fleetpocket/server/internal/tasks"
	"github.com/andrewsl105/fleetpocket/server/internal/workspace"
)

const (
	pollInterval = 5 * time.Second
	cancelGrace  = 15 * time.Second
	commitAuthor = "fleetpocket[bot]"
	commitEmail  = "fleetpocket[bot]@users.noreply.github.com"
)

// ErrRejected marks a diff validation failure: the task fails and the
// workspace is kept for inspection.
var ErrRejected = errors.New("diff rejected")

// PublishInput is what the publisher needs to push and open/update the PR.
type PublishInput struct {
	// Host holds the task's workspace (validate, push, diff stat).
	Host    hosts.Host
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
	Cfg   *config.Config
	Store *store.Store
	Hub   *events.Hub
	Tasks *tasks.Service
	// Hosts resolves a task's placement (the server or a node).
	Hosts     *hosts.Registry
	Publisher Publisher // nil: commits stay local (phase 3 / GitHub not configured)
	Hooks     Hooks
	// Files holds task attachments, mounted read-only into runners; nil = none.
	Files *attachments.Files

	wake chan struct{}
	mu   sync.Mutex
	live map[int64]*liveRun
	// pushing holds tasks with an afterRun in flight.
	pushing map[int64]bool
	wg      sync.WaitGroup
}

type liveRun struct {
	runner *runnermgr.Runner
	runID  int64
	host   hosts.Host
}

// New initialises a Scheduler whose dependencies are already set.
func New(s *Scheduler) *Scheduler {
	s.wake = make(chan struct{}, 1)
	s.live = map[int64]*liveRun{}
	s.pushing = map[int64]bool{}
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

// host is the task's host: the server, or the node it was placed on.
func (s *Scheduler) host(t *store.Task) hosts.Host { return s.Hosts.For(t.Node) }

// Cancel asks a live runner to stop (SIGTERM after a grace period) and removes
// the task's containers; a task without a runner just loses its sidecars.
// A node that is offline is told again when it reconnects.
func (s *Scheduler) Cancel(ctx context.Context, taskID int64) error {
	s.mu.Lock()
	l, ok := s.live[taskID]
	s.mu.Unlock()
	if !ok {
		t, err := s.Store.GetTask(ctx, taskID)
		if err != nil {
			return err
		}
		if err := s.host(t).RemoveAll(ctx, taskID); err != nil && !hosts.IsOffline(err) {
			return err
		}
		return nil
	}
	r := l.runner
	_ = r.Send(runnermgr.Cancel())
	go func() {
		select {
		case <-r.Done():
		case <-time.After(cancelGrace):
			_ = l.host.Stop(context.Background(), r.ContainerID, 5*time.Second)
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
		t, err := s.Store.ClaimNext(ctx, s.Cfg.MaxConcurrentTasks, s.Hosts.Online())
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
	_ = s.host(t).RemoveAll(ctx, t.ID)
	_ = s.Store.CloseOpenQuestions(ctx, t.ID, "")
	if _, err := s.Tasks.SetStatus(ctx, t.ID, tasks.Failed, reason); err != nil && !errors.Is(err, store.ErrConflict) {
		slog.Error("set failed", "task_id", t.ID, "err", err)
	}
	s.Wake()
}

// failOrRequeue fails the task, unless its node went offline while it was
// being prepared: then it waits in the queue for the node to come back.
func (s *Scheduler) failOrRequeue(ctx context.Context, t *store.Task, run *store.Run, reason string, err error) {
	if hosts.IsOffline(err) && t.Node != nil {
		s.log(t, run.ID).Info("node went offline while preparing; requeued", "node", *t.Node)
		_ = s.Store.ResetRunStart(ctx, run.ID)
		if nt, err := s.Store.SetStatus(ctx, t.ID, []string{tasks.Preparing}, tasks.Queued, "waiting for "+*t.Node, false); err == nil {
			s.Hub.PublishTask(nt)
		}
		return
	}
	if reason != "" {
		reason += ": "
	}
	s.fail(ctx, t, reason+err.Error())
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
	h := s.host(t)
	wsRepo := workspace.Repo{FullName: repo.FullName, InstallationID: repo.InstallationID, BaseBranch: repo.BaseBranch}
	if err := h.PrepareWorkspace(ctx, t.ID, wsRepo, branch); err != nil {
		s.failOrRequeue(ctx, t, run, "prepare workspace", err)
		return
	}
	image := deref(project.RunnerImage, s.Cfg.RunnerImage)
	if err := h.HasImage(ctx, image); err != nil {
		s.failOrRequeue(ctx, t, run, "", err)
		return
	}
	if err := h.EnsureNetwork(ctx, t.ID, t.ProjectID); err != nil {
		s.failOrRequeue(ctx, t, run, "network", err)
		return
	}
	if err := h.EnsureVolume(ctx, t.ID, t.ProjectID); err != nil {
		s.failOrRequeue(ctx, t, run, "volume", err)
		return
	}
	svcEnv, err := h.EnsureSidecars(ctx, t.ID, t.ProjectID, project.Services)
	if err != nil {
		s.failOrRequeue(ctx, t, run, "", err)
		return
	}

	env := map[string]string{}
	for k, v := range project.Env {
		env[k] = v
	}
	for k, v := range svcEnv {
		env[k] = v
	}
	if s.Cfg.RunnerFake {
		env["FLEETPOCKET_RUNNER_FAKE"] = "1"
	}

	cmd := s.runCommand(t, run, project, repo, branch)
	hasAtt, err := s.prepareAttachments(ctx, h, t.ID, run, &cmd)
	if err != nil {
		s.failOrRequeue(ctx, t, run, "attachments", err)
		return
	}
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
	// The host adds the agent's credentials: the server's own, or the
	// node's (docs/design/remote-nodes.md, section 4).
	r, err := h.StartRunner(ctx, hosts.StartSpec{
		TaskID: t.ID, RunID: run.ID, Project: t.ProjectID, Image: image, Agent: t.Agent,
		Env: env, Attachments: hasAtt,
		Memory: deref(project.RunnerMemory, s.Cfg.RunnerMemory),
		CPUs:   deref(project.RunnerCPUs, s.Cfg.RunnerCPUs),
		User:   s.Cfg.RunnerUser,
	})
	if err != nil {
		if hosts.IsOffline(err) {
			// Back to the queue; if the container did start, the node's next
			// hello reports it and reconciliation removes it (its run is reset).
			s.failOrRequeue(ctx, t, run, "", err)
			return
		}
		_, _ = s.Store.FinishRun(ctx, run.ID, -1)
		s.fail(ctx, t, err.Error())
		return
	}
	_ = s.Store.SetRunContainer(ctx, run.ID, r.ContainerID)
	// Registered before the status says running, so a node reconnecting in
	// between does not take the task for one whose runner is lost.
	s.register(t.ID, run.ID, h, r)

	nt, err := s.Tasks.SetStatus(ctx, t.ID, tasks.Running, "")
	if err != nil {
		// Cancelled while preparing.
		log.Info("task left preparing before start", "err", err)
		s.unregister(t.ID)
		_ = r.Send(runnermgr.Cancel())
		r.Close()
		_ = h.Remove(ctx, r.ContainerID)
		_, _ = s.Store.FinishRun(ctx, run.ID, runnermgr.ExitCancelled)
		_ = h.RemoveAll(ctx, t.ID)
		return
	}
	t = nt
	s.addEvent(ctx, t.ID, &run.ID, "run_started", map[string]any{"run_id": run.ID, "kind": run.Kind})
	log.Info("runner started", "container", short(r.ContainerID), "kind", run.Kind)

	if err := r.Send(cmd); err != nil {
		log.Error("send run", "err", err)
	}
	if run.Kind == tasks.RunResume {
		s.replayAnswer(ctx, t.ID, run.ID, r)
	}
	s.supervise(ctx, h, t, run, r, time.Now())
}

func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
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

func (s *Scheduler) register(taskID, runID int64, h hosts.Host, r *runnermgr.Runner) {
	s.mu.Lock()
	s.live[taskID] = &liveRun{runner: r, runID: runID, host: h}
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
		Agent:              t.Agent,
		Model:              s.model(t, p),
		Limits: runnermgr.Limits{
			MaxTurns:           maxTurns,
			MaxBudgetUSD:       budget,
			MaxDurationSec:     int(dur.Seconds()),
			QuestionTimeoutSec: int(s.Cfg.QuestionTimeout.Seconds()),
		},
		Commit: runnermgr.CommitInfo{
			Message:     fmt.Sprintf("%s\n\nFleetpocket task #%d", msg, t.ID),
			AuthorName:  commitAuthor,
			AuthorEmail: commitEmail,
		},
	})
}

// model is the task's model: the project's when the task runs with the
// project's agent (it names a model of that agent), else DEFAULT_MODEL for
// Claude, else empty so Codex and Cursor use their own default.
func (s *Scheduler) model(t *store.Task, p *store.Project) string {
	if p.Model != nil && t.Agent == tasks.ProjectAgent(p, s.Cfg.DefaultAgent) {
		return *p.Model
	}
	if t.Agent == "claude" {
		return s.Cfg.DefaultModel
	}
	return ""
}

// prepareAttachments lists the run's attachments at the end of its prompt and
// makes them available on the host. It reports whether there are any (when
// not, nothing is mounted at /attachments).
func (s *Scheduler) prepareAttachments(ctx context.Context, h hosts.Host, taskID int64, run *store.Run, cmd *runnermgr.RunCommand) (bool, error) {
	if s.Files == nil {
		return false, nil
	}
	cur, earlier, err := s.Store.AttachmentsForRun(ctx, taskID, run.ID, run.Kind == tasks.RunRetry)
	if err != nil || len(cur)+len(earlier) == 0 {
		return false, err
	}
	var files []hosts.AttachmentFile
	for _, a := range append(append([]*store.Attachment{}, cur...), earlier...) {
		files = append(files, hosts.AttachmentFile{Name: path.Base(attachments.AgentPath(a)), Path: s.Files.Path(a)})
	}
	if err := h.PrepareAttachments(ctx, taskID, files); err != nil {
		return false, err
	}
	cmd.Prompt += AttachmentBlock(cmd.Agent, cur, earlier)
	return true, nil
}

// AttachmentBlock is appended to a run's prompt: the images that came with
// this input and the task's earlier ones, as paths in the agent container.
// Claude opens them with its Read tool; the Codex and Cursor runners pass the
// "Attached images" to the model as image inputs (see docs/PROTOCOL.md).
func AttachmentBlock(agent string, cur, earlier []*store.Attachment) string {
	if len(cur)+len(earlier) == 0 {
		return ""
	}
	var b strings.Builder
	list := func(as []*store.Attachment) {
		for _, a := range as {
			fmt.Fprintf(&b, "- %s (%s, %dx%d)\n", attachments.AgentPath(a), a.Name, a.Width, a.Height)
		}
	}
	claude := agent == "" || agent == "claude"
	if len(cur) > 0 {
		if claude {
			b.WriteString("\n\nAttached images (open each with the Read tool before you start):\n")
		} else {
			b.WriteString("\n\nAttached images (included with this message):\n")
		}
		list(cur)
	}
	if len(earlier) > 0 {
		if claude {
			b.WriteString("\n\nEarlier attachments of this task (still available; Read them if they matter for this request):\n")
		} else {
			b.WriteString("\n\nEarlier attachments of this task (still available; open them if they matter for this request):\n")
		}
		list(earlier)
	}
	b.WriteString("\nThe folder " + attachments.AgentDir + " is read-only; copy an image into the repository only if the change needs it.\n")
	return b.String()
}

// SystemPrompt is appended to Claude Code's default system prompt (Codex: its
// developer instructions; Cursor: ahead of the first prompt).
func SystemPrompt(t *store.Task, repo *store.Repository, branch, projectInstructions string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are running unattended inside Fleetpocket on task #%d.\n", t.ID)
	fmt.Fprintf(&b, "Repository %s is checked out in /workspace on branch %s (base %s).\n", repo.FullName, branch, repo.BaseBranch)
	b.WriteString("Do not push, change git remotes or touch .github/: Fleetpocket commits your changes and opens the pull request.\n")
	if t.Agent == "" || t.Agent == "claude" {
		b.WriteString("If a decision needs the owner, ask with the AskUserQuestion tool and keep options short.\n")
	} else {
		// Only the Claude runner can ask (docs/DECISIONS.md, Agent).
		b.WriteString("Nobody can answer questions during the run: make reasonable choices yourself and list open questions in your summary.\n")
	}
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
func (s *Scheduler) supervise(ctx context.Context, h hosts.Host, t *store.Task, run *store.Run, r *runnermgr.Runner, started time.Time) {
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
				running, err := h.IsRunning(ctx, r.ContainerID)
				if err != nil || !running || reattached >= maxReattach {
					goto exited
				}
				r.Close()
				nr, err := h.Attach(ctx, r.ContainerID)
				if err != nil {
					s.log(t, run.ID).Error("re-attach after stream end", "err", err)
					goto exited
				}
				reattached++
				s.log(t, run.ID).Warn("runner output stream ended while running; re-attached", "count", reattached)
				r = nr
				s.register(t.ID, run.ID, h, r)
				s.replayFile(ctx, h, t, run.ID, &out)
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
	code, err := h.Wait(ctx, r.ContainerID)
	if err != nil {
		s.log(t, run.ID).Error("wait runner", "err", err)
	}
	// Anything the stream missed (a cut connection) is in the event log.
	s.replayFile(ctx, h, t, run.ID, &out)
	s.finishRun(ctx, h, t.ID, run, code, out, started)
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
	// The question row must exist before the event is published: subscribers
	// (the Telegram bot) look it up as soon as they see the event.
	if ev.Type == runnermgr.EvQuestion {
		q := store.Question{ID: tasks.QuestionID(runID, ev.QuestionID), TaskID: t.ID, RunID: runID, Text: ev.Text, Options: ev.Options}
		if _, err := s.Store.CreateQuestion(ctx, q); err != nil {
			s.log(t, runID).Error("store question", "err", err)
		}
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
func (s *Scheduler) finishRun(ctx context.Context, h hosts.Host, taskID int64, run *store.Run, code int, out runOutcome, started time.Time) {
	s.unregister(taskID)
	first, _ := s.Store.FinishRun(ctx, run.ID, code)
	if first {
		s.addEvent(ctx, taskID, &run.ID, "run_finished", map[string]any{"run_id": run.ID, "exit_code": code})
	}
	if run.ContainerID != nil {
		_ = h.Remove(ctx, *run.ContainerID)
	}
	if h.ID() == "" {
		_ = h.Remove(ctx, runnermgr.RunnerName(taskID))
	}
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
		_ = h.RemoveAll(ctx, taskID)
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
				if _, _, err := s.Store.QueueMessage(ctx, taskID, []string{tasks.Running, tasks.WaitingInput}, tasks.ResumeInput(q.Text, *q.Answer), false, nil); err != nil {
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
		_ = h.RemoveAll(ctx, taskID)
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
	// One push per task at a time (a node reconnect resumes pushes too).
	s.mu.Lock()
	if s.pushing[t.ID] {
		s.mu.Unlock()
		return
	}
	s.pushing[t.ID] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pushing, t.ID)
		s.mu.Unlock()
	}()
	repo, err := s.Store.GetRepository(ctx, t.RepositoryID)
	if err != nil {
		s.fail(ctx, t, "repository: "+err.Error())
		return
	}
	branch := deref(t.Branch, "")
	h := s.host(t)
	unpushed, err := h.UnpushedCommits(ctx, t.ID, repo.BaseBranch, branch)
	if err != nil {
		if hosts.IsOffline(err) {
			// Stays pushing; the node's reconnect resumes it.
			s.log(t, 0).Warn("node offline after the run; push resumes when it reconnects")
			if t.Status != tasks.Pushing {
				_, _ = s.Tasks.SetStatus(ctx, t.ID, tasks.Pushing, "waiting for "+deref(t.Node, ""))
			}
			return
		}
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
			_ = h.RemoveAll(ctx, t.ID)
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
		_ = h.RemoveAll(ctx, t.ID)
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
		Host: h, Task: t, Repo: repo, Project: project, Branch: branch,
		Summary: summary, CostUSD: t.CostUSDTotal, Turns: turns, Limit: limit,
	})
	if err != nil {
		if hosts.IsOffline(err) {
			s.log(t, 0).Warn("node offline while publishing; resumes when it reconnects")
			return
		}
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
