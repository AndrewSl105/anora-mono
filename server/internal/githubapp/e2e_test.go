package githubapp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// TestEndToEndPR runs a fake-runner task through push and PR creation against
// a fake GitHub API and a local bare repo, then a follow-up that comments.
func TestEndToEndPR(t *testing.T) {
	st := testutil.DB(t)
	ctx := context.Background()
	dc, err := runnermgr.NewDockerClient("")
	if err != nil {
		t.Skip(err)
	}
	docker := &runnermgr.Manager{Docker: dc, SidecarMemory: "256m"}
	image := os.Getenv("ANORA_TEST_RUNNER_IMAGE")
	if image == "" {
		image = "anora-agent:dev"
	}
	if docker.Ping(ctx) != nil || docker.HasImage(ctx, image) != nil {
		t.Skip("docker or runner image unavailable")
	}
	f, srv := newFakeGitHub(t)
	app := testApp(t, srv.URL)
	bare := bareRepo(t)
	ws := &workspace.Manager{Dir: filepath.Join(t.TempDir(), "ws"), RemoteURL: func(string) string { return bare }, Auth: app.Token}
	cfg := &config.Config{RunnerImage: image, RunnerFake: true, MaxConcurrentTasks: 1, RunnerMemory: "512m", RunnerCPUs: 1,
		DefaultMaxTurns: 5, DefaultMaxBudgetUSD: 1, DefaultMaxDuration: time.Minute, QuestionTimeout: time.Minute,
		RunnerUser: fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())}
	hub := events.NewHub()
	svc := &tasks.Service{Store: st, Hub: hub}
	sched := scheduler.New(&scheduler.Scheduler{Cfg: cfg, Store: st, Hub: hub, Tasks: svc, Docker: docker, Workspace: ws,
		Publisher: &Publisher{App: app, Workspace: ws}})
	svc.Executor = sched
	ps := &projects.Service{Store: st}
	name := "P"
	_, _ = ps.Create(ctx, projects.CreateInput{ID: "p", ProjectFields: store.ProjectFields{Name: &name}})
	_, _ = ps.AddRepository(ctx, "p", projects.RepoInput{FullName: "o/r", InstallationID: 11, BaseBranch: "dev"})

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { _ = sched.Run(runCtx); close(done) }()
	var taskID int64
	defer func() { cancel(); <-done; _ = docker.RemoveAll(ctx, taskID); _ = docker.RemoveVolume(ctx, taskID) }()

	wait := func(id int64, want string) *store.Task {
		t.Helper()
		deadline := time.Now().Add(2 * time.Minute)
		for {
			tk, _ := st.GetTask(ctx, id)
			if tk.Status == want {
				return tk
			}
			if time.Now().After(deadline) || (tk.Status == tasks.Failed && want != tasks.Failed) {
				t.Fatalf("status %s (%v), want %s", tk.Status, tk.StatusReason, want)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	answer := func(id int64, text string) {
		wait(id, tasks.WaitingInput)
		v, _ := svc.Get(ctx, id)
		if _, err := svc.Answer(ctx, id, v.OpenQuestion.ID, text, "web"); err != nil {
			t.Fatal(err)
		}
	}

	v, _ := svc.Create(ctx, tasks.CreateInput{ProjectID: "p", Prompt: "Say hello"})
	taskID = v.ID
	answer(v.ID, "hello")
	tk := wait(v.ID, tasks.PROpen)
	if tk.PRNumber == nil || *tk.PRNumber != 5 || !strings.Contains(*tk.PRURL, "/pull/5") {
		t.Fatalf("pr %v %v", tk.PRNumber, tk.PRURL)
	}
	if !strings.Contains(f.bodies["POST /repos/o/r/pulls"], "Wrote ANORA_FAKE.md") {
		t.Fatalf("PR body lacks summary: %s", f.bodies["POST /repos/o/r/pulls"])
	}

	// Follow-up from pr_open: new run on the same branch, PR comment, still pr_open.
	if _, err := svc.Message(ctx, v.ID, "Also say hi"); err != nil {
		t.Fatal(err)
	}
	answer(v.ID, "hi")
	deadline := time.Now().Add(time.Minute)
	for f.count("POST /repos/o/r/issues/5/comments") == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("no PR comment; requests %v", f.requests)
		}
		time.Sleep(200 * time.Millisecond)
	}
	wait(v.ID, tasks.PROpen)
	if f.count("POST /repos/o/r/pulls") != 1 {
		t.Fatal("follow-up must not open a second PR")
	}
}
