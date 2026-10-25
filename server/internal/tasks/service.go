package tasks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/andrewsl105/fleetpocket/server/internal/attachments"
	"github.com/andrewsl105/fleetpocket/server/internal/events"
	"github.com/andrewsl105/fleetpocket/server/internal/store"
)

var (
	ErrNotFound   = store.ErrNotFound
	ErrConflict   = store.ErrConflict
	ErrInvalid    = errors.New("invalid input")
	ErrNotRunning = errors.New("task has no live runner")
	// ErrNotMergeable is GitHub refusing a merge (conflicts, required
	// checks or reviews, head moved); the message says why.
	ErrNotMergeable   = errors.New("pull request is not mergeable")
	ErrGitHubDisabled = errors.New("GitHub App is not configured")
)

// Merger merges a task's PR on GitHub (githubapp.App). Nil when the GitHub
// App is not configured.
type Merger interface {
	MergePR(ctx context.Context, fullName string, installationID int64, number int, branch, title string) error
}

// Executor is the execution side (scheduler + runner manager). It is
// optional so the API works before execution is wired in.
type Executor interface {
	// Wake nudges the scheduler to look for queued tasks.
	Wake()
	// Answer and Interrupt write to a live runner's stdin.
	Answer(taskID int64, runnerQuestionID, text string) error
	Interrupt(taskID int64) error
	// Cancel stops a task's runner (if any) and cleans up its containers.
	Cancel(ctx context.Context, taskID int64) error
}

type Service struct {
	Store    *store.Store
	Hub      *events.Hub
	Executor Executor
	// Files stores attachments; nil rejects uploads.
	Files *attachments.Files
	// GitHub merges PRs; nil rejects Merge with ErrGitHubDisabled.
	GitHub Merger
}

// View is a task with its latest run, open question and PR info.
type View struct {
	*store.Task
	Repository   *store.Repository `json:"repository"`
	LatestRun    *store.Run        `json:"latest_run"`
	OpenQuestion *store.Question   `json:"open_question"`
}

type CreateInput struct {
	ProjectID    string `json:"project_id"`
	RepositoryID int64  `json:"repository_id"`
	Title        string `json:"title"`
	Prompt       string `json:"prompt"`
	Source       string `json:"-"`
	// Images are validated uploads (attachments.Process).
	Images []*attachments.Image `json:"-"`
}

func (s *Service) Create(ctx context.Context, in CreateInput) (*View, error) {
	in.Prompt = strings.TrimSpace(in.Prompt)
	if in.Prompt == "" {
		return nil, fmt.Errorf("%w: prompt is required", ErrInvalid)
	}
	p, err := s.Store.GetProject(ctx, in.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("project %q: %w", in.ProjectID, err)
	}
	if p.Archived {
		return nil, fmt.Errorf("%w: project %q is archived", ErrInvalid, p.ID)
	}
	repoID := in.RepositoryID
	if repoID == 0 {
		for _, r := range p.Repositories {
			if r.IsDefault {
				repoID = r.ID
			}
		}
		if repoID == 0 {
			return nil, fmt.Errorf("%w: project %q has no repositories", ErrInvalid, p.ID)
		}
	} else if !hasRepo(p, repoID) {
		return nil, fmt.Errorf("%w: repository %d does not belong to project %q", ErrInvalid, repoID, p.ID)
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = TitleFromPrompt(in.Prompt)
	}
	if in.Source == "" {
		in.Source = "api"
	}
	up, release, err := s.upload(in.Images)
	if err != nil {
		return nil, err
	}
	t, err := s.Store.CreateTask(ctx, store.Task{ProjectID: p.ID, RepositoryID: repoID, Title: title, Prompt: in.Prompt, Source: in.Source}, up)
	release()
	if err != nil {
		return nil, err
	}
	s.publishTask(t)
	s.wake()
	return s.Get(ctx, t.ID)
}

// upload turns validated images into a store.Upload. release must be called
// once the store call that inserts them has returned (it holds off sweeps).
func (s *Service) upload(imgs []*attachments.Image) (*store.Upload, func(), error) {
	if len(imgs) == 0 {
		return nil, func() {}, nil
	}
	if s.Files == nil {
		return nil, nil, fmt.Errorf("%w: attachments are not configured", ErrInvalid)
	}
	if len(imgs) > attachments.MaxFiles {
		return nil, nil, fmt.Errorf("%w: at most %d images per request", ErrInvalid, attachments.MaxFiles)
	}
	up := &store.Upload{Place: s.Files.Place(imgs)}
	for _, im := range imgs {
		up.Items = append(up.Items, store.NewAttachment{Name: im.Name, Mime: im.Mime, Size: int64(len(im.Data)), Width: im.Width, Height: im.Height})
	}
	return up, s.Files.Hold(), nil
}

func hasRepo(p *store.Project, id int64) bool {
	for _, r := range p.Repositories {
		if r.ID == id {
			return true
		}
	}
	return false
}

// TitleFromPrompt is the first line of the prompt, at most 80 characters.
func TitleFromPrompt(prompt string) string {
	line := strings.TrimSpace(strings.SplitN(prompt, "\n", 2)[0])
	if utf8.RuneCountInString(line) > 80 {
		r := []rune(line)
		line = strings.TrimSpace(string(r[:77])) + "…"
	}
	return line
}

func (s *Service) Get(ctx context.Context, id int64) (*View, error) {
	t, err := s.Store.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.view(ctx, t)
}

func (s *Service) view(ctx context.Context, t *store.Task) (*View, error) {
	v := &View{Task: t}
	var err error
	if v.Repository, err = s.Store.GetRepository(ctx, t.RepositoryID); err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if v.LatestRun, err = s.Store.LatestRun(ctx, t.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if v.OpenQuestion, err = s.Store.OpenQuestion(ctx, t.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	return v, nil
}

func (s *Service) List(ctx context.Context, f store.TaskFilter) ([]*store.Task, error) {
	for _, st := range f.Statuses {
		if !Valid(st) {
			return nil, fmt.Errorf("%w: unknown status %q", ErrInvalid, st)
		}
	}
	return s.Store.ListTasks(ctx, f)
}

// Answer records the first answer to a question and delivers it: to the live
// runner, or by resuming a paused task.
func (s *Service) Answer(ctx context.Context, taskID int64, questionID, text, via string) (*View, error) {
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("%w: text is required", ErrInvalid)
	}
	t, err := s.Store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	qn, err := s.Store.AnswerQuestion(ctx, questionID, taskID, text, via)
	if err != nil {
		return nil, err
	}
	ev, _ := s.Store.AddBackendEvent(ctx, taskID, &qn.RunID, "answer", map[string]any{
		"question_id": qn.ID, "text": text, "answered_via": via,
	})
	s.publishEvent(ev)

	switch t.Status {
	case Running, WaitingInput:
		// If the runner is already gone, the runner manager sees the stored
		// answer when it handles the pause and resumes the task.
		if s.Executor != nil && s.Executor.Answer(taskID, RunnerQuestionID(qn.ID), text) == nil {
			if nt, err := s.Store.SetStatus(ctx, taskID, []string{WaitingInput}, Running, "", false); err == nil {
				s.publishTask(nt)
			}
		}
	case Paused:
		nt, err := s.Store.Requeue(ctx, taskID, []string{Paused}, RunResume, ResumeInput(qn.Text, text))
		if err != nil {
			return nil, err
		}
		s.publishTask(nt)
		s.wake()
	}
	return s.Get(ctx, taskID)
}

// Message queues text while a run is pending or active (it becomes the next
// run when the current one ends) or starts a follow-up run right away. imgs
// are attached to the message (validated uploads; may be nil).
func (s *Service) Message(ctx context.Context, taskID int64, text string, imgs []*attachments.Image) (*View, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("%w: text is required", ErrInvalid)
	}
	// The task can move on between reading and writing (a run ending); the
	// conditional writes then fail with ErrConflict and the state is re-read.
	for attempt := 0; ; attempt++ {
		t, err := s.Store.GetTask(ctx, taskID)
		if err != nil {
			return nil, err
		}
		err = s.message(ctx, t, text, imgs)
		if errors.Is(err, ErrConflict) && attempt < 2 {
			continue
		}
		if err != nil {
			return nil, err
		}
		return s.Get(ctx, taskID)
	}
}

func (s *Service) message(ctx context.Context, t *store.Task, text string, imgs []*attachments.Image) error {
	action := MessageActionFor(t.Status)
	if action != MessageQueue && action != MessageNewRun {
		return fmt.Errorf("%w: cannot send a message to a task in state %s", ErrConflict, t.Status)
	}
	up, release, err := s.upload(imgs)
	if err != nil {
		return err
	}
	defer release()
	switch action {
	case MessageQueue:
		nt, ev, err := s.Store.QueueMessage(ctx, t.ID, QueueStates, text, true, up)
		if err != nil {
			return err
		}
		s.publishEvent(ev)
		s.publishTask(nt)
	case MessageNewRun:
		kind := RunFollowup
		if t.Status == Paused {
			kind = RunResume
		}
		nt, ev, err := s.Store.RequeueMessage(ctx, t.ID, []string{t.Status}, kind, text, up)
		if err != nil {
			return err
		}
		// The status events first, then the message, as before.
		s.publishTask(nt)
		s.publishEvent(ev)
		s.wake()
	}
	return nil
}

func (s *Service) Interrupt(ctx context.Context, taskID int64) (*View, error) {
	t, err := s.Store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if !IsLive(t.Status) || s.Executor == nil {
		return nil, fmt.Errorf("%w: task is %s", ErrConflict, t.Status)
	}
	if err := s.Executor.Interrupt(taskID); err != nil {
		return nil, err
	}
	ev, _ := s.Store.AddBackendEvent(ctx, taskID, nil, "interrupt", map[string]any{})
	s.publishEvent(ev)
	return s.Get(ctx, taskID)
}

// Cancel ends a task. A live runner is cancelled and the task is marked
// cancelled right away; the runner's exit is then ignored.
func (s *Service) Cancel(ctx context.Context, taskID int64) (*View, error) {
	t, err := s.Store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if IsTerminal(t.Status) {
		return nil, fmt.Errorf("%w: task is already %s", ErrConflict, t.Status)
	}
	nt, err := s.Store.SetStatus(ctx, taskID, From(Cancelled), Cancelled, "cancelled by owner", true)
	if err != nil {
		return nil, err
	}
	s.publishTask(nt)
	if s.Executor != nil {
		if err := s.Executor.Cancel(ctx, taskID); err != nil {
			return nil, err
		}
	}
	return s.Get(ctx, taskID)
}

// Retry queues a fresh run of the original prompt on the same branch.
func (s *Service) Retry(ctx context.Context, taskID int64) (*View, error) {
	t, err := s.Store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if t.Status != Failed && t.Status != Cancelled {
		return nil, fmt.Errorf("%w: only failed or cancelled tasks can be retried (task is %s)", ErrConflict, t.Status)
	}
	nt, err := s.Store.Requeue(ctx, taskID, []string{Failed, Cancelled}, RunRetry, t.Prompt)
	if err != nil {
		return nil, err
	}
	s.publishTask(nt)
	s.wake()
	return s.Get(ctx, taskID)
}

// Merge squash-merges the PR of a pr_open task, deletes its branch and
// completes the task. The pull_request webhook that follows finds the task
// already completed and records nothing twice.
func (s *Service) Merge(ctx context.Context, taskID int64) (*View, error) {
	t, err := s.Store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if t.Status != PROpen || t.PRNumber == nil {
		return nil, fmt.Errorf("%w: only a task with an open PR can be merged (task is %s)", ErrConflict, t.Status)
	}
	if s.GitHub == nil {
		return nil, ErrGitHubDisabled
	}
	repo, err := s.Store.GetRepository(ctx, t.RepositoryID)
	if err != nil {
		return nil, err
	}
	branch := ""
	if t.Branch != nil {
		branch = *t.Branch
	}
	if err := s.GitHub.MergePR(ctx, repo.FullName, repo.InstallationID, *t.PRNumber, branch, t.Title); err != nil {
		return nil, err
	}
	ev, _ := s.Store.AddBackendEvent(ctx, taskID, nil, "pr_closed", map[string]any{"number": *t.PRNumber, "merged": true})
	s.publishEvent(ev)
	// Only from pr_open: a follow-up started meanwhile keeps running.
	if nt, err := s.Store.SetStatus(ctx, taskID, []string{PROpen}, Completed, "PR merged", true); err == nil {
		s.publishTask(nt)
		if s.Executor != nil {
			_ = s.Executor.Cancel(ctx, taskID) // removes idle sidecars
		}
	} else if !errors.Is(err, ErrConflict) {
		return nil, err
	}
	return s.Get(ctx, taskID)
}

// SetStatus applies a state machine transition and publishes it.
func (s *Service) SetStatus(ctx context.Context, taskID int64, to, reason string) (*store.Task, error) {
	t, err := s.Store.SetStatus(ctx, taskID, From(to), to, reason, IsTerminal(to))
	if err != nil {
		return nil, err
	}
	s.publishTask(t)
	return t, nil
}

func (s *Service) wake() {
	if s.Executor != nil {
		s.Executor.Wake()
	}
}

func (s *Service) publishTask(t *store.Task) {
	if s.Hub != nil {
		s.Hub.PublishTask(t)
	}
}

func (s *Service) publishEvent(ev *store.Event) {
	if s.Hub != nil && ev != nil {
		s.Hub.PublishEvent(ev)
	}
}

// QuestionID is the stored id of a runner question: "<run_id>-<question_id>".
func QuestionID(runID int64, runnerQuestionID string) string {
	return fmt.Sprintf("%d-%s", runID, runnerQuestionID)
}

// RunnerQuestionID strips the run prefix again.
func RunnerQuestionID(id string) string {
	if _, rest, ok := strings.Cut(id, "-"); ok {
		return rest
	}
	return id
}

// ResumeInput is the prompt of a run that resumes a session after a question
// timed out: the original tool call is gone, so the answer is restated.
func ResumeInput(question, answer string) string {
	return fmt.Sprintf("Answer to your earlier question %q: %s", question, answer)
}
