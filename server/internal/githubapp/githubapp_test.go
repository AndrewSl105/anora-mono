package githubapp

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/andrewsl105/anora/server/internal/events"
	"github.com/andrewsl105/anora/server/internal/projects"
	"github.com/andrewsl105/anora/server/internal/scheduler"
	"github.com/andrewsl105/anora/server/internal/store"
	"github.com/andrewsl105/anora/server/internal/tasks"
	"github.com/andrewsl105/anora/server/internal/testutil"
	"github.com/andrewsl105/anora/server/internal/workspace"
)

// fakeGitHub records requests and answers the few endpoints we use.
type fakeGitHub struct {
	mu       sync.Mutex
	requests []string
	bodies   map[string]string
	nextPR   int
	openPRs  map[string]int // head -> number
}

func newFakeGitHub(t *testing.T) (*fakeGitHub, *httptest.Server) {
	f := &fakeGitHub{bodies: map[string]string{}, nextPR: 5, openPRs: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	key := r.Method + " " + r.URL.Path
	f.requests = append(f.requests, key)
	f.bodies[key] = string(body)
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/access_tokens"):
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"token":"ghs_test","expires_at":"2099-01-01T00:00:00Z"}`))
	case r.Method == "GET" && r.URL.Path == "/app/installations":
		_, _ = w.Write([]byte(`[{"id":11}]`))
	case r.Method == "GET" && r.URL.Path == "/installation/repositories":
		_, _ = w.Write([]byte(`{"total_count":1,"repositories":[{"full_name":"o/r","default_branch":"dev","private":true}]}`))
	case r.Method == "GET" && r.URL.Path == "/repos/o/r/pulls":
		head := strings.TrimPrefix(r.URL.Query().Get("head"), "o:")
		if n, ok := f.openPRs[head]; ok {
			_, _ = w.Write([]byte(`[{"number":` + itoa(n) + `,"html_url":"https://github.com/o/r/pull/` + itoa(n) + `"}]`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	case r.Method == "POST" && r.URL.Path == "/repos/o/r/pulls":
		var in struct{ Head string }
		_ = json.Unmarshal(body, &in)
		n := f.nextPR
		f.nextPR++
		f.openPRs[in.Head] = n
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"number":` + itoa(n) + `,"html_url":"https://github.com/o/r/pull/` + itoa(n) + `"}`))
	case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/repos/o/r/issues/"):
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":1}`))
	default:
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"message":"not found"}`))
	}
}

func (f *fakeGitHub) count(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if r == key {
			n++
		}
	}
	return n
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func testApp(t *testing.T, apiURL string) *App {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	app, err := NewWithKey(1, pemBytes, apiURL)
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func TestTokenIsScopedToOneRepo(t *testing.T) {
	f, srv := newFakeGitHub(t)
	app := testApp(t, srv.URL)
	tok, err := app.Token(context.Background(), "o/r", 11)
	if err != nil || tok != "ghs_test" {
		t.Fatalf("%q %v", tok, err)
	}
	if body := f.bodies["POST /app/installations/11/access_tokens"]; !strings.Contains(body, `"repositories":["r"]`) {
		t.Fatalf("token not scoped: %s", body)
	}
	repos, err := app.ListRepositories(context.Background())
	if err != nil || len(repos) != 1 || repos[0].FullName != "o/r" || repos[0].InstallationID != 11 {
		t.Fatalf("repos %+v %v", repos, err)
	}
}

func TestCIStatus(t *testing.T) {
	cases := []struct{ action, status, conclusion, want string }{
		{"requested", "queued", "", "pending"},
		{"in_progress", "in_progress", "", "pending"},
		{"completed", "completed", "success", "success"},
		{"completed", "completed", "skipped", "success"},
		{"completed", "completed", "failure", "failure"},
		{"completed", "completed", "timed_out", "failure"},
		{"edited", "", "", ""},
	}
	for _, c := range cases {
		if got := CIStatus(c.action, c.status, c.conclusion); got != c.want {
			t.Errorf("%v: got %q", c, got)
		}
	}
}

func bareRepo(t *testing.T) string {
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
	run(dir, "init", "-q", "--bare", "-b", "dev", bare)
	run(dir, "init", "-q", "-b", "dev", work)
	_ = os.WriteFile(filepath.Join(work, "README.md"), []byte("hi\n"), 0o644)
	run(work, "add", "-A")
	run(work, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "init")
	run(work, "push", "-q", bare, "dev")
	return bare
}

func commit(t *testing.T, dir, file, content string) {
	_ = os.MkdirAll(filepath.Dir(filepath.Join(dir, file)), 0o755)
	_ = os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644)
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.name=a", "-c", "user.email=a@a", "commit", "-qm", "c"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
}

func TestPublisherCreatesThenComments(t *testing.T) {
	ctx := context.Background()
	f, srv := newFakeGitHub(t)
	app := testApp(t, srv.URL)
	bare := bareRepo(t)
	ws := &workspace.Manager{Dir: t.TempDir(), RemoteURL: func(string) string { return bare }, Auth: app.Token}
	repo := &store.Repository{ID: 1, FullName: "o/r", BaseBranch: "dev", InstallationID: 11}
	if err := ws.Prepare(ctx, 7, workspace.Repo{FullName: "o/r", InstallationID: 11, BaseBranch: "dev"}, "agent/7-x"); err != nil {
		t.Fatal(err)
	}
	commit(t, ws.Path(7), "a.txt", "a\n")
	p := &Publisher{App: app, Workspace: ws, TaskURL: func(id int64) string { return "https://anora.example.com/tasks/7" }}
	task := &store.Task{ID: 7, Title: "Do X", Prompt: "Do X\nplease"}
	res, err := p.Publish(ctx, scheduler.PublishInput{Task: task, Repo: repo, Branch: "agent/7-x", Summary: "Did X", CostUSD: 0.5, Turns: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Created || res.PRNumber != 5 {
		t.Fatalf("%+v", res)
	}
	body := f.bodies["POST /repos/o/r/pulls"]
	for _, want := range []string{`"base":"dev"`, `"head":"agent/7-x"`, "Did X", "a.txt", "Cost: $0.50", "Turns: 3", "/tasks/7"} {
		if !strings.Contains(body, want) {
			t.Errorf("PR body missing %q: %s", want, body)
		}
	}
	// The branch reached the remote.
	if out, _ := exec.Command("git", "--git-dir", bare, "branch", "--list", "agent/7-x").Output(); !strings.Contains(string(out), "agent/7-x") {
		t.Fatal("branch not pushed")
	}

	// A later run pushes to the same branch and comments instead of opening a PR.
	commit(t, ws.Path(7), "b.txt", "b\n")
	n := 5
	task.PRNumber = &n
	res, err = p.Publish(ctx, scheduler.PublishInput{Task: task, Repo: repo, Branch: "agent/7-x", Summary: "Did Y"})
	if err != nil || res.Created || res.PRNumber != 5 {
		t.Fatalf("%+v %v", res, err)
	}
	if f.count("POST /repos/o/r/pulls") != 1 || f.count("POST /repos/o/r/issues/5/comments") != 1 {
		t.Fatalf("requests %v", f.requests)
	}

	// A rejected diff never pushes.
	commit(t, ws.Path(7), ".github/workflows/x.yml", "on: push\n")
	_, err = p.Publish(ctx, scheduler.PublishInput{Task: task, Repo: repo, Branch: "agent/7-x"})
	if err == nil || !strings.Contains(err.Error(), "diff rejected") {
		t.Fatalf("expected rejection, got %v", err)
	}
}

func sign(secret, body []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func TestWebhook(t *testing.T) {
	st := testutil.DB(t)
	ctx := context.Background()
	hub := events.NewHub()
	svc := &tasks.Service{Store: st, Hub: hub}
	ps := &projects.Service{Store: st}
	name := "P"
	_, _ = ps.Create(ctx, projects.CreateInput{ID: "p", ProjectFields: store.ProjectFields{Name: &name}})
	_, _ = ps.AddRepository(ctx, "p", projects.RepoInput{FullName: "o/r", InstallationID: 1})
	v, err := svc.Create(ctx, tasks.CreateInput{ProjectID: "p", Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.SetBranch(ctx, v.ID, "agent/1-x")
	_ = st.SetPR(ctx, v.ID, 9, "https://github.com/o/r/pull/9")
	for _, s := range []string{tasks.Preparing, tasks.Running, tasks.Pushing, tasks.PROpen} {
		if _, err := svc.SetStatus(ctx, v.ID, s, ""); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}

	var failed []CIFailure
	cleaned := false
	h := &Webhook{Secret: []byte("whsec"), Store: st, Hub: hub, Tasks: svc,
		OnCIFailed: func(_ context.Context, f CIFailure) { failed = append(failed, f) },
		Cleanup:    func(context.Context, int64) { cleaned = true },
	}
	post := func(event, body string, sig string) int {
		req := httptest.NewRequest("POST", "/webhooks/github", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-GitHub-Event", event)
		req.Header.Set("X-Hub-Signature-256", sig)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	cs := func(action, status, concl string) string {
		return `{"action":"` + action + `","check_suite":{"head_branch":"agent/1-x","status":"` + status + `","conclusion":"` + concl + `","app":{"name":"GitHub Actions"}},"repository":{"full_name":"o/r"}}`
	}
	body := cs("completed", "completed", "failure")
	if code := post("check_suite", body, "sha256=00"); code != 401 {
		t.Fatalf("bad signature accepted: %d", code)
	}
	if code := post("check_suite", body, sign([]byte("whsec"), []byte(body))); code != 200 {
		t.Fatalf("check_suite: %d", code)
	}
	tk, _ := st.GetTask(ctx, v.ID)
	if tk.CIStatus == nil || *tk.CIStatus != "failure" || len(failed) != 1 {
		t.Fatalf("ci %v failed %v", tk.CIStatus, failed)
	}
	// A later success from another suite does not hide the failure.
	body = cs("completed", "completed", "success")
	post("check_suite", body, sign([]byte("whsec"), []byte(body)))
	tk, _ = st.GetTask(ctx, v.ID)
	if *tk.CIStatus != "failure" {
		t.Fatalf("failure should stick, got %s", *tk.CIStatus)
	}
	// A new push resets to pending.
	body = cs("requested", "queued", "")
	post("check_suite", body, sign([]byte("whsec"), []byte(body)))
	tk, _ = st.GetTask(ctx, v.ID)
	if *tk.CIStatus != "pending" {
		t.Fatalf("pending expected, got %s", *tk.CIStatus)
	}

	body = `{"action":"closed","number":9,"pull_request":{"number":9,"merged":true},"repository":{"full_name":"o/r"}}`
	if code := post("pull_request", body, sign([]byte("whsec"), []byte(body))); code != 200 {
		t.Fatalf("pull_request: %d", code)
	}
	tk, _ = st.GetTask(ctx, v.ID)
	if tk.Status != tasks.Completed || *tk.StatusReason != "PR merged" || !cleaned {
		t.Fatalf("status %s reason %v cleaned %v", tk.Status, tk.StatusReason, cleaned)
	}

	body = `{"zen":"hi"}`
	if code := post("ping", body, sign([]byte("whsec"), []byte(body))); code != 200 {
		t.Fatalf("ping: %d", code)
	}
}
