package scheduler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrewsl105/anora/server/internal/attachments"
	"github.com/andrewsl105/anora/server/internal/config"
	"github.com/andrewsl105/anora/server/internal/events"
	"github.com/andrewsl105/anora/server/internal/projects"
	"github.com/andrewsl105/anora/server/internal/runnermgr"
	"github.com/andrewsl105/anora/server/internal/scheduler"
	"github.com/andrewsl105/anora/server/internal/store"
	"github.com/andrewsl105/anora/server/internal/tasks"
	"github.com/andrewsl105/anora/server/internal/testutil"
	"github.com/andrewsl105/anora/server/internal/workspace"
)

// env is a running scheduler against real Docker with the fake runner.
type env struct {
	t      *testing.T
	st     *store.Store
	svc    *tasks.Service
	sched  *scheduler.Scheduler
	docker *runnermgr.Manager
	ws     *workspace.Manager
	files  *attachments.Files
	bare   string
	cancel context.CancelFunc
	done   chan struct{}
}

func runnerImage() string {
	if img := os.Getenv("ANORA_TEST_RUNNER_IMAGE"); img != "" {
		return img
	}
	return "anora-agent:dev"
}

func newEnv(t *testing.T, questionTimeout time.Duration, services []string) *env {
	t.Helper()
	st := testutil.DB(t)
	dc, err := runnermgr.NewDockerClient("")
	if err != nil {
		t.Skipf("docker client: %v", err)
	}
	docker := &runnermgr.Manager{Docker: dc, SidecarMemory: "256m"}
	ctx := context.Background()
	if err := docker.Ping(ctx); err != nil {
		t.Skipf("docker unavailable: %v", err)
	}
	if err := docker.HasImage(ctx, runnerImage()); err != nil {
		t.Skipf("runner image missing: %v", err)
	}

	bare := newBareRepo(t, "dev")
	ws := &workspace.Manager{Dir: filepath.Join(t.TempDir(), "workspaces"), RemoteURL: func(string) string { return bare }}
	cfg := &config.Config{
		RunnerImage: runnerImage(), RunnerFake: true, MaxConcurrentTasks: 2,
		RunnerMemory: "512m", RunnerCPUs: 1, SidecarMemory: "256m",
		DefaultMaxTurns: 10, DefaultMaxBudgetUSD: 1, DefaultMaxDuration: 2 * time.Minute,
		QuestionTimeout: questionTimeout,
		RunnerUser:      fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
	}
	hub := events.NewHub()
	files := &attachments.Files{Dir: filepath.Join(t.TempDir(), "attachments")}
	svc := &tasks.Service{Store: st, Hub: hub, Files: files}
	sched := scheduler.New(&scheduler.Scheduler{Cfg: cfg, Store: st, Hub: hub, Tasks: svc, Docker: docker, Workspace: ws, Files: files})
	svc.Executor = sched

	ps := &projects.Service{Store: st}
	name := "Demo"
	if _, err := ps.Create(ctx, projects.CreateInput{ID: "demo", ProjectFields: store.ProjectFields{Name: &name, Services: &services}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.AddRepository(ctx, "demo", projects.RepoInput{FullName: "o/demo", InstallationID: 1, BaseBranch: "dev"}); err != nil {
		t.Fatal(err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	e := &env{t: t, st: st, svc: svc, sched: sched, docker: docker, ws: ws, files: files, bare: bare, cancel: cancel, done: make(chan struct{})}
	go func() { _ = sched.Run(runCtx); close(e.done) }()
	t.Cleanup(func() {
		cancel()
		<-e.done
		all, _ := st.ListTasks(context.Background(), store.TaskFilter{Limit: 100})
		for _, tk := range all {
			_ = docker.RemoveAll(context.Background(), tk.ID)
			_ = docker.RemoveVolume(context.Background(), tk.ID)
		}
	})
	return e
}

func (e *env) waitStatus(id int64, want string, timeout time.Duration) *store.Task {
	e.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		t, err := e.st.GetTask(context.Background(), id)
		if err != nil {
			e.t.Fatal(err)
		}
		if t.Status == want {
			return t
		}
		if tasks.IsTerminal(t.Status) && !tasks.IsTerminal(want) || time.Now().After(deadline) {
			e.t.Fatalf("task %d: status %s (reason %v), want %s", id, t.Status, deref(t.StatusReason), want)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (e *env) eventTypes(id int64) []string {
	evs, err := e.st.ListEvents(context.Background(), id, 0, 500)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, ev := range evs {
		out = append(out, ev.Type)
	}
	return out
}

func TestFakeRunQueuedToCommit(t *testing.T) {
	e := newEnv(t, time.Minute, []string{"redis"})
	ctx := context.Background()
	v, err := e.svc.Create(ctx, tasks.CreateInput{ProjectID: "demo", Prompt: "Say hello", Source: "api"})
	if err != nil {
		t.Fatal(err)
	}
	e.waitStatus(v.ID, tasks.WaitingInput, 2*time.Minute)

	view, _ := e.svc.Get(ctx, v.ID)
	if view.OpenQuestion == nil || len(view.OpenQuestion.Options) != 2 {
		t.Fatalf("open question: %+v", view.OpenQuestion)
	}
	if _, err := e.svc.Answer(ctx, v.ID, view.OpenQuestion.ID, "hello", "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Answer(ctx, v.ID, view.OpenQuestion.ID, "again", "web"); err != store.ErrConflict {
		t.Fatalf("second answer: %v", err)
	}
	done := e.waitStatus(v.ID, tasks.Completed, time.Minute)
	if !strings.Contains(deref(done.StatusReason), "committed locally") {
		t.Fatalf("reason %q", deref(done.StatusReason))
	}

	// The commit is on the task branch in the workspace.
	wsDir := e.ws.Path(v.ID)
	out, err := exec.Command("git", "-C", wsDir, "log", "-1", "--format=%an|%s|%D").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), "anora[bot]|Say hello|") || !strings.Contains(string(out), fmt.Sprintf("agent/%d-say-hello", v.ID)) {
		t.Fatalf("commit: %s", out)
	}
	if b, _ := os.ReadFile(filepath.Join(wsDir, "ANORA_FAKE.md")); !strings.HasPrefix(string(b), "# hello") {
		t.Fatalf("file: %q", b)
	}

	types := strings.Join(e.eventTypes(v.ID), ",")
	for _, want := range []string{"run_started", "started", "question", "answer", "committed", "done", "run_finished"} {
		if !strings.Contains(types, want) {
			t.Errorf("missing event %s in %s", want, types)
		}
	}
	run, _ := e.st.LatestRun(ctx, v.ID)
	if run.ExitCode == nil || *run.ExitCode != 0 || run.LastSeq == 0 {
		t.Fatalf("run: %+v", run)
	}
	final, _ := e.st.GetTask(ctx, v.ID)
	if final.CostUSDTotal != 0.01 || final.SessionID == nil {
		t.Fatalf("cost %v session %v", final.CostUSDTotal, final.SessionID)
	}
	if final.Turns != 2 || run.Turns != 2 {
		t.Fatalf("turns: task %d run %d", final.Turns, run.Turns)
	}
	if final.LastActivityAt == nil || !strings.HasPrefix(deref(final.LastActivity), "completed: committed locally") {
		t.Fatalf("last activity %q at %v", deref(final.LastActivity), final.LastActivityAt)
	}
	// Runner, sidecar and network are gone; the state volume stays until cleanup.
	ids, _ := e.docker.TaskIDsWithResources(ctx)
	if ids[v.ID] {
		out, _ := exec.Command("sh", "-c", "docker ps -a --filter label=anora.task_id --format '{{.Names}} {{.Status}}'; docker network ls --filter label=anora.task_id --format '{{.Name}}'").CombinedOutput()
		t.Fatalf("task resources left behind:\n%s", out)
	}
}

func TestFakeRunPauseAndResume(t *testing.T) {
	e := newEnv(t, 2*time.Second, nil)
	ctx := context.Background()
	v, err := e.svc.Create(ctx, tasks.CreateInput{ProjectID: "demo", Prompt: "Pause me", Source: "api"})
	if err != nil {
		t.Fatal(err)
	}
	e.waitStatus(v.ID, tasks.Paused, 2*time.Minute)
	view, _ := e.svc.Get(ctx, v.ID)
	if view.OpenQuestion == nil {
		t.Fatal("paused task should keep its open question")
	}
	if _, err := e.svc.Answer(ctx, v.ID, view.OpenQuestion.ID, "hi", "telegram"); err != nil {
		t.Fatal(err)
	}
	e.waitStatus(v.ID, tasks.Completed, 2*time.Minute)
	run, _ := e.st.LatestRun(ctx, v.ID)
	if run.Kind != tasks.RunResume || !strings.Contains(run.Input, "hi") {
		t.Fatalf("resume run: %+v", run)
	}
	b, _ := os.ReadFile(filepath.Join(e.ws.Path(v.ID), "ANORA_FAKE.md"))
	if !strings.HasPrefix(string(b), "# hi") {
		t.Fatalf("file %q", b)
	}
	evs, _ := e.st.ListEvents(ctx, v.ID, 0, 500)
	var seqs []int64
	for _, ev := range evs {
		if ev.Seq != nil {
			seqs = append(seqs, *ev.Seq)
		}
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] {
			t.Fatalf("seq not monotonic across runs: %v", seqs)
		}
	}
}

func TestFakeRunCancelAndFollowUp(t *testing.T) {
	e := newEnv(t, time.Minute, nil)
	ctx := context.Background()
	v, _ := e.svc.Create(ctx, tasks.CreateInput{ProjectID: "demo", Prompt: "Cancel me", Source: "api"})
	e.waitStatus(v.ID, tasks.WaitingInput, 2*time.Minute)
	if _, err := e.svc.Cancel(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	e.waitStatus(v.ID, tasks.Cancelled, time.Second)
	deadline := time.Now().Add(time.Minute)
	for {
		run, _ := e.st.LatestRun(ctx, v.ID)
		if run.ExitCode != nil {
			if *run.ExitCode != runnermgr.ExitCancelled {
				t.Fatalf("exit code %d", *run.ExitCode)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("runner did not exit after cancel")
		}
		time.Sleep(200 * time.Millisecond)
	}
	if _, err := e.svc.Message(ctx, v.ID, "more", nil); err == nil {
		t.Fatal("message to a cancelled task must be rejected")
	}

	// Retry runs again from scratch; answering completes it. A follow-up
	// message then starts another run on the same branch.
	if _, err := e.svc.Retry(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	e.waitStatus(v.ID, tasks.WaitingInput, 2*time.Minute)
	view, _ := e.svc.Get(ctx, v.ID)
	_, _ = e.svc.Answer(ctx, v.ID, view.OpenQuestion.ID, "hello", "web")
	e.waitStatus(v.ID, tasks.Completed, time.Minute)
	if _, err := e.svc.Message(ctx, v.ID, "Also add a footer", nil); err != nil {
		t.Fatal(err)
	}
	e.waitStatus(v.ID, tasks.WaitingInput, 2*time.Minute)
	view, _ = e.svc.Get(ctx, v.ID)
	_, _ = e.svc.Answer(ctx, v.ID, view.OpenQuestion.ID, "hi", "web")
	e.waitStatus(v.ID, tasks.Completed, time.Minute)
	out, _ := exec.Command("git", "-C", e.ws.Path(v.ID), "log", "--format=%s", "dev..HEAD").Output()
	if !strings.Contains(string(out), "Follow-up: Also add a footer") {
		t.Fatalf("follow-up commit missing: %s", out)
	}
}

// A message sent while a run is active is queued and starts the next run when
// the current one ends; nothing is injected into the live session.
func TestMessageWhileRunningIsQueued(t *testing.T) {
	e := newEnv(t, time.Minute, nil)
	ctx := context.Background()
	v, _ := e.svc.Create(ctx, tasks.CreateInput{ProjectID: "demo", Prompt: "Queue me", Source: "api"})
	e.waitStatus(v.ID, tasks.WaitingInput, 2*time.Minute)
	view, err := e.svc.Message(ctx, v.ID, "Also add a footer", nil)
	if err != nil {
		t.Fatal(err)
	}
	if view.QueuedMessage == nil || *view.QueuedMessage != "Also add a footer" {
		t.Fatalf("queued_message %v", view.QueuedMessage)
	}
	_, _ = e.svc.Answer(ctx, v.ID, view.OpenQuestion.ID, "hello", "web")

	// The first run completes, the queued text starts a follow-up run, which
	// (fake runner) asks again.
	e.waitStatus(v.ID, tasks.WaitingInput, 2*time.Minute)
	view, _ = e.svc.Get(ctx, v.ID)
	if view.QueuedMessage != nil || view.LatestRun.Kind != tasks.RunFollowup || view.LatestRun.Input != "Also add a footer" {
		t.Fatalf("follow-up run %+v queued %v", view.LatestRun, view.QueuedMessage)
	}
	_, _ = e.svc.Answer(ctx, v.ID, view.OpenQuestion.ID, "hi", "web")
	e.waitStatus(v.ID, tasks.Completed, time.Minute)
	out, _ := exec.Command("git", "-C", e.ws.Path(v.ID), "log", "--format=%s", "dev..HEAD").Output()
	if !strings.Contains(string(out), "Follow-up: Also add a footer") {
		t.Fatalf("follow-up commit missing: %s", out)
	}
	types := strings.Join(e.eventTypes(v.ID), ",")
	if strings.Count(types, "run_started") != 2 {
		t.Fatalf("events %s", types)
	}
}

// Interrupt ends the run; a message queued before it starts the next run, so
// "queue a message, then interrupt" redirects Claude.
func TestInterruptEndsRunAndStartsQueuedMessage(t *testing.T) {
	e := newEnv(t, time.Minute, nil)
	ctx := context.Background()
	v, _ := e.svc.Create(ctx, tasks.CreateInput{ProjectID: "demo", Prompt: "Redirect me", Source: "api"})
	e.waitStatus(v.ID, tasks.WaitingInput, 2*time.Minute)
	if _, err := e.svc.Message(ctx, v.ID, "Do this instead", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Interrupt(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	var view *tasks.View
	for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(200 * time.Millisecond) {
		view, _ = e.svc.Get(ctx, v.ID)
		if view.Status == tasks.WaitingInput && view.LatestRun.Kind == tasks.RunFollowup {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no follow-up run: status %s, run %+v", view.Status, view.LatestRun)
		}
	}
	if view.LatestRun.Input != "Do this instead" {
		t.Fatalf("follow-up run %+v", view.LatestRun)
	}
	types := strings.Join(e.eventTypes(v.ID), ",")
	if !strings.Contains(types, "interrupt") || strings.Count(types, "run_started") != 2 {
		t.Fatalf("events %s", types)
	}
	_, _ = e.svc.Cancel(ctx, v.ID)
}

func newBareRepo(t *testing.T, base string) string {
	t.Helper()
	dir := t.TempDir()
	bare := filepath.Join(dir, "remote.git")
	work := filepath.Join(dir, "seed")
	run := func(d string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = d
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run(dir, "init", "-q", "--bare", "-b", base, bare)
	run(dir, "init", "-q", "-b", base, work)
	_ = os.WriteFile(filepath.Join(work, "README.md"), []byte("hi\n"), 0o644)
	run(work, "add", "-A")
	run(work, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "init")
	run(work, "push", "-q", bare, base)
	return bare
}

func TestStreamCutIsReattached(t *testing.T) {
	e := newEnv(t, time.Minute, nil)
	ctx := context.Background()
	v, _ := e.svc.Create(ctx, tasks.CreateInput{ProjectID: "demo", Prompt: "Survive a cut", Source: "api"})
	e.waitStatus(v.ID, tasks.WaitingInput, 2*time.Minute)
	if !e.sched.CutStream(v.ID) {
		t.Fatal("no live runner")
	}
	time.Sleep(500 * time.Millisecond)
	view, _ := e.svc.Get(ctx, v.ID)
	// The answer must reach the runner through the new attach.
	if _, err := e.svc.Answer(ctx, v.ID, view.OpenQuestion.ID, "hello", "web"); err != nil {
		t.Fatal(err)
	}
	done := e.waitStatus(v.ID, tasks.Completed, time.Minute)
	if !strings.Contains(deref(done.StatusReason), "committed locally") {
		t.Fatalf("reason %q", deref(done.StatusReason))
	}
	types := strings.Join(e.eventTypes(v.ID), ",")
	if !strings.Contains(types, "committed") || !strings.Contains(types, ",done") {
		t.Fatalf("events lost across the cut: %s", types)
	}
}

// TestReconcile simulates a restart: a run whose container vanished, a task
// stuck in preparing, and a runner that finished while the service was down.
func TestReconcile(t *testing.T) {
	e := newEnv(t, time.Minute, nil)
	ctx := context.Background()
	// Stop the env's scheduler loop so the test controls Reconcile.
	e.cancel()
	<-e.done

	mk := func(prompt string) *store.Task {
		v, err := e.svc.Create(ctx, tasks.CreateInput{ProjectID: "demo", Prompt: prompt, Source: "api"})
		if err != nil {
			t.Fatal(err)
		}
		return v.Task
	}

	// 1. running, but no container: lost.
	lost := mk("lost")
	lr, _ := e.st.PendingRun(ctx, lost.ID)
	_ = e.st.MarkRunStarting(ctx, lr.ID)
	for _, s := range []string{tasks.Preparing, tasks.Running} {
		_, _ = e.st.SetStatus(ctx, lost.ID, nil, s, "", false)
	}

	// 2. preparing: back to queued with its run pending again.
	prep := mk("prep")
	pr, _ := e.st.PendingRun(ctx, prep.ID)
	_ = e.st.MarkRunStarting(ctx, pr.ID)
	_, _ = e.st.SetStatus(ctx, prep.ID, nil, tasks.Preparing, "", false)

	// 3. a runner that ran to completion while we were "down".
	done := mk("Say hello")
	dr, _ := e.st.PendingRun(ctx, done.ID)
	branch := "agent/x"
	_ = e.st.SetBranch(ctx, done.ID, branch)
	if err := e.ws.Prepare(ctx, done.ID, workspace.Repo{FullName: "o/demo", BaseBranch: "dev"}, branch); err != nil {
		t.Fatal(err)
	}
	if _, err := e.docker.EnsureNetwork(ctx, done.ID, "demo"); err != nil {
		t.Fatal(err)
	}
	_, _ = e.docker.EnsureVolume(ctx, done.ID, "demo")
	_ = e.st.MarkRunStarting(ctx, dr.ID)
	r, err := e.docker.StartRunner(ctx, runnermgr.RunnerSpec{TaskID: done.ID, RunID: dr.ID, Project: "demo", Image: runnerImage(),
		Workspace: e.ws.Path(done.ID), Env: map[string]string{"ANORA_RUNNER_FAKE": "1"}, Memory: "512m", CPUs: 1,
		User: fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())})
	if err != nil {
		t.Fatal(err)
	}
	_ = e.st.SetRunContainer(ctx, dr.ID, r.ContainerID)
	for _, s := range []string{tasks.Preparing, tasks.Running} {
		_, _ = e.st.SetStatus(ctx, done.ID, nil, s, "", false)
	}
	_ = r.Send(runnermgr.NewRun(runnermgr.RunCommand{TaskID: fmt.Sprint(done.ID), Prompt: "Say hello",
		Limits: runnermgr.Limits{MaxTurns: 5, MaxBudgetUSD: 1, MaxDurationSec: 60, QuestionTimeoutSec: 30},
		Commit: runnermgr.CommitInfo{Message: "Say hello", AuthorName: "anora[bot]", AuthorEmail: "a@b"}}))
	_ = r.Send(runnermgr.NewAnswer("q1", "hello"))
	if code, _ := e.docker.Wait(ctx, r.ContainerID); code != 0 {
		t.Fatalf("runner exit %d", code)
	}
	r.Close() // nobody consumed the stream: every event is only in events.jsonl

	// 4. an orphaned network of a task that no longer exists.
	_, _ = e.docker.EnsureNetwork(ctx, 999_999_999, "gone")

	if err := e.sched.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}

	if tk, _ := e.st.GetTask(ctx, lost.ID); tk.Status != tasks.Failed || deref(tk.StatusReason) != "lost" {
		t.Fatalf("lost: %s %v", tk.Status, deref(tk.StatusReason))
	}
	if tk, _ := e.st.GetTask(ctx, prep.ID); tk.Status != tasks.Queued {
		t.Fatalf("prep: %s", tk.Status)
	}
	if run, err := e.st.PendingRun(ctx, prep.ID); err != nil || run.ID != pr.ID {
		t.Fatalf("prep run not pending again: %v", err)
	}
	tk, _ := e.st.GetTask(ctx, done.ID)
	if tk.Status != tasks.Completed || !strings.Contains(deref(tk.StatusReason), "committed locally") {
		t.Fatalf("done: %s %v", tk.Status, deref(tk.StatusReason))
	}
	types := strings.Join(e.eventTypes(done.ID), ",")
	for _, want := range []string{"started", "question", "committed", "done", "run_finished"} {
		if !strings.Contains(types, want) {
			t.Errorf("replay missed %s: %s", want, types)
		}
	}
	ids, _ := e.docker.TaskIDsWithResources(ctx)
	if ids[999_999_999] || ids[done.ID] {
		t.Fatalf("leftover resources: %v", ids)
	}
}

func TestAttachmentsMountedReadOnly(t *testing.T) {
	e := newEnv(t, time.Minute, nil)
	ctx := context.Background()
	var png bytes.Buffer
	if err := pngEncode(&png, 64, 32); err != nil {
		t.Fatal(err)
	}
	img, err := attachments.Process("mock.png", png.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	v, err := e.svc.Create(ctx, tasks.CreateInput{ProjectID: "demo", Prompt: "Match the mock", Source: "api", Images: []*attachments.Image{img}})
	if err != nil {
		t.Fatal(err)
	}
	// The fake runner reads every attachment and tries to write next to them
	// before it asks its question.
	e.waitStatus(v.ID, tasks.WaitingInput, 2*time.Minute)
	live, err := e.st.LiveAttachments(ctx, v.ID)
	if err != nil || len(live) != 1 {
		t.Fatalf("attachments: %v %v", live, err)
	}
	wantRead := fmt.Sprintf("Read %s (%d bytes)", attachments.AgentPath(live[0]), len(img.Data))
	evs, _ := e.st.ListEvents(ctx, v.ID, 0, 500)
	var read, denied bool
	for _, ev := range evs {
		var p struct {
			Tool, Summary string
			OK            *bool
		}
		_ = json.Unmarshal(ev.Payload, &p)
		if ev.Type == "tool_use" && p.Summary == wantRead {
			read = true
		}
		if ev.Type == "tool_result" && p.Tool == "Write" && p.OK != nil && !*p.OK && strings.Contains(p.Summary, "read-only") {
			denied = true
		}
	}
	if !read || !denied {
		var all []string
		for _, ev := range evs {
			all = append(all, string(ev.Payload))
		}
		t.Fatalf("read %v (want %q), write denied %v; events:\n%s", read, wantRead, denied, strings.Join(all, "\n"))
	}
	if _, err := os.Stat(filepath.Join(e.files.TaskDir(v.ID), ".write-probe")); !os.IsNotExist(err) {
		t.Fatalf("the agent wrote into the attachment folder: %v", err)
	}
	// The run's stored input stays the owner's text; the block goes only to the runner.
	if run, _ := e.st.LatestRun(ctx, v.ID); run.Input != "Match the mock" {
		t.Fatalf("run input %q", run.Input)
	}
	view, _ := e.svc.Get(ctx, v.ID)
	if _, err := e.svc.Answer(ctx, v.ID, view.OpenQuestion.ID, "hello", "web"); err != nil {
		t.Fatal(err)
	}
	e.waitStatus(v.ID, tasks.Completed, time.Minute)
}

func pngEncode(w *bytes.Buffer, width, height int) error {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for i := range img.Pix {
		img.Pix[i] = 0xC0
	}
	return png.Encode(w, img)
}
