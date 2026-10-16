// Package telegram is the owner-only Telegram bot. It runs in-process with
// long polling and calls the same task service as the REST API.
package telegram

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"

	"github.com/andrewsl105/anora/server/internal/events"
	"github.com/andrewsl105/anora/server/internal/store"
	"github.com/andrewsl105/anora/server/internal/tasks"
)

type API interface {
	SendMessage(context.Context, *telego.SendMessageParams) (*telego.Message, error)
	EditMessageText(context.Context, *telego.EditMessageTextParams) (*telego.Message, error)
	EditMessageReplyMarkup(context.Context, *telego.EditMessageReplyMarkupParams) (*telego.Message, error)
	AnswerCallbackQuery(context.Context, *telego.AnswerCallbackQueryParams) error
	SetMessageReaction(context.Context, *telego.SetMessageReactionParams) error
	GetFile(context.Context, *telego.GetFileParams) (*telego.File, error)
	FileDownloadURL(string) string
}

type Bot struct {
	API     API
	Store   *store.Store
	Tasks   *tasks.Service
	Hub     *events.Hub
	OwnerID int64

	WebAppURL, MiniAppLink string
	EditInterval           time.Duration
	MergeWindow            time.Duration
	AlbumWait              time.Duration // how long an album is collected after its latest photo
	STT                    Transcriber
	STTEnabled             bool
	OnError                func(string)

	createMu sync.RWMutex
	statusMu sync.Mutex
	mu       sync.Mutex
	edits    map[int64]*editState
	images   imageState
}

type editState struct {
	last    time.Time
	pending bool
}

type target struct{ chatID, threadID int64 }

func (b *Bot) init() {
	if b.EditInterval == 0 {
		b.EditInterval = 3 * time.Second
	}
	if b.MergeWindow == 0 {
		b.MergeWindow = 3 * time.Second
	}
	if b.edits == nil {
		b.edits = map[int64]*editState{}
	}
}

func (b *Bot) Run(ctx context.Context, tg *telego.Bot) error {
	b.init()
	updates, err := tg.UpdatesViaLongPolling(ctx, &telego.GetUpdatesParams{Timeout: 30, AllowedUpdates: []string{"message", "callback_query"}})
	if err != nil {
		return err
	}
	b.fail("set_commands", tg.SetMyCommands(ctx, &telego.SetMyCommandsParams{Commands: commandList}))
	if b.WebAppURL != "" {
		b.fail("set_menu", tg.SetChatMenuButton(ctx, &telego.SetChatMenuButtonParams{MenuButton: &telego.MenuButtonWebApp{
			Type: telego.ButtonTypeWebApp, Text: "Open Anora", WebApp: telego.WebAppInfo{URL: b.WebAppURL},
		}}))
	}
	go b.follow(ctx)
	for u := range updates {
		b.HandleUpdate(ctx, u)
	}
	return nil
}

func (b *Bot) Start(ctx context.Context) { b.init(); go b.follow(ctx) }

func (b *Bot) fail(op string, err error) {
	if err == nil {
		return
	}
	slog.Warn("telegram", "op", op, "err", err)
	if b.OnError != nil {
		b.OnError(op)
	}
}

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

func (b *Bot) sendMode(ctx context.Context, to target, text string, markup telego.ReplyMarkup, replyTo int, sound bool) (int, error) {
	p := &telego.SendMessageParams{ChatID: tu.ID(to.chatID), MessageThreadID: int(to.threadID), Text: clamp(text), ParseMode: telego.ModeHTML,
		LinkPreviewOptions: &telego.LinkPreviewOptions{IsDisabled: true}, ReplyMarkup: markup, DisableNotification: !sound}
	if replyTo != 0 {
		p.ReplyParameters = &telego.ReplyParameters{MessageID: replyTo, AllowSendingWithoutReply: true}
	}
	m, err := b.API.SendMessage(ctx, p)
	if err != nil {
		b.fail("send", err)
		return 0, err
	}
	return m.MessageID, nil
}

// send is for cards, commands, drafts and prompts, which should not produce a
// second audible notification. Task event notifications opt into sound below.
func (b *Bot) send(ctx context.Context, to target, text string, markup telego.ReplyMarkup, replyTo int) (int, error) {
	return b.sendMode(ctx, to, text, markup, replyTo, false)
}

func (b *Bot) sendTask(ctx context.Context, t *store.Task, kind, text string, kb *telego.InlineKeyboardMarkup, replyCard bool) (int, error) {
	to, tt := b.targetFor(ctx, t)
	reply := 0
	if replyCard {
		reply = int(tt.MessageID)
	}
	sound := kind == "question" || kind == "pr" || kind == "failed" || kind == "ci_failed" || kind == "limit"
	id, err := b.sendMode(ctx, to, text, kb, reply, sound)
	if err == nil {
		b.fail("record_message", b.Store.AddTelegramTaskMessage(ctx, store.TelegramMessage{
			ChatID: to.chatID, MessageID: int64(id), TaskID: t.ID, Kind: kind, HasButtons: kb != nil,
		}))
	}
	return id, err
}

func (b *Bot) edit(ctx context.Context, chatID int64, msgID int, text string, kb *telego.InlineKeyboardMarkup) {
	_, err := b.API.EditMessageText(ctx, &telego.EditMessageTextParams{ChatID: tu.ID(chatID), MessageID: msgID, Text: clamp(text),
		ParseMode: telego.ModeHTML, LinkPreviewOptions: &telego.LinkPreviewOptions{IsDisabled: true}, ReplyMarkup: kb})
	if err != nil && !strings.Contains(err.Error(), "message is not modified") {
		b.fail("edit", err)
	}
}

func (b *Bot) react(ctx context.Context, m *telego.Message, emoji string) {
	err := b.API.SetMessageReaction(ctx, &telego.SetMessageReactionParams{ChatID: tu.ID(m.Chat.ID), MessageID: m.MessageID,
		Reaction: []telego.ReactionType{&telego.ReactionTypeEmoji{Type: telego.ReactionEmoji, Emoji: emoji}}})
	b.fail("reaction", err)
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

func (b *Bot) callback(ctx context.Context, c store.TelegramCallback) string {
	if id, err := b.Store.FindTelegramCallback(ctx, c); err == nil {
		return "c:" + id
	}
	buf := make([]byte, 7)
	if _, err := rand.Read(buf); err != nil {
		b.fail("callback_random", err)
		return "x"
	}
	c.ID = base64.RawURLEncoding.EncodeToString(buf)
	ttl := 30 * 24 * time.Hour
	if strings.HasPrefix(c.Action, "draft_") || c.Action == "answer" || c.Action == "own_answer" {
		ttl = 24 * time.Hour
	}
	if err := b.Store.AddTelegramCallback(ctx, c, time.Now().Add(ttl)); err != nil {
		b.fail("callback_store", err)
		return "x"
	}
	return "c:" + c.ID
}

func (b *Bot) taskButtons(ctx context.Context, t *store.Task, private bool) *telego.InlineKeyboardMarkup {
	var buttons []telego.InlineKeyboardButton
	open, hasOpen := b.appButton("📱 Open", fmt.Sprintf("/tasks/%d", t.ID), private)
	switch t.Status {
	case tasks.PROpen:
		if t.PRURL != nil {
			buttons = append(buttons, tu.InlineKeyboardButton(fmt.Sprintf("🔗 PR #%d", deref(t.PRNumber))).WithURL(*t.PRURL))
		}
		if hasOpen {
			buttons = append(buttons, open)
		}
	case tasks.Failed, tasks.Cancelled:
		buttons = append(buttons, tu.InlineKeyboardButton("🔁 Retry").WithCallbackData(b.callback(ctx, store.TelegramCallback{Action: "retry", TaskID: t.ID})))
		if hasOpen {
			buttons = append(buttons, open)
		}
	case tasks.Queued, tasks.Preparing, tasks.Running, tasks.WaitingInput, tasks.Pushing:
		if hasOpen {
			buttons = append(buttons, open)
		}
		buttons = append(buttons, tu.InlineKeyboardButton("⏹ Stop").WithCallbackData(b.callback(ctx, store.TelegramCallback{Action: "cancel", TaskID: t.ID})))
	default:
		if hasOpen {
			buttons = append(buttons, open)
		}
	}
	if len(buttons) == 0 {
		return nil
	}
	var rows [][]telego.InlineKeyboardButton
	for len(buttons) > 0 {
		n := min(2, len(buttons))
		rows = append(rows, tu.InlineKeyboardRow(buttons[:n]...))
		buttons = buttons[n:]
	}
	return tu.InlineKeyboard(rows...)
}

func (b *Bot) card(ctx context.Context, t *store.Task) string {
	c := cardData{Task: t, Now: time.Now()}
	if r, err := b.Store.GetRepository(ctx, t.RepositoryID); err == nil {
		c.Repo = r.FullName
	}
	if r, err := b.Store.LatestRun(ctx, t.ID); err == nil {
		c.Run = r
	}
	return renderCard(c)
}

func (b *Bot) ensureStatus(ctx context.Context, t *store.Task) (target, int) {
	b.createMu.RLock()
	defer b.createMu.RUnlock()
	b.statusMu.Lock()
	defer b.statusMu.Unlock()
	to, tt := b.targetFor(ctx, t)
	if tt.MessageID != 0 {
		return to, int(tt.MessageID)
	}
	kb := b.taskButtons(ctx, t, to.threadID == 0 && to.chatID == b.OwnerID)
	id, err := b.send(ctx, to, b.card(ctx, t), kb, 0)
	if err != nil {
		return to, 0
	}
	_ = b.Store.SetTaskTelegram(ctx, t.ID, store.TelegramTarget{ChatID: to.chatID, ThreadID: to.threadID, MessageID: int64(id)})
	_ = b.Store.AddTelegramTaskMessage(ctx, store.TelegramMessage{ChatID: to.chatID, MessageID: int64(id), TaskID: t.ID, Kind: "card", HasButtons: kb != nil})
	b.mu.Lock()
	b.edits[t.ID] = &editState{last: time.Now()}
	b.mu.Unlock()
	return to, id
}

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
	if wait < 0 {
		wait = 0
	}
	st.pending = true
	b.mu.Unlock()
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
	b.edit(ctx, to.chatID, msgID, b.card(ctx, t), b.taskButtons(ctx, t, to.threadID == 0 && to.chatID == b.OwnerID))
}

func (b *Bot) removeStaleButtons(ctx context.Context, taskID int64) {
	msgs, err := b.Store.TelegramMessagesWithButtons(ctx, taskID)
	if err != nil {
		b.fail("stale_buttons", err)
		return
	}
	for _, m := range msgs {
		_, err := b.API.EditMessageReplyMarkup(ctx, &telego.EditMessageReplyMarkupParams{ChatID: tu.ID(m.ChatID), MessageID: int(m.MessageID)})
		if err != nil && !strings.Contains(err.Error(), "message is not modified") {
			b.fail("stale_buttons", err)
			continue
		}
		_ = b.Store.ClearTelegramMessageButtons(ctx, m.ChatID, m.MessageID)
	}
}

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
	if len(t.StatusEvents) > 0 {
		b.removeStaleButtons(ctx, t.ID)
	}
	if _, id := b.ensureStatus(ctx, t); id != 0 {
		b.requestEdit(ctx, t.ID)
	}
	if t.Status == tasks.Failed && len(t.StatusEvents) > 0 {
		reason := deref(t.StatusReason)
		heading := "Task failed"
		kind := "failed"
		if h := humanizeError(reason); strings.Contains(strings.ToLower(h.Summary), "limit") {
			heading = "Limit hit"
			kind = "limit"
		}
		if !b.Store.HasTelegramTaskMessageKind(ctx, t.ID, kind) {
			_, _ = b.sendTask(ctx, t, kind, renderErrorNotification(t, heading, reason), b.taskButtons(ctx, t, b.targetPrivate(ctx, t)), true)
		}
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
	case "tool_use", "assistant_text", "usage":
		b.requestEdit(ctx, ev.TaskID)
	case "question":
		if ev.RunID != nil {
			b.sendQuestion(ctx, ev.TaskID, tasks.QuestionID(*ev.RunID, p.Question))
		}
	case "answer":
		b.refreshQuestion(ctx, ev.TaskID, p.Question)
	case "pr_opened":
		b.notify(ctx, ev.TaskID, "pr", func(t *store.Task) string {
			return fmt.Sprintf("✅ <b>#%d · PR #%d opened</b>\n%s", t.ID, p.Number, esc(cut(t.Title, 120)))
		})
	case "ci":
		if p.Status == "failure" {
			b.notify(ctx, ev.TaskID, "ci_failed", func(t *store.Task) string {
				name := esc(cut(p.Name, 100))
				if p.URL != "" {
					name = fmt.Sprintf("<a href=\"%s\">%s</a>", esc(p.URL), name)
				}
				return fmt.Sprintf("❌ <b>#%d · CI failed</b>\n%s", t.ID, name)
			})
		}
	case "error":
		if p.Code == "limit_exceeded" || strings.Contains(strings.ToLower(p.Code), "limit") {
			t, err := b.Store.GetTask(ctx, ev.TaskID)
			if err == nil && !b.Store.HasTelegramTaskMessageKind(ctx, t.ID, "limit") {
				_, _ = b.sendTask(ctx, t, "limit", renderErrorNotification(t, "Limit hit", p.Message), b.taskButtons(ctx, t, b.targetPrivate(ctx, t)), true)
			}
		}
	}
}

func (b *Bot) targetPrivate(ctx context.Context, t *store.Task) bool {
	to, _ := b.targetFor(ctx, t)
	return to.threadID == 0 && to.chatID == b.OwnerID
}

func (b *Bot) notify(ctx context.Context, taskID int64, kind string, text func(*store.Task) string) {
	t, err := b.Store.GetTask(ctx, taskID)
	if err != nil {
		return
	}
	_, _ = b.sendTask(ctx, t, kind, text(t), b.taskButtons(ctx, t, b.targetPrivate(ctx, t)), true)
}

func (b *Bot) questionKeyboard(ctx context.Context, q *store.Question) *telego.InlineKeyboardMarkup {
	if q.Answer != nil {
		return nil
	}
	var buttons []telego.InlineKeyboardButton
	for i, option := range q.Options {
		buttons = append(buttons, tu.InlineKeyboardButton(cut(option, 40)).WithCallbackData(b.callback(ctx, store.TelegramCallback{
			Action: "answer", TaskID: q.TaskID, QuestionID: q.ID, Value: fmt.Sprint(i),
		})))
	}
	buttons = append(buttons, tu.InlineKeyboardButton("✍️ Own answer").WithCallbackData(b.callback(ctx, store.TelegramCallback{
		Action: "own_answer", TaskID: q.TaskID, QuestionID: q.ID,
	})))
	var rows [][]telego.InlineKeyboardButton
	for len(buttons) > 0 {
		n := min(2, len(buttons))
		rows = append(rows, tu.InlineKeyboardRow(buttons[:n]...))
		buttons = buttons[n:]
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
	kb := b.questionKeyboard(ctx, q)
	id, err := b.sendTask(ctx, t, "question", renderQuestion(t, q), kb, true)
	if err == nil {
		_ = b.Store.SetQuestionTelegramMessage(ctx, q.ID, int64(id))
	}
}

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
	_ = b.Store.ClearTelegramMessageButtons(ctx, to.chatID, *q.TelegramMessageID)
}

func (b *Bot) HandleUpdate(ctx context.Context, u telego.Update) {
	switch {
	case u.Message != nil:
		if u.Message.From == nil || u.Message.From.ID != b.OwnerID {
			slog.Info("telegram: ignored message from non-owner", "from", userID(u.Message.From), "chat", u.Message.Chat.ID)
			return
		}
		b.onMessage(ctx, u.Message)
	case u.CallbackQuery != nil:
		if u.CallbackQuery.From.ID != b.OwnerID {
			slog.Info("telegram: ignored callback from non-owner", "from", u.CallbackQuery.From.ID)
			return
		}
		b.onCallback(ctx, u.CallbackQuery)
	}
}

func userID(u *telego.User) int64 {
	if u == nil {
		return 0
	}
	return u.ID
}

func (b *Bot) reply(ctx context.Context, m *telego.Message, text string, markup telego.ReplyMarkup) (int, error) {
	return b.send(ctx, target{m.Chat.ID, int64(m.MessageThreadID)}, text, markup, m.MessageID)
}

func (b *Bot) onCallback(ctx context.Context, cq *telego.CallbackQuery) {
	answer := func(text string) {
		b.fail("answer_callback", b.API.AnswerCallbackQuery(ctx, &telego.AnswerCallbackQueryParams{CallbackQueryID: cq.ID, Text: text}))
	}
	if !strings.HasPrefix(cq.Data, "c:") {
		answer("This action has expired")
		return
	}
	c, err := b.Store.TelegramCallback(ctx, strings.TrimPrefix(cq.Data, "c:"))
	if err != nil {
		answer("This action has expired")
		return
	}
	switch c.Action {
	case "answer":
		q, err := b.Store.GetQuestion(ctx, c.QuestionID)
		var i int
		_, _ = fmt.Sscan(c.Value, &i)
		if err != nil || i < 0 || i >= len(q.Options) {
			answer("Question not found")
			return
		}
		if _, err = b.Tasks.Answer(ctx, q.TaskID, q.ID, q.Options[i], "telegram"); err != nil {
			answer(errText(err))
			return
		}
		answer("Answered: " + cut(q.Options[i], 150))
	case "own_answer":
		m := cq.Message
		if m == nil {
			answer("Open the task chat to answer")
			return
		}
		threadID := int64(0)
		if mm := m.Message(); mm != nil {
			threadID = int64(mm.MessageThreadID)
		}
		id, err := b.send(ctx, target{m.GetChat().ID, threadID}, "✍️ Write your answer", &telego.ForceReply{ForceReply: true, InputFieldPlaceholder: "Your answer"}, 0)
		if err == nil {
			_ = b.Store.AddTelegramReplyPrompt(ctx, m.GetChat().ID, int64(id), store.TelegramReplyPrompt{Kind: "question", TaskID: c.TaskID, QuestionID: c.QuestionID})
			_ = b.Store.AddTelegramTaskMessage(ctx, store.TelegramMessage{ChatID: m.GetChat().ID, MessageID: int64(id), TaskID: c.TaskID, Kind: "answer_prompt"})
		}
		answer("Write your answer below")
	case "cancel":
		_, err := b.Tasks.Cancel(ctx, c.TaskID)
		if err != nil {
			answer(errText(err))
			return
		}
		answer("Stopped")
	case "retry":
		_, err := b.Tasks.Retry(ctx, c.TaskID)
		if err != nil {
			answer(errText(err))
			return
		}
		answer("Retry started")
	case "draft_create", "draft_cancel", "draft_project", "draft_repo", "draft_edit":
		b.handleDraftCallback(ctx, cq, c, answer)
	default:
		answer("Unknown action")
	}
}

func errText(err error) string {
	switch {
	case errors.Is(err, store.ErrConflict):
		if err.Error() == store.ErrConflict.Error() {
			return "Already answered or not allowed now"
		}
		return cut(err.Error(), 190)
	case errors.Is(err, store.ErrNotFound):
		return "Not found"
	default:
		return cut(err.Error(), 190)
	}
}
