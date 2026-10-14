package telegram

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/andrewsl105/anora-runner/internal/events"
	"github.com/andrewsl105/anora-runner/internal/projects"
	"github.com/andrewsl105/anora-runner/internal/store"
	"github.com/andrewsl105/anora-runner/internal/tasks"
	"github.com/andrewsl105/anora-runner/internal/testutil"
)

type sent struct {
	chat, thread int64
	msgID        int
	text         string
	markup       any
}

type fakeAPI struct {
	mu        sync.Mutex
	nextID    int
	sends     []sent
	edits     []sent
	callbacks []string
}

func (f *fakeAPI) SendMessage(_ context.Context, p *telego.SendMessageParams) (*telego.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	f.sends = append(f.sends, sent{chat: p.ChatID.ID, thread: int64(p.MessageThreadID), msgID: f.nextID, text: p.Text, markup: p.ReplyMarkup})
	return &telego.Message{MessageID: f.nextID}, nil
}

func (f *fakeAPI) EditMessageText(_ context.Context, p *telego.EditMessageTextParams) (*telego.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edits = append(f.edits, sent{chat: p.ChatID.ID, msgID: p.MessageID, text: p.Text})
	return &telego.Message{MessageID: p.MessageID}, nil
}

func (f *fakeAPI) AnswerCallbackQuery(_ context.Context, p *telego.AnswerCallbackQueryParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callbacks = append(f.callbacks, p.Text)
	return nil
}

func (f *fakeAPI) snapshot() ([]sent, []sent, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sent(nil), f.sends...), append([]sent(nil), f.edits...), append([]string(nil), f.callbacks...)
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

const owner = 42

func msg(chat int64, thread int, text string) telego.Update {
	return telego.Update{Message: &telego.Message{
		MessageID: 1000, MessageThreadID: thread, From: &telego.User{ID: owner},
		Chat: telego.Chat{ID: chat, Type: "supergroup"}, Text: text,
	}}
}

func TestBotFlow(t *testing.T) {
	st := testutil.DB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := events.NewHub()
	svc := &tasks.Service{Store: st, Hub: hub}
	name := "Demo"
	_, _ = (&projects.Service{Store: st}).Create(ctx, projects.CreateInput{ID: "demo", ProjectFields: store.ProjectFields{Name: &name}})
	_, _ = (&projects.Service{Store: st}).AddRepository(ctx, "demo", projects.RepoInput{FullName: "o/r", InstallationID: 1})

	api := &fakeAPI{}
	b := &Bot{API: api, Store: st, Tasks: svc, Hub: hub, OwnerID: owner, WebAppURL: "https://app.example.com", EditInterval: 300 * time.Millisecond}
	b.Start(ctx)

	// Strangers are ignored.
	stranger := msg(-100, 0, "/projects")
	stranger.Message.From.ID = 7
	b.HandleUpdate(ctx, stranger)
	if s, _, _ := api.snapshot(); len(s) != 0 {
		t.Fatal("bot answered a stranger")
	}

	// /bind in a topic, then plain text there creates a task.
	b.HandleUpdate(ctx, msg(-100, 5, "/bind@anora_bot demo"))
	b.HandleUpdate(ctx, msg(-100, 5, "Add CSV export <b>now</b>"))
	var taskID int64
	eventually(t, "task created", func() bool {
		list, _ := st.ListTasks(ctx, store.TaskFilter{Limit: 5})
		if len(list) == 1 {
			taskID = list[0].ID
		}
		return taskID != 0
	})
	eventually(t, "status message", func() bool {
		s, _, _ := api.snapshot()
		for _, m := range s {
			if strings.Contains(m.text, "Status: queued") {
				return m.chat == -100 && m.thread == 5 && strings.Contains(m.text, "&lt;b&gt;now&lt;/b&gt;")
			}
		}
		return false
	})
	sends, _, _ := api.snapshot()
	statusCount := 0
	for _, m := range sends {
		if strings.Contains(m.text, "Status:") {
			statusCount++
		}
	}
	if statusCount != 1 {
		t.Fatalf("expected exactly one status message, got %d", statusCount)
	}

	// Activity edits are throttled and coalesced.
	run, _ := st.PendingRun(ctx, taskID)
	_ = st.MarkRunStarting(ctx, run.ID)
	for i := 0; i < 10; i++ {
		ev, _ := st.AddRunnerEvent(ctx, taskID, run.ID, int64(i+1), "tool_use", []byte(`{"type":"tool_use","tool":"Edit","summary":"Edit file`+string(rune('a'+i))+`.go"}`))
		hub.PublishEvent(ev)
	}
	time.Sleep(700 * time.Millisecond)
	_, edits, _ := api.snapshot()
	if len(edits) == 0 || len(edits) > 3 {
		t.Fatalf("expected 1-3 coalesced edits, got %d", len(edits))
	}

	// A question is sent with option buttons; tapping one answers it.
	if _, err := st.SetStatus(ctx, taskID, nil, tasks.WaitingInput, "", false); err != nil {
		t.Fatal(err)
	}
	qid := tasks.QuestionID(run.ID, "q1")
	_, _ = st.CreateQuestion(ctx, store.Question{ID: qid, TaskID: taskID, RunID: run.ID, Text: "Which format?", Options: []string{"CSV", "XLSX"}})
	ev, _ := st.AddRunnerEvent(ctx, taskID, run.ID, 20, "question", []byte(`{"type":"question","question_id":"q1","text":"Which format?","options":["CSV","XLSX"]}`))
	hub.PublishEvent(ev)
	eventually(t, "question message", func() bool {
		q, _ := st.GetQuestion(ctx, qid)
		return q.TelegramMessageID != nil
	})
	b.HandleUpdate(ctx, telego.Update{CallbackQuery: &telego.CallbackQuery{ID: "cb", From: telego.User{ID: owner}, Data: "ans|" + qid + "|1"}})
	q, _ := st.GetQuestion(ctx, qid)
	if q.Answer == nil || *q.Answer != "XLSX" || *q.AnsweredVia != "telegram" {
		t.Fatalf("answer %+v", q)
	}
	eventually(t, "question message edited with answer", func() bool {
		_, edits, _ := api.snapshot()
		for _, e := range edits {
			if e.msgID == int(*q.TelegramMessageID) && strings.Contains(e.text, "(Telegram): XLSX") {
				return true
			}
		}
		return false
	})

	// Replying to the question message again: first answer already won.
	reply := msg(-100, 5, "CSV please")
	reply.Message.ReplyToMessage = &telego.Message{MessageID: int(*q.TelegramMessageID)}
	b.HandleUpdate(ctx, reply)
	eventually(t, "conflict reply", func() bool {
		s, _, _ := api.snapshot()
		return strings.Contains(s[len(s)-1].text, "Already answered")
	})

	// /msg on a queued task is rejected; /cancel works.
	b.HandleUpdate(ctx, msg(-100, 5, "/cancel "+itoa(taskID)))
	tk, _ := st.GetTask(ctx, taskID)
	if tk.Status != tasks.Cancelled {
		t.Fatalf("status %s", tk.Status)
	}
	b.HandleUpdate(ctx, msg(-100, 5, "/tasks"))
	eventually(t, "tasks reply", func() bool {
		s, _, _ := api.snapshot()
		return strings.Contains(s[len(s)-1].text, "No active tasks")
	})
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestParseCommand(t *testing.T) {
	cases := []struct{ in, cmd, args string }{
		{"/new vatary Add export", "new", "vatary Add export"},
		{"/tasks@anora_bot", "tasks", ""},
		{"/MSG 12 hi there", "msg", "12 hi there"},
		{"/new\nmulti line", "new", "multi line"},
	}
	for _, c := range cases {
		cmd, args, ok := ParseCommand(c.in)
		if !ok || cmd != c.cmd || args != c.args {
			t.Errorf("%q: got %q %q %v", c.in, cmd, args, ok)
		}
	}
	if _, _, ok := ParseCommand("hello"); ok {
		t.Fatal("plain text is not a command")
	}
	if id, rest, ok := splitID("#12 do it"); !ok || id != 12 || rest != "do it" {
		t.Fatal("splitID")
	}
}

func TestFormatting(t *testing.T) {
	if esc("<a&b>") != "&lt;a&amp;b&gt;" {
		t.Fatal("escape")
	}
	if got := cut("héllo world", 5); got != "héll…" {
		t.Fatalf("cut %q", got)
	}
	long := strings.Repeat("я", 5000)
	if n := len([]rune(clamp(long))); n != maxMessage {
		t.Fatalf("clamp %d", n)
	}
	title := strings.Repeat("x", 500)
	pr, num := "https://github.com/o/r/pull/3", 3
	s := renderStatus(&store.Task{ID: 1, Title: title, ProjectID: "p", Status: "pr_open", PRURL: &pr, PRNumber: &num, CostUSDTotal: 1.234}, "Editing x")
	if len([]rune(s)) > maxMessage || !strings.Contains(s, "PR: <a href") || strings.Contains(s, "Editing x") || !strings.Contains(s, "$1.23") {
		t.Fatalf("status render: %s", s)
	}
}
