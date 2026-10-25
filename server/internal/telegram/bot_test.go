package telegram

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/andrewsl105/fleetpocket/server/internal/events"
	"github.com/andrewsl105/fleetpocket/server/internal/projects"
	"github.com/andrewsl105/fleetpocket/server/internal/store"
	"github.com/andrewsl105/fleetpocket/server/internal/tasks"
	"github.com/andrewsl105/fleetpocket/server/internal/testutil"
)

type sent struct {
	chat, thread int64
	msgID        int
	text         string
	markup       any
	silent       bool
}

type fakeAPI struct {
	mu        sync.Mutex
	nextID    int
	sends     []sent
	edits     []sent
	callbacks []string
	reactions []string
}

func (f *fakeAPI) SendMessage(_ context.Context, p *telego.SendMessageParams) (*telego.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	f.sends = append(f.sends, sent{chat: p.ChatID.ID, thread: int64(p.MessageThreadID), msgID: f.nextID, text: p.Text, markup: p.ReplyMarkup, silent: p.DisableNotification})
	return &telego.Message{MessageID: f.nextID}, nil
}

func (f *fakeAPI) EditMessageText(_ context.Context, p *telego.EditMessageTextParams) (*telego.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edits = append(f.edits, sent{chat: p.ChatID.ID, msgID: p.MessageID, text: p.Text, markup: p.ReplyMarkup})
	return &telego.Message{MessageID: p.MessageID}, nil
}

func (f *fakeAPI) EditMessageReplyMarkup(_ context.Context, p *telego.EditMessageReplyMarkupParams) (*telego.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var markup any
	if p.ReplyMarkup != nil { // a typed nil in `any` would not compare equal to nil
		markup = p.ReplyMarkup
	}
	f.edits = append(f.edits, sent{chat: p.ChatID.ID, msgID: p.MessageID, markup: markup})
	return &telego.Message{MessageID: p.MessageID}, nil
}

func (f *fakeAPI) SetMessageReaction(_ context.Context, p *telego.SetMessageReactionParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(p.Reaction) > 0 {
		f.reactions = append(f.reactions, p.Reaction[0].(*telego.ReactionTypeEmoji).Emoji)
	}
	return nil
}

func (f *fakeAPI) GetFile(_ context.Context, p *telego.GetFileParams) (*telego.File, error) {
	return &telego.File{FileID: p.FileID, FilePath: p.FileID}, nil
}

func (f *fakeAPI) FileDownloadURL(path string) string { return path }

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

func (f *fakeAPI) reactionSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reactions...)
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

	// /bind in a topic, then plain text there creates a durable draft.
	b.HandleUpdate(ctx, msg(-100, 5, "/bind@fleetpocket_bot demo"))
	b.HandleUpdate(ctx, msg(-100, 5, "Add CSV export <b>now</b>"))
	var draftID int64
	eventually(t, "draft card", func() bool {
		if err := st.Pool.QueryRow(ctx, `SELECT id FROM telegram_drafts ORDER BY id DESC LIMIT 1`).Scan(&draftID); err != nil {
			return false
		}
		s, _, _ := api.snapshot()
		return len(s) > 0 && strings.Contains(s[len(s)-1].text, "Create a task?")
	})
	var createData string
	sends, _, _ := api.snapshot()
	for _, row := range sends[len(sends)-1].markup.(*telego.InlineKeyboardMarkup).InlineKeyboard {
		for _, button := range row {
			if button.Text == "✅ Create" {
				createData = button.CallbackData
			}
		}
	}
	if createData == "" {
		t.Fatal("missing create callback")
	}
	b.HandleUpdate(ctx, telego.Update{CallbackQuery: &telego.CallbackQuery{ID: "create", From: telego.User{ID: owner}, Data: createData}})
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
			if strings.Contains(m.text, "⏳ In queue") {
				return m.chat == -100 && m.thread == 5 && strings.Contains(m.text, "&lt;b&gt;now&lt;/b&gt;")
			}
		}
		return false
	})
	sends, _, _ = api.snapshot()
	statusCount := 0
	for _, m := range sends {
		if strings.Contains(m.text, "⏳ In queue") {
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
	var answerData string
	sends, _, _ = api.snapshot()
	for _, row := range sends[len(sends)-1].markup.(*telego.InlineKeyboardMarkup).InlineKeyboard {
		for _, button := range row {
			if button.Text == "XLSX" {
				answerData = button.CallbackData
			}
		}
	}
	b.HandleUpdate(ctx, telego.Update{CallbackQuery: &telego.CallbackQuery{ID: "cb", From: telego.User{ID: owner}, Data: answerData}})
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

	// Replying to an answered question message becomes a task follow-up.
	reply := msg(-100, 5, "CSV please")
	reply.Message.ReplyToMessage = &telego.Message{MessageID: int(*q.TelegramMessageID)}
	b.HandleUpdate(ctx, reply)
	eventually(t, "queued follow-up", func() bool {
		tk, _ := st.GetTask(ctx, taskID)
		return tk.QueuedMessage != nil && *tk.QueuedMessage == "CSV please"
	})

	// /cancel works.
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
		{"/tasks@fleetpocket_bot", "tasks", ""},
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
	if len([]rune(s)) > maxMessage || !strings.Contains(s, "PR #3") || strings.Contains(s, "Editing x") || !strings.Contains(s, "$1.23") {
		t.Fatalf("status render: %s", s)
	}
}

func TestCardRenderingStates(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	started := now.Add(-2*time.Minute - 5*time.Second)
	activity := "Edit src/x.ts"
	ciFail, reason, pr := "failure", "invalid CLAUDE_CODE_OAUTH_TOKEN", 17
	cases := []struct {
		status string
		want   []string
	}{
		{"queued", []string{"⏳ In queue"}},
		{"preparing", []string{"⏳ Preparing…"}},
		{"running", []string{"⏳ Running · 2m · 3 turns · $1.25", "✏️ Editing src/x.ts"}},
		{"waiting_input", []string{"❓ Waiting for your answer"}},
		{"pr_open", []string{"✅ PR #17 · CI ✗ · $1.25"}},
		{"failed", []string{"❌ Claude authentication failed"}},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			task := &store.Task{ID: 9, ProjectID: "demo", Title: "Do work", Status: tc.status, CreatedAt: started, Turns: 3, CostUSDTotal: 1.25,
				LastActivity: &activity, CIStatus: &ciFail, StatusReason: &reason, PRNumber: &pr}
			got := renderCard(cardData{Task: task, Repo: "org/repo", Run: &store.Run{StartedAt: &started}, Now: now})
			if !strings.Contains(got, "#9 · Do work") || !strings.Contains(got, "demo / org/repo") {
				t.Fatalf("header: %s", got)
			}
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Fatalf("want %q in %s", want, got)
				}
			}
		})
	}
}

func TestErrorHumanizer(t *testing.T) {
	cases := map[string]string{
		"invalid CLAUDE_CODE_OAUTH_TOKEN":    "Claude authentication failed",
		"fatal: remote branch dev not found": "base branch was not found",
		"max budget exceeded":                "budget limit was reached",
		"max turns reached turn limit":       "turn limit was reached",
		"deadline exceeded":                  "time limit was reached",
		"lost run: container missing":        "running agent was lost",
	}
	for raw, want := range cases {
		if got := humanizeError(raw); !strings.Contains(strings.ToLower(got.Summary), strings.ToLower(want)) || got.Raw != raw {
			t.Errorf("%q: %+v", raw, got)
		}
	}
	task := &store.Task{ID: 1}
	if got := renderErrorNotification(task, "Task failed", `<bad & raw>`); !strings.Contains(got, "<blockquote expandable>") || !strings.Contains(got, "&lt;bad &amp; raw&gt;") {
		t.Fatalf("notification: %s", got)
	}
}

func TestDraftMergingAlbumAndText(t *testing.T) {
	st := testutil.DB(t)
	ctx := context.Background()
	name := "Demo"
	_, _ = (&projects.Service{Store: st}).Create(ctx, projects.CreateInput{ID: "demo", ProjectFields: store.ProjectFields{Name: &name}})
	first, err := st.MergeTelegramDraft(ctx, -10, 4, 1, "demo", 0, "Please update this", nil, time.Now().Add(-3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.MergeTelegramDraft(ctx, -10, 4, 2, "demo", 0, "", []store.TelegramDraftMedia{{FileID: "a", Name: "a.jpg", Kind: "image"}}, time.Now().Add(-3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	third, err := st.MergeTelegramDraft(ctx, -10, 4, 3, "demo", 0, "and keep the colors", []store.TelegramDraftMedia{{FileID: "b", Name: "b.jpg", Kind: "image"}}, time.Now().Add(-3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || second.ID != third.ID || len(third.Media) != 2 || third.Text != "Please update this\n\nand keep the colors" {
		t.Fatalf("merged draft: %+v", third)
	}
}

func TestCallbackDataAndStaleButtonCleanup(t *testing.T) {
	st := testutil.DB(t)
	ctx := context.Background()
	api := &fakeAPI{}
	hub := events.NewHub()
	svc := &tasks.Service{Store: st, Hub: hub}
	name := "Demo"
	_, _ = (&projects.Service{Store: st}).Create(ctx, projects.CreateInput{ID: "demo", ProjectFields: store.ProjectFields{Name: &name}})
	_, _ = (&projects.Service{Store: st}).AddRepository(ctx, "demo", projects.RepoInput{FullName: "o/r", InstallationID: 1})
	v, err := svc.Create(ctx, tasks.CreateInput{ProjectID: "demo", Prompt: "work", Source: "telegram"})
	if err != nil {
		t.Fatal(err)
	}
	b := &Bot{API: api, Store: st, Tasks: svc, Hub: hub, OwnerID: owner, WebAppURL: "https://app.example.com"}
	b.init()
	kb := b.taskButtons(ctx, v.Task, true)
	for _, row := range kb.InlineKeyboard {
		for _, button := range row {
			if len(button.CallbackData) > 64 {
				t.Fatalf("callback is %d bytes", len(button.CallbackData))
			}
		}
	}
	if err := st.AddTelegramTaskMessage(ctx, store.TelegramMessage{ChatID: owner, MessageID: 88, TaskID: v.ID, Kind: "failed", HasButtons: true}); err != nil {
		t.Fatal(err)
	}
	v.StatusEvents = []*store.Event{{TaskID: v.ID, Type: "status"}}
	b.onTask(ctx, v.Task)
	_, edits, _ := api.snapshot()
	found := false
	for _, edit := range edits {
		if edit.msgID == 88 && edit.text == "" && edit.markup == nil {
			found = true
		}
	}
	if !found {
		t.Fatalf("stale buttons were not cleared: %+v", edits)
	}
}

func TestReplyRoutingQuestionThenFollowup(t *testing.T) {
	st := testutil.DB(t)
	ctx := context.Background()
	hub := events.NewHub()
	svc := &tasks.Service{Store: st, Hub: hub}
	name := "Demo"
	_, _ = (&projects.Service{Store: st}).Create(ctx, projects.CreateInput{ID: "demo", ProjectFields: store.ProjectFields{Name: &name}})
	_, _ = (&projects.Service{Store: st}).AddRepository(ctx, "demo", projects.RepoInput{FullName: "o/r", InstallationID: 1})
	v, err := svc.Create(ctx, tasks.CreateInput{ProjectID: "demo", Prompt: "work"})
	if err != nil {
		t.Fatal(err)
	}
	run, _ := st.PendingRun(ctx, v.ID)
	_ = st.MarkRunStarting(ctx, run.ID)
	_, _ = st.SetStatus(ctx, v.ID, nil, tasks.WaitingInput, "", false)
	qid := tasks.QuestionID(run.ID, "q")
	_, _ = st.CreateQuestion(ctx, store.Question{ID: qid, TaskID: v.ID, RunID: run.ID, Text: "Which?"})
	b := &Bot{API: &fakeAPI{}, Store: st, Tasks: svc, Hub: hub, OwnerID: owner}
	b.init()
	first := msg(owner, 0, "Use CSV")
	if err := b.routeTaskReply(ctx, first.Message, v.ID); err != nil {
		t.Fatal(err)
	}
	q, _ := st.GetQuestion(ctx, qid)
	if q.Answer == nil || *q.Answer != "Use CSV" {
		t.Fatalf("question not answered: %+v", q)
	}
	second := msg(owner, 0, "Also add tests")
	if err := b.routeTaskReply(ctx, second.Message, v.ID); err != nil {
		t.Fatal(err)
	}
	task, _ := st.GetTask(ctx, v.ID)
	if task.QueuedMessage == nil || *task.QueuedMessage != "Also add tests" {
		t.Fatalf("follow-up not queued: %+v", task.QueuedMessage)
	}
}
