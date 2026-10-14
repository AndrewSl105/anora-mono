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
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

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

	// Backward pagination: before_id=0 starts at the newest event.
	_, all := c.do("GET", "/api/v1/tasks/"+fmt.Sprint(id)+"/events", nil)
	allItems := all["items"].([]any)
	code, back := c.do("GET", "/api/v1/tasks/"+fmt.Sprint(id)+"/events?before_id=0&limit=1", nil)
	backItems := back["items"].([]any)
	if code != 200 || len(backItems) != 1 || backItems[0].(map[string]any)["id"] != allItems[len(allItems)-1].(map[string]any)["id"] {
		t.Fatalf("events before_id=0: %v", back)
	}
	if len(allItems) > 1 {
		code, back2 := c.do("GET", "/api/v1/tasks/"+fmt.Sprint(id)+"/events?before_id="+back["next_cursor"].(string), nil)
		items := back2["items"].([]any)
		if code != 200 || len(items) != len(allItems)-1 || items[0].(map[string]any)["id"] != allItems[len(allItems)-2].(map[string]any)["id"] {
			t.Fatalf("events before_id page 2: %v", back2)
		}
	}
	if code, _ := c.do("GET", "/api/v1/tasks/"+fmt.Sprint(id)+"/events?before_id=5&after_id=1", nil); code != 400 {
		t.Fatalf("before_id with after_id: %d", code)
	}

	code, runs := c.do("GET", "/api/v1/tasks/"+fmt.Sprint(id)+"/runs", nil)
	runItems, _ := runs["items"].([]any)
	// Oldest first: the initial run, then the queued resume run (not started yet).
	if code != 200 || len(runItems) != 2 || runItems[0].(map[string]any)["kind"] != "initial" ||
		runItems[0].(map[string]any)["turns"] != float64(0) || runItems[1].(map[string]any)["started_at"] != nil {
		t.Fatalf("runs: %d %v", code, runs)
	}
	if code, _ := c.do("GET", "/api/v1/tasks/999999999/runs", nil); code != 404 {
		t.Fatalf("runs of unknown task: %d", code)
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

func TestWebSocket(t *testing.T) {
	c, _, secret := setup(t)
	otp, _ := totp.GenerateCode(secret, time.Now())
	c.do("POST", "/api/v1/auth/login", map[string]string{"password": "pw-for-tests", "totp": otp})
	c.do("POST", "/api/v1/projects", map[string]any{"id": "demo", "name": "Demo"})
	c.do("POST", "/api/v1/projects/demo/repositories", map[string]any{"full_name": "o/r", "installation_id": 1})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(c.base, "http") + "/api/v1/ws"
	if _, _, err := websocket.Dial(ctx, wsURL, nil); err == nil {
		t.Fatal("websocket without session must fail")
	}
	u, _ := url.Parse(c.base)
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: c.http, HTTPHeader: http.Header{
		"Cookie": {cookieHeader(c.http.Jar.Cookies(u))},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	_ = wsjson.Write(ctx, conn, map[string]any{"op": "subscribe", "scope": "tasks"})
	_ = wsjson.Write(ctx, conn, map[string]any{"op": "bogus"})
	var msg map[string]any
	if err := wsjson.Read(ctx, conn, &msg); err != nil || msg["op"] != "error" {
		t.Fatalf("expected error op, got %v %v", msg, err)
	}
	_, task := c.do("POST", "/api/v1/tasks", map[string]any{"project_id": "demo", "prompt": "hello"})
	id := int64(task["id"].(float64))
	if err := wsjson.Read(ctx, conn, &msg); err != nil || msg["op"] != "task" {
		t.Fatalf("expected task push, got %v %v", msg, err)
	}

	// Per-task subscription also receives timeline events.
	_ = wsjson.Write(ctx, conn, map[string]any{"op": "subscribe", "task_id": id})
	time.Sleep(100 * time.Millisecond)
	c.do("POST", "/api/v1/tasks/"+fmt.Sprint(id)+"/cancel", nil)
	gotEvent := false
	for i := 0; i < 4 && !gotEvent; i++ {
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			t.Fatal(err)
		}
		gotEvent = msg["op"] == "event"
	}
	if !gotEvent {
		t.Fatal("no event push for subscribed task")
	}
}

func cookieHeader(cs []*http.Cookie) string {
	var parts []string
	for _, c := range cs {
		parts = append(parts, c.Name+"="+c.Value)
	}
	return strings.Join(parts, "; ")
}
