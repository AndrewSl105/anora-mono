// Package telegram is the owner-only Telegram bot. It runs in-process with
// long polling and calls the same task service as the REST API.
package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"

	"github.com/andrewsl105/anora-runner/internal/events"
	"github.com/andrewsl105/anora-runner/internal/store"
	"github.com/andrewsl105/anora-runner/internal/tasks"
)

// API is the subset of *telego.Bot the bot uses (faked in tests).
type API interface {
	SendMessage(ctx context.Context, params *telego.SendMessageParams) (*telego.Message, error)
	EditMessageText(ctx context.Context, params *telego.EditMessageTextParams) (*telego.Message, error)
	AnswerCallbackQuery(ctx context.Context, params *telego.AnswerCallbackQueryParams) error
}

type Bot struct {
	API     API
	Store   *store.Store
	Tasks   *tasks.Service
	Hub     *events.Hub
	OwnerID int64
	// WebAppURL is the Mini App base URL; MiniAppLink optionally is the
	// t.me/<bot>/<app> link used for deep links in groups.
	WebAppURL   string
	MiniAppLink string
	// EditInterval throttles status message edits (default 3 s).
	EditInterval time.Duration
	OnError      func(op string)

	// createMu makes "create task from Telegram, then record where it came
	// from" atomic with respect to the hub listener sending its status message.
	createMu sync.RWMutex
	// statusMu serialises sending the first status message of a task.
	statusMu sync.Mutex

	mu       sync.Mutex
	activity map[int64]string
	edits    map[int64]*editState
}

type editState struct {
	last    time.Time
	pending bool
}

func (b *Bot) init() {
	if b.EditInterval == 0 {
		b.EditInterval = 3 * time.Second
	}
	b.activity = map[int64]string{}
	b.edits = map[int64]*editState{}
}

// Run long-polls updates and follows the hub until ctx ends.
func (b *Bot) Run(ctx context.Context, tg *telego.Bot) error {
	b.init()
	updates, err := tg.UpdatesViaLongPolling(ctx, &telego.GetUpdatesParams{
		Timeout:        30,
		AllowedUpdates: []string{"message", "callback_query"},
	})
	if err != nil {
		return err
	}
	_ = tg.SetMyCommands(ctx, &telego.SetMyCommandsParams{Commands: commandList})
	go b.follow(ctx)
	for u := range updates {
		b.HandleUpdate(ctx, u)
	}
	return nil
}

// Start runs only the hub listener (tests drive HandleUpdate directly).
func (b *Bot) Start(ctx context.Context) {
	b.init()
	go b.follow(ctx)
}

func (b *Bot) fail(op string, err error) {
	if err == nil {
		return
	}
	slog.Warn("telegram", "op", op, "err", err)
	if b.OnError != nil {
		b.OnError(op)
	}
}

// ---- outgoing ----

type target struct {
	chatID, threadID int64
}

// targetFor is where a task's messages go: the chat it was created from, the
// project's forum topic, or the owner's private chat.
func (b *Bot) targetFor(ctx context.Context, t *store.Task) (target, store.TelegramTarget) {
	tt, _ := b.Store.TaskTelegram(ctx, t.ID)
	if tt.ChatID != 0 {
		return target{tt.ChatID, tt.ThreadID}, tt
	}
	if chat, thread, err := b.Store.ProjectTelegram(ctx, t.ProjectID); err == nil && chat != 0 {
		return target{chat, thread}, tt
	}
	return target{b.OwnerID, 0}, tt
}

func (b *Bot) send(ctx context.Context, to target, text string, kb *telego.InlineKeyboardMarkup) (int, error) {
	p := &telego.SendMessageParams{
		ChatID: tu.ID(to.chatID), MessageThreadID: int(to.threadID), Text: clamp(text), ParseMode: telego.ModeHTML,
		LinkPreviewOptions: &telego.LinkPreviewOptions{IsDisabled: true},
	}
	if kb != nil {
		p.ReplyMarkup = kb
	}
	m, err := b.API.SendMessage(ctx, p)
	if err != nil {
		b.fail("send", err)
		return 0, err
	}
	return m.MessageID, nil
}

func (b *Bot) edit(ctx context.Context, chatID int64, msgID int, text string, kb *telego.InlineKeyboardMarkup) {
	_, err := b.API.EditMessageText(ctx, &telego.EditMessageTextParams{
		ChatID: tu.ID(chatID), MessageID: msgID, Text: clamp(text), ParseMode: telego.ModeHTML,
		LinkPreviewOptions: &telego.LinkPreviewOptions{IsDisabled: true}, ReplyMarkup: kb,
	})
	if err != nil && !strings.Contains(err.Error(), "message is not modified") {
		b.fail("edit", err)
	}
}

func (b *Bot) appButton(label, path string, private bool) (telego.InlineKeyboardButton, bool) {
	if b.WebAppURL == "" {
		return telego.InlineKeyboardButton{}, false
	}
	url := strings.TrimRight(b.WebAppURL, "/") + path
	if private {
		return tu.InlineKeyboardButton(label).WithWebApp(&telego.WebAppInfo{URL: url}), true
	}
	if b.MiniAppLink != "" {
		param := "home"
		if strings.HasPrefix(path, "/tasks/") {
			param = "task_" + strings.TrimPrefix(path, "/tasks/")
		}
		return tu.InlineKeyboardButton(label).WithURL(b.MiniAppLink + "?startapp=" + param), true
	}
	return tu.InlineKeyboardButton(label).WithURL(url), true
}

// taskButtons: Open in app, Cancel (while not terminal), PR (when it exists).
func (b *Bot) taskButtons(t *store.Task, private bool) *telego.InlineKeyboardMarkup {
	var row []telego.InlineKeyboardButton
	if btn, ok := b.appButton("Open in app", fmt.Sprintf("/tasks/%d", t.ID), private); ok {
		row = append(row, btn)
	}
	if !tasks.IsTerminal(t.Status) {
		row = append(row, tu.InlineKeyboardButton("Cancel").WithCallbackData(fmt.Sprintf("cancel|%d", t.ID)))
	}
	if t.PRURL != nil {
		row = append(row, tu.InlineKeyboardButton("PR").WithURL(*t.PRURL))
	}
	if t.Status == tasks.Failed || t.Status == tasks.Cancelled {
		row = append(row, tu.InlineKeyboardButton("Retry").WithCallbackData(fmt.Sprintf("retry|%d", t.ID)))
	}
	if len(row) == 0 {
		return nil
	}
	return tu.InlineKeyboard(row)
}

// ensureStatus sends the status message once and remembers it.
func (b *Bot) ensureStatus(ctx context.Context, t *store.Task) (target, int) {
	b.createMu.RLock()
	defer b.createMu.RUnlock()
	b.statusMu.Lock()
	defer b.statusMu.Unlock()
	to, tt := b.targetFor(ctx, t)
	if tt.MessageID != 0 {
		return to, int(tt.MessageID)
	}
	id, err := b.send(ctx, to, renderStatus(t, b.activityOf(t.ID)), b.taskButtons(t, to.threadID == 0 && to.chatID == b.OwnerID))
	if err != nil {
		return to, 0
	}
	_ = b.Store.SetTaskTelegram(ctx, t.ID, store.TelegramTarget{ChatID: to.chatID, ThreadID: to.threadID, MessageID: int64(id)})
	b.mu.Lock()
	b.edits[t.ID] = &editState{last: time.Now()}
	b.mu.Unlock()
	return to, id
}

func (b *Bot) activityOf(id int64) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.activity[id]
}

// requestEdit coalesces status edits to one per EditInterval per message.
func (b *Bot) requestEdit(ctx context.Context, taskID int64) {
	b.mu.Lock()
	st := b.edits[taskID]
	if st == nil {
		st = &editState{}
		b.edits[taskID] = st
	}
	if st.pending {
		b.mu.Unlock()
		return
	}
	wait := b.EditInterval - time.Since(st.last)
	st.pending = true
	b.mu.Unlock()
	if wait < 0 {
		wait = 0
	}
	time.AfterFunc(wait, func() {
		b.mu.Lock()
		st.pending = false
		st.last = time.Now()
		b.mu.Unlock()
		b.editStatus(ctx, taskID)
	})
}

func (b *Bot) editStatus(ctx context.Context, taskID int64) {
	t, err := b.Store.GetTask(ctx, taskID)
	if err != nil {
		return
	}
	to, msgID := b.ensureStatus(ctx, t)
	if msgID == 0 {
		return
	}
	b.edit(ctx, to.chatID, msgID, renderStatus(t, b.activityOf(taskID)), b.taskButtons(t, to.threadID == 0 && to.chatID == b.OwnerID))
}

// follow turns hub messages into Telegram messages.
func (b *Bot) follow(ctx context.Context) {
	msgs, unsub := b.Hub.Subscribe(1024)
	defer unsub()
	for {
		select {
		case <-ctx.Done():
			return
		case m, ok := <-msgs:
			if !ok {
				return
			}
			if m.Task != nil {
				b.onTask(ctx, m.Task)
			}
			if m.Event != nil {
				b.onEvent(ctx, m.Event)
			}
		}
	}
}

func (b *Bot) onTask(ctx context.Context, t *store.Task) {
	if _, msgID := b.ensureStatus(ctx, t); msgID != 0 {
		b.requestEdit(ctx, t.ID)
	}
	if t.Status == tasks.Failed && len(t.StatusEvents) > 0 {
		to, _ := b.targetFor(ctx, t)
		_, _ = b.send(ctx, to, fmt.Sprintf("❌ <b>#%d failed</b>: %s", t.ID, esc(cut(deref(t.StatusReason), 500))),
			b.taskButtons(t, to.chatID == b.OwnerID))
	}
}

type payload struct {
	Type     string   `json:"type"`
	Tool     string   `json:"tool"`
	Summary  string   `json:"summary"`
	Text     string   `json:"text"`
	Code     string   `json:"code"`
	Message  string   `json:"message"`
	Number   int      `json:"number"`
	URL      string   `json:"url"`
	Status   string   `json:"status"`
	Name     string   `json:"name"`
	Question string   `json:"question_id"`
	Options  []string `json:"options"`
}

func (b *Bot) onEvent(ctx context.Context, ev *store.Event) {
	var p payload
	_ = json.Unmarshal(ev.Payload, &p)
	switch ev.Type {
	case "tool_use", "assistant_text":
		line := p.Summary
		if ev.Type == "assistant_text" {
			line = strings.SplitN(strings.TrimSpace(p.Text), "\n", 2)[0]
		}
		if line == "" {
			return
		}
		b.mu.Lock()
		b.activity[ev.TaskID] = line
		b.mu.Unlock()
		b.requestEdit(ctx, ev.TaskID)
	case "question":
		if ev.RunID != nil {
			b.sendQuestion(ctx, ev.TaskID, tasks.QuestionID(*ev.RunID, p.Question))
		}
	case "answer":
		b.refreshQuestion(ctx, ev.TaskID, p.Question)
	case "pr_opened":
		b.notify(ctx, ev.TaskID, func(t *store.Task) string {
			return fmt.Sprintf("🔀 <b>#%d PR opened</b>: <a href=\"%s\">#%d</a> %s", t.ID, esc(p.URL), p.Number, esc(cut(t.Title, 100)))
		})
	case "ci":
		if p.Status == "failure" {
			b.notify(ctx, ev.TaskID, func(t *store.Task) string {
				link := esc(cut(p.Name, 80))
				if p.URL != "" {
					link = fmt.Sprintf("<a href=\"%s\">%s</a>", esc(p.URL), link)
				}
				return fmt.Sprintf("🔴 <b>#%d CI failed</b>: %s", t.ID, link)
			})
		} else {
			b.requestEdit(ctx, ev.TaskID)
		}
	case "error":
		if p.Code == "limit_exceeded" {
			b.notify(ctx, ev.TaskID, func(t *store.Task) string {
				return fmt.Sprintf("⏱ <b>#%d hit a limit</b>: %s", t.ID, esc(cut(p.Message, 300)))
			})
		}
	}
}

func (b *Bot) notify(ctx context.Context, taskID int64, text func(*store.Task) string) {
	t, err := b.Store.GetTask(ctx, taskID)
	if err != nil {
		return
	}
	to, _ := b.targetFor(ctx, t)
	_, _ = b.send(ctx, to, text(t), b.taskButtons(t, to.chatID == b.OwnerID))
}

func (b *Bot) questionKeyboard(q *store.Question) *telego.InlineKeyboardMarkup {
	if q.Answer != nil || len(q.Options) == 0 {
		return nil
	}
	var rows [][]telego.InlineKeyboardButton
	for i, o := range q.Options {
		data := fmt.Sprintf("ans|%s|%d", q.ID, i)
		if len(data) > 64 {
			continue
		}
		rows = append(rows, tu.InlineKeyboardRow(tu.InlineKeyboardButton(cut(o, 60)).WithCallbackData(data)))
	}
	if len(rows) == 0 {
		return nil
	}
	return tu.InlineKeyboard(rows...)
}

func (b *Bot) sendQuestion(ctx context.Context, taskID int64, questionID string) {
	t, err := b.Store.GetTask(ctx, taskID)
	if err != nil {
		return
	}
	q, err := b.Store.GetQuestion(ctx, questionID)
	if err != nil || q.TelegramMessageID != nil {
		return
	}
	to, _ := b.targetFor(ctx, t)
	id, err := b.send(ctx, to, renderQuestion(t, q), b.questionKeyboard(q))
	if err == nil {
		_ = b.Store.SetQuestionTelegramMessage(ctx, q.ID, int64(id))
	}
}

// refreshQuestion edits a question message to show the answer and who gave it.
func (b *Bot) refreshQuestion(ctx context.Context, taskID int64, questionID string) {
	q, err := b.Store.GetQuestion(ctx, questionID)
	if err != nil || q.TelegramMessageID == nil {
		return
	}
	t, err := b.Store.GetTask(ctx, taskID)
	if err != nil {
		return
	}
	to, _ := b.targetFor(ctx, t)
	b.edit(ctx, to.chatID, int(*q.TelegramMessageID), renderQuestion(t, q), nil)
}

// ---- incoming ----

// HandleUpdate processes one update from the owner; everything else is dropped.
func (b *Bot) HandleUpdate(ctx context.Context, u telego.Update) {
	switch {
	case u.Message != nil:
		m := u.Message
		if m.From == nil || m.From.ID != b.OwnerID {
			slog.Info("telegram: ignored message from non-owner", "from", userID(m.From), "chat", m.Chat.ID)
			return
		}
		b.onMessage(ctx, m)
	case u.CallbackQuery != nil:
		cq := u.CallbackQuery
		if cq.From.ID != b.OwnerID {
			slog.Info("telegram: ignored callback from non-owner", "from", cq.From.ID)
			return
		}
		b.onCallback(ctx, cq)
	}
}

func userID(u *telego.User) int64 {
	if u == nil {
		return 0
	}
	return u.ID
}

func (b *Bot) reply(ctx context.Context, m *telego.Message, text string, kb *telego.InlineKeyboardMarkup) {
	_, _ = b.send(ctx, target{m.Chat.ID, int64(m.MessageThreadID)}, text, kb)
}

func (b *Bot) onCallback(ctx context.Context, cq *telego.CallbackQuery) {
	answer := func(text string) {
		b.fail("answer_callback", b.API.AnswerCallbackQuery(ctx, &telego.AnswerCallbackQueryParams{CallbackQueryID: cq.ID, Text: text}))
	}
	parts := strings.Split(cq.Data, "|")
	switch parts[0] {
	case "ans":
		if len(parts) != 3 {
			answer("Unknown action")
			return
		}
		q, err := b.Store.GetQuestion(ctx, parts[1])
		i, _ := strconv.Atoi(parts[2])
		if err != nil || i < 0 || i >= len(q.Options) {
			answer("Question not found")
			return
		}
		if _, err := b.Tasks.Answer(ctx, q.TaskID, q.ID, q.Options[i], "telegram"); err != nil {
			answer(errText(err))
			return
		}
		answer("Answered: " + cut(q.Options[i], 150))
	case "cancel", "retry", "task":
		if len(parts) != 2 {
			answer("Unknown action")
			return
		}
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		var err error
		switch parts[0] {
		case "cancel":
			_, err = b.Tasks.Cancel(ctx, id)
		case "retry":
			_, err = b.Tasks.Retry(ctx, id)
		case "task":
			if m := cq.Message; m != nil {
				b.showTask(ctx, target{m.GetChat().ID, 0}, id)
			}
		}
		if err != nil {
			answer(errText(err))
			return
		}
		answer("OK")
	default:
		answer("Unknown action")
	}
}

func errText(err error) string {
	switch {
	case errors.Is(err, store.ErrConflict):
		msg := err.Error()
		if msg == store.ErrConflict.Error() {
			return "Already answered or not allowed now"
		}
		return cut(msg, 190)
	case errors.Is(err, store.ErrNotFound):
		return "Not found"
	}
	return cut(err.Error(), 190)
}
