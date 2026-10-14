package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/andrewsl105/anora-runner/internal/api"
	"github.com/andrewsl105/anora-runner/internal/auth"
	"github.com/andrewsl105/anora-runner/internal/config"
	"github.com/andrewsl105/anora-runner/internal/events"
	"github.com/andrewsl105/anora-runner/internal/projects"
	"github.com/andrewsl105/anora-runner/internal/store"
	"github.com/andrewsl105/anora-runner/internal/tasks"
	"github.com/andrewsl105/anora-runner/internal/testutil"
)

type client struct {
	t    *testing.T
	base string
	http *http.Client
}

func (c *client) do(method, path string, body any) (int, map[string]any) {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, r)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "anora")
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func setup(t *testing.T) (*client, *store.Store, string) {
	st := testutil.DB(t)
	hash, _ := auth.HashPassword("pw-for-tests")
	secret, _, _ := auth.NewTOTP("t")
	cfg := &config.Config{TelegramBotToken: "1:T", TelegramAllowedUserID: 42}
	hub := events.NewHub()
	srv := &api.Server{
		Cfg: cfg, Store: st, Hub: hub,
		Tasks:    &tasks.Service{Store: st, Hub: hub},
		Projects: &projects.Service{Store: st},
		Auth:     &auth.Manager{Store: st, PasswordHash: hash, TOTPSecret: secret, SessionSecret: []byte("s"), CookieInsecure: true},
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	return &client{t: t, base: ts.URL, http: &http.Client{Jar: jar}}, st, secret
}

func TestAPIFlow(t *testing.T) {
	c, st, secret := setup(t)
	ctx := context.Background()

	if code, _ := c.do("GET", "/api/v1/me", nil); code != 401 {
		t.Fatalf("me without session: %d", code)
	}
	code, _ := c.do("POST", "/api/v1/auth/login", map[string]string{"password": "pw-for-tests", "totp": "000000"})
	if code != 401 {
		t.Fatalf("bad totp: %d", code)
	}
	otp, _ := totp.GenerateCode(secret, time.Now())
	if code, body := c.do("POST", "/api/v1/auth/login", map[string]string{"password": "pw-for-tests", "totp": otp}); code != 200 {
		t.Fatalf("login: %d %v", code, body)
	}

	if code, body := c.do("POST", "/api/v1/projects", map[string]any{"id": "demo", "name": "Demo"}); code != 201 {
		t.Fatalf("create project: %d %v", code, body)
	}
	if code, _ := c.do("POST", "/api/v1/tasks", map[string]any{"project_id": "demo", "prompt": "x"}); code != 400 {
		t.Fatalf("task without repo should be 400, got %d", code)
	}
	c.do("POST", "/api/v1/projects/demo/repositories", map[string]any{"full_name": "o/r", "installation_id": 1, "base_branch": "main"})
	code, task := c.do("POST", "/api/v1/tasks", map[string]any{"project_id": "demo", "prompt": "Do the thing\nwith details"})
	if code != 201 || task["title"] != "Do the thing" || task["status"] != "queued" {
		t.Fatalf("create task: %d %v", code, task)
	}
	id := int64(task["id"].(float64))

	// A question answered twice: first wins, second is 409.
	run, _ := st.PendingRun(ctx, id)
	_ = st.MarkRunStarting(ctx, run.ID)
	if _, err := st.SetStatus(ctx, id, nil, tasks.WaitingInput, "", false); err != nil {
		t.Fatal(err)
	}
	qid := tasks.QuestionID(run.ID, "q1")
	if _, err := st.CreateQuestion(ctx, store.Question{ID: qid, TaskID: id, RunID: run.ID, Text: "Which?", Options: []string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	code, view := c.do("GET", "/api/v1/tasks/"+fmt.Sprint(id), nil)
	if code != 200 || view["open_question"] == nil {
		t.Fatalf("open question missing: %v", view)
	}
	// No executor: the answer is stored and the task stays waiting until the runner exits.
	if code, body := c.do("POST", "/api/v1/tasks/"+fmt.Sprint(id)+"/answer", map[string]any{"question_id": qid, "text": "a"}); code != 200 {
		t.Fatalf("answer: %d %v", code, body)
	}
	if code, body := c.do("POST", "/api/v1/tasks/"+fmt.Sprint(id)+"/answer", map[string]any{"question_id": qid, "text": "b"}); code != 409 || body["error"].(map[string]any)["code"] != "already_answered" {
		t.Fatalf("second answer: %d %v", code, body)
	}

	// Paused task: message resumes (new run), interrupt is 409.
	if _, err := st.SetStatus(ctx, id, nil, tasks.Paused, "", false); err != nil {
		t.Fatal(err)
	}
	if code, _ := c.do("POST", "/api/v1/tasks/"+fmt.Sprint(id)+"/interrupt", nil); code != 409 {
		t.Fatalf("interrupt paused: %d", code)
	}
	if code, body := c.do("POST", "/api/v1/tasks/"+fmt.Sprint(id)+"/message", map[string]any{"text": "continue"}); code != 200 || body["status"] != "queued" {
		t.Fatalf("message paused: %d %v", code, body)
	}
	if r, err := st.PendingRun(ctx, id); err != nil || r.Kind != tasks.RunResume || r.Input != "continue" {
		t.Fatalf("pending run: %+v %v", r, err)
	}

	code, evs := c.do("GET", "/api/v1/tasks/"+fmt.Sprint(id)+"/events?limit=2", nil)
	if code != 200 || evs["next_cursor"] == "" {
		t.Fatalf("events page: %v", evs)
	}
	code, evs2 := c.do("GET", "/api/v1/tasks/"+fmt.Sprint(id)+"/events?after_id="+evs["next_cursor"].(string), nil)
	if code != 200 || len(evs2["items"].([]any)) == 0 {
		t.Fatalf("events page 2: %v", evs2)
	}

	if code, _ := c.do("POST", "/api/v1/tasks/"+fmt.Sprint(id)+"/cancel", nil); code != 200 {
		t.Fatal("cancel")
	}
	if code, _ := c.do("POST", "/api/v1/tasks/"+fmt.Sprint(id)+"/cancel", nil); code != 409 {
		t.Fatal("double cancel should be 409")
	}
	if code, body := c.do("POST", "/api/v1/tasks/"+fmt.Sprint(id)+"/retry", nil); code != 200 || body["status"] != "queued" {
		t.Fatalf("retry: %v", body)
	}

	// CSRF header is required.
	req, _ := http.NewRequest("POST", c.base+"/api/v1/tasks/"+fmt.Sprint(id)+"/cancel", nil)
	resp, _ := c.http.Do(req)
	if resp.StatusCode != 403 {
		t.Fatalf("csrf: %d", resp.StatusCode)
	}
}

func TestTelegramLogin(t *testing.T) {
	c, _, _ := setup(t)
	mk := func(uid string) string {
		return auth.SignInitData("1:T", map[string]string{
			"auth_date": jsonNum(time.Now().Unix()), "user": `{"id":` + uid + `}`,
		})
	}
	if code, _ := c.do("POST", "/api/v1/auth/telegram", map[string]string{"init_data": mk("7")}); code != 401 {
		t.Fatalf("other user accepted: %d", code)
	}
	if code, body := c.do("POST", "/api/v1/auth/telegram", map[string]string{"init_data": mk("42")}); code != 200 || body["kind"] != "telegram" {
		t.Fatalf("owner rejected: %d %v", code, body)
	}
}

func jsonNum(n int64) string { b, _ := json.Marshal(n); return string(b) }
