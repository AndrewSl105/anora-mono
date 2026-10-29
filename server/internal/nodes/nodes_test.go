package nodes_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrewsl105/fleetpocket/server/internal/config"
	"github.com/andrewsl105/fleetpocket/server/internal/events"
	"github.com/andrewsl105/fleetpocket/server/internal/hosts"
	"github.com/andrewsl105/fleetpocket/server/internal/nodeagent"
	"github.com/andrewsl105/fleetpocket/server/internal/nodes"
	"github.com/andrewsl105/fleetpocket/server/internal/projects"
	"github.com/andrewsl105/fleetpocket/server/internal/runnermgr"
	"github.com/andrewsl105/fleetpocket/server/internal/scheduler"
	"github.com/andrewsl105/fleetpocket/server/internal/store"
	"github.com/andrewsl105/fleetpocket/server/internal/tasks"
	"github.com/andrewsl105/fleetpocket/server/internal/testutil"
	"github.com/andrewsl105/fleetpocket/server/internal/workspace"
)

func TestToken(t *testing.T) {
	tok, hash := nodes.NewToken("macbook")
	if !strings.HasPrefix(tok, "fpn_macbook.") || len(hash) != 32 {
		t.Fatalf("token %q", tok)
	}
	if nodes.TokenNode(tok) != "macbook" {
		t.Fatalf("TokenNode(%q) = %q", tok, nodes.TokenNode(tok))
	}
	for _, bad := range []string{"", "fpn_", "fpn_x", "Bearer fpn_a.b", "fpn_A.b", "xyz_a.b"} {
		if nodes.TokenNode(bad) != "" {
			t.Errorf("TokenNode(%q) accepted", bad)
		}
	}
}

// ---- a node whose Docker is faked; the workspace is real ----

type fakeRunner struct {
	runner *runnermgr.Runner
	stream *runnermgr.Stream
	taskID int64
	stdin  chan string
	exit   chan int
	code   int
}

type fakeHost struct {
	*hosts.Local
	mu      sync.Mutex
	runners map[string]*fakeRunner
	tokens  []string
}

func (f *fakeHost) Git(token string) nodeagent.Host {
	f.mu.Lock()
	f.tokens = append(f.tokens, token)
	f.mu.Unlock()
	return f
}
func (f *fakeHost) ScannerAvailable() bool                             { return false }
func (f *fakeHost) HasImage(context.Context, string) error             { return nil }
func (f *fakeHost) EnsureNetwork(context.Context, int64, string) error { return nil }
func (f *fakeHost) EnsureVolume(context.Context, int64, string) error  { return nil }
func (f *fakeHost) Ping(context.Context) error                         { return nil }

// IsRunning is false once a runner's output ended (no re-attach in tests).
func (f *fakeHost) IsRunning(context.Context, string) (bool, error)   { return false, nil }
func (f *fakeHost) RemoveAll(context.Context, int64) error            { return nil }
func (f *fakeHost) Stop(context.Context, string, time.Duration) error { return nil }
func (f *fakeHost) TaskIDsWithResources(context.Context) (map[int64]bool, error) {
	return map[int64]bool{}, nil
}

func (f *fakeHost) newRunner(id string, taskID int64) *fakeRunner {
	fr := &fakeRunner{taskID: taskID, stdin: make(chan string, 16), exit: make(chan int, 1), code: -1}
	fr.runner, fr.stream = runnermgr.NewStreamRunner(id, func(b []byte) error {
		fr.stdin <- string(b)
		return nil
	}, nil)
	f.mu.Lock()
	f.runners[id] = fr
	f.mu.Unlock()
	return fr
}

func (f *fakeHost) StartRunner(_ context.Context, s hosts.StartSpec) (*runnermgr.Runner, error) {
	if s.Env["CLAUDE_CODE_OAUTH_TOKEN"] != "" {
		return nil, fmt.Errorf("the server sent agent credentials")
	}
	return f.newRunner(fmt.Sprintf("c%d", s.TaskID), s.TaskID).runner, nil
}

// Attach (the node process restarted, or a server reconcile) re-opens the
// stream of an existing container.
func (f *fakeHost) Attach(_ context.Context, id string) (*runnermgr.Runner, error) {
	f.mu.Lock()
	old := f.runners[id]
	f.mu.Unlock()
	if old == nil {
		return nil, fmt.Errorf("no container %s", id)
	}
	nr := f.newRunner(id, old.taskID)
	nr.exit = old.exit
	return nr.runner, nil
}

func (f *fakeHost) Wait(ctx context.Context, id string) (int, error) {
	f.mu.Lock()
	fr := f.runners[id]
	f.mu.Unlock()
	select {
	case code := <-fr.exit:
		fr.code = code
		return code, nil
	case <-ctx.Done():
		return -1, ctx.Err()
	}
}

func (f *fakeHost) ListRunners(context.Context) ([]runnermgr.ContainerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []runnermgr.ContainerInfo
	for id, fr := range f.runners {
		out = append(out, runnermgr.ContainerInfo{ID: id, TaskID: fr.taskID, Running: fr.code < 0, ExitCode: max(fr.code, 0)})
	}
	return out, nil
}

func (f *fakeHost) Remove(_ context.Context, id string) error {
	f.mu.Lock()
	delete(f.runners, id)
	f.mu.Unlock()
	return nil
}

func (f *fakeHost) runner(id string) *fakeRunner {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runners[id]
}

func line(seq int64, summary string) string {
	return fmt.Sprintf(`{"type":"tool_use","ts":"2026-10-29T10:00:00Z","seq":%d,"tool":"Edit","summary":%q}`, seq, summary)
}

func appendLog(t *testing.T, ws *workspace.Manager, taskID int64, lines ...string) {
	t.Helper()
	dir := filepath.Join(ws.Path(taskID), ".fleetpocket")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		fmt.Fprintln(f, l)
	}
}

func bareRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	bare := filepath.Join(dir, "bare.git")
	src := filepath.Join(dir, "src")
	run("init", "--bare", "-b", "dev", bare)
	run("init", "-b", "dev", src)
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("-C", src, "add", ".")
	run("-C", src, "commit", "-m", "init")
	run("-C", src, "push", bare, "dev")
	return bare
}

// connTracker lets the test cut the node's TCP connection (a network drop).
type connTracker struct {
	mu    sync.Mutex
	conns []net.Conn
}

func (c *connTracker) client() *http.Client {
	d := &net.Dialer{}
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := d.DialContext(ctx, network, addr)
		if err == nil {
			c.mu.Lock()
			c.conns = append(c.conns, conn)
			c.mu.Unlock()
		}
		return conn, err
	}}}
}

func (c *connTracker) cut() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, conn := range c.conns {
		_ = conn.Close()
	}
	c.conns = nil
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func nextEvent(t *testing.T, r *runnermgr.Runner) runnermgr.Event {
	t.Helper()
	select {
	case ev, ok := <-r.Events:
		if !ok {
			t.Fatal("event stream ended")
		}
		return ev
	case <-time.After(10 * time.Second):
		t.Fatal("no event")
	}
	return runnermgr.Event{}
}

func TestNodeEndToEnd(t *testing.T) {
	st := testutil.DB(t)
	ctx := context.Background()

	var maxSeq sync.Map // task id → int64, what the "database" has
	reg := &hosts.Registry{
		Auth: func(context.Context, string, int64) (string, error) { return "ghs_short_lived", nil },
		MaxSeq: func(_ context.Context, id int64) (int64, error) {
			v, _ := maxSeq.Load(id)
			n, _ := v.(int64)
			return n, nil
		},
	}
	var onConnect []runnermgr.ContainerInfo
	var ocMu sync.Mutex
	reg.OnConnect = func(_ context.Context, _ *hosts.Remote, unknown []runnermgr.ContainerInfo) {
		ocMu.Lock()
		onConnect = append(onConnect, unknown...)
		ocMu.Unlock()
	}
	svc := &nodes.Service{Store: st, Hub: events.NewHub(), Registry: reg}
	reg.OnChange = svc.Changed
	_, tok, err := svc.Create(ctx, nodes.CreateInput{ID: "mac", Name: "Mac"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(svc.Serve))
	defer srv.Close()

	// A wrong token is refused before the upgrade.
	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Authorization", "Bearer fpn_mac.wrong")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token: %v %v", resp, err)
	}

	bare := bareRepo(t)
	ws := &workspace.Manager{Dir: t.TempDir(), RemoteURL: func(string) string { return bare }}
	attDir := t.TempDir()
	host := &fakeHost{runners: map[string]*fakeRunner{}, Local: &hosts.Local{
		Workspace: ws, WriteAttachments: true,
		AttachmentsDir: func(id int64) string { return filepath.Join(attDir, fmt.Sprint(id)) },
	}}
	tracker := &connTracker{}
	agent := &nodeagent.Agent{Server: srv.URL, Token: tok, Version: "test", Local: host, Agents: []string{"claude"}, HTTPClient: tracker.client()}
	actx, stop := context.WithCancel(ctx)
	defer stop()
	go func() { _ = agent.Run(actx) }()
	waitFor(t, "node online", func() bool { return reg.IsOnline("mac") })
	if n, _ := st.GetNode(ctx, "mac"); len(n.Agents) != 1 || n.Version != "test" {
		t.Fatalf("hello not stored: %+v", n)
	}
	remote := reg.Node("mac")

	// Workspace: clone with the server's short-lived token, diff, unpushed.
	repo := workspace.Repo{FullName: "o/r", InstallationID: 1, BaseBranch: "dev"}
	if err := remote.PrepareWorkspace(ctx, 7, repo, "agent/7-x"); err != nil {
		t.Fatal(err)
	}
	if len(host.tokens) != 1 || host.tokens[0] != "ghs_short_lived" {
		t.Fatalf("git token not passed per call: %v", host.tokens)
	}
	if n, err := remote.UnpushedCommits(ctx, 7, "dev", "agent/7-x"); err != nil || n != 0 {
		t.Fatalf("unpushed: %d %v", n, err)
	}

	// Attachments travel with the call.
	src := filepath.Join(t.TempDir(), "1.png")
	_ = os.WriteFile(src, []byte("png"), 0o644)
	if err := remote.PrepareAttachments(ctx, 7, []hosts.AttachmentFile{{Name: "1.png", Path: src}}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(attDir, "7", "1.png")); string(b) != "png" {
		t.Fatalf("attachment not written: %q", b)
	}

	// A runner: output lines become events, stdin reaches the container.
	r, err := remote.StartRunner(ctx, hosts.StartSpec{TaskID: 7, RunID: 1, Image: "img", Agent: "claude", Env: map[string]string{"A": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	fr := host.runner("c7")
	fr.stream.Line([]byte(line(1, "first")))
	if ev := nextEvent(t, r); ev.Seq != 1 || ev.Summary != "first" {
		t.Fatalf("event %+v", ev)
	}
	maxSeq.Store(int64(7), int64(1))
	if err := r.Send(runnermgr.NewAnswer("q1", "yes")); err != nil {
		t.Fatal(err)
	}
	if in := <-fr.stdin; !strings.Contains(in, `"answer"`) {
		t.Fatalf("stdin %q", in)
	}

	// Network drop: output keeps coming (the agent also logs it); on
	// reconnect the node resends what the server has not stored, in order.
	tracker.cut()
	waitFor(t, "node offline", func() bool { return !reg.IsOnline("mac") })
	if _, err := remote.DiffStat(ctx, 7, "dev"); !hosts.IsOffline(err) {
		t.Fatalf("call while offline: %v", err)
	}
	appendLog(t, ws, 7, line(1, "first"), line(2, "while offline"))
	fr.stream.Line([]byte(line(2, "while offline")))
	waitFor(t, "node back", func() bool { return reg.IsOnline("mac") })
	ev := nextEvent(t, r)
	if ev.Seq != 2 || ev.Summary != "while offline" {
		t.Fatalf("after reconnect got %+v", ev)
	}
	fr.stream.Line([]byte(line(3, "live again")))
	if ev := nextEvent(t, r); ev.Seq != 3 {
		t.Fatalf("live after reconnect: %+v", ev)
	}

	// Exit: the stream ends and Wait returns the code.
	fr.stream.End()
	fr.exit <- 0
	waitFor(t, "exit", func() bool {
		running, _ := remote.IsRunning(ctx, "c7")
		return !running
	})
	if code, err := remote.Wait(ctx, "c7"); err != nil || code != 0 {
		t.Fatalf("wait: %d %v", code, err)
	}
	if err := remote.Remove(ctx, "c7"); err != nil {
		t.Fatal(err)
	}

	// Server restart: a fresh registry knows no runners; the node's runner
	// is handed to the scheduler (OnConnect) and can be re-attached.
	reg2 := &hosts.Registry{MaxSeq: reg.MaxSeq, OnConnect: reg.OnConnect}
	svc2 := &nodes.Service{Store: st, Hub: events.NewHub(), Registry: reg2}
	srv2 := httptest.NewServer(http.HandlerFunc(svc2.Serve))
	defer srv2.Close()
	stop()
	host.newRunner("c8", 8)
	appendLog(t, ws, 8, line(1, "before restart"))
	agent2 := &nodeagent.Agent{Server: srv2.URL, Token: tok, Version: "test", Local: host}
	a2ctx, stop2 := context.WithCancel(ctx)
	defer stop2()
	go func() { _ = agent2.Run(a2ctx) }()
	waitFor(t, "reconcile", func() bool {
		ocMu.Lock()
		defer ocMu.Unlock()
		return len(onConnect) == 1 && onConnect[0].ID == "c8" && onConnect[0].TaskID == 8
	})
	r8, err := reg2.Node("mac").Attach(ctx, "c8")
	if err != nil {
		t.Fatal(err)
	}
	if ev := nextEvent(t, r8); ev.Summary != "before restart" {
		t.Fatalf("re-attach replay: %+v", ev)
	}
}

// TestSchedulerRunsTaskOnNode drives a whole task through the scheduler on a
// node: queued while the node is offline, claimed when it connects, prepared
// and started over the WebSocket, finished from the node's output.
func TestSchedulerRunsTaskOnNode(t *testing.T) {
	st := testutil.DB(t)
	ctx := context.Background()
	hub := events.NewHub()
	cfg := &config.Config{
		RunnerImage: "img", MaxConcurrentTasks: 1, DefaultMaxTurns: 5, DefaultMaxBudgetUSD: 1,
		DefaultMaxDuration: time.Minute, QuestionTimeout: time.Minute, DefaultAgent: "claude",
		ClaudeOAuthToken: "server-secret",
	}
	bare := bareRepo(t)
	serverWS := &workspace.Manager{Dir: t.TempDir(), RemoteURL: func(string) string { return bare }}
	serverHost := &fakeHost{runners: map[string]*fakeRunner{}, Local: &hosts.Local{Workspace: serverWS, AgentEnv: cfg.AgentEnv}}
	reg := &hosts.Registry{Local: serverHost, MaxSeq: st.MaxSeq}
	svc := &tasks.Service{Store: st, Hub: hub, DefaultAgent: "claude", AgentEnabled: cfg.AgentEnabled}
	sched := scheduler.New(&scheduler.Scheduler{Cfg: cfg, Store: st, Hub: hub, Tasks: svc, Hosts: reg})
	svc.Executor = sched
	nodeSvc := &nodes.Service{Store: st, Hub: hub, Registry: reg}
	reg.OnChange = func(id string, online bool) {
		nodeSvc.Changed(id, online)
		sched.Wake()
	}
	reg.OnConnect = func(ctx context.Context, h *hosts.Remote, rs []runnermgr.ContainerInfo) {
		sched.ReconcileNode(ctx, h, rs)
	}
	_, tok, err := nodeSvc.Create(ctx, nodes.CreateInput{ID: "mac", Name: "Mac"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.NodeSeen(ctx, "mac", []string{"claude"}, "", ""); err != nil {
		t.Fatal(err)
	}
	ps := &projects.Service{Store: st}
	mac := "mac"
	if _, err := ps.Create(ctx, projects.CreateInput{ID: "p", ProjectFields: store.ProjectFields{Node: &mac}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.AddRepository(ctx, "p", projects.RepoInput{FullName: "o/r", InstallationID: 1, BaseBranch: "dev"}); err != nil {
		t.Fatal(err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { _ = sched.Run(runCtx); close(done) }()
	defer func() { cancel(); <-done }()

	v, err := svc.Create(ctx, tasks.CreateInput{ProjectID: "p", Prompt: "Say hi"})
	if err != nil {
		t.Fatal(err)
	}
	sched.Wake()
	time.Sleep(300 * time.Millisecond)
	if tk, _ := st.GetTask(ctx, v.ID); tk.Status != tasks.Queued {
		t.Fatalf("task of an offline node left the queue: %s", tk.Status)
	}

	srv := httptest.NewServer(http.HandlerFunc(nodeSvc.Serve))
	defer srv.Close()
	ws := &workspace.Manager{Dir: t.TempDir(), RemoteURL: func(string) string { return bare }}
	host := &fakeHost{runners: map[string]*fakeRunner{}, Local: &hosts.Local{
		Workspace: ws, WriteAttachments: true, AttachmentsDir: func(int64) string { return t.TempDir() },
	}}
	actx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		_ = (&nodeagent.Agent{Server: srv.URL, Token: tok, Local: host, Agents: []string{"claude"}}).Run(actx)
	}()

	container := fmt.Sprintf("c%d", v.ID)
	var fr *fakeRunner
	waitFor(t, "runner started on the node", func() bool { fr = host.runner(container); return fr != nil })
	if _, err := os.Stat(filepath.Join(ws.Path(v.ID), ".git")); err != nil {
		t.Fatalf("workspace not cloned on the node: %v", err)
	}
	select {
	case in := <-fr.stdin:
		if !strings.Contains(in, `"type":"run"`) || !strings.Contains(in, "Say hi") {
			t.Fatalf("run command %q", in)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no run command")
	}
	waitFor(t, "running", func() bool { tk, _ := st.GetTask(ctx, v.ID); return tk.Status == tasks.Running })
	fr.stream.Line([]byte(`{"type":"started","ts":"2026-10-29T10:00:00Z","seq":1,"session_id":"s1"}`))
	fr.stream.Line([]byte(line(2, "Edit README.md")))
	fr.stream.Line([]byte(`{"type":"done","ts":"2026-10-29T10:00:01Z","seq":3,"session_id":"s1","summary":"Nothing to change"}`))
	fr.stream.End()
	fr.exit <- 0

	waitFor(t, "completed", func() bool { tk, _ := st.GetTask(ctx, v.ID); return tk.Status == tasks.Completed })
	tk, _ := st.GetTask(ctx, v.ID)
	if tk.SessionID == nil || *tk.SessionID != "s1" || tk.LastActivity == nil {
		t.Fatalf("events not applied: %+v", tk)
	}
	if n, _ := st.MaxSeq(ctx, v.ID); n != 3 {
		t.Fatalf("persisted seq %d", n)
	}
}
