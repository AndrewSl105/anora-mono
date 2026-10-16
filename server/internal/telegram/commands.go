package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"

	"github.com/andrewsl105/anora/server/internal/store"
	"github.com/andrewsl105/anora/server/internal/tasks"
)

var commandList = []telego.BotCommand{
	{Command: "new", Description: "Create a task"},
	{Command: "tasks", Description: "Show active tasks"},
	{Command: "task", Description: "Show task details"},
	{Command: "msg", Description: "Message a task"},
	{Command: "cancel", Description: "Stop a task"},
	{Command: "retry", Description: "Retry a failed task"},
	{Command: "projects", Description: "List projects"},
	{Command: "bind", Description: "Bind this topic"},
	{Command: "app", Description: "Open Anora"},
}

const help = `<b>Anora</b>
Send text, photos, an image file, or a voice message to draft a task.
Reply to any task card or notification to talk to that task.

/new &lt;project&gt; &lt;prompt&gt; — create immediately
/tasks — active tasks
/task &lt;id&gt; — details
/msg &lt;id&gt; &lt;text&gt; — message a task
/cancel &lt;id&gt; · /retry &lt;id&gt;
/projects · /bind &lt;project&gt; · /app`

func ParseCommand(text string) (cmd, args string, ok bool) {
	if !strings.HasPrefix(text, "/") {
		return "", "", false
	}
	head, rest, _ := strings.Cut(strings.TrimSpace(text[1:]), " ")
	if i := strings.Index(head, "\n"); i >= 0 {
		rest = head[i+1:] + " " + rest
		head = head[:i]
	}
	head, _, _ = strings.Cut(head, "@")
	return strings.ToLower(head), strings.TrimSpace(rest), head != ""
}

func splitID(args string) (int64, string, bool) {
	first, rest, _ := strings.Cut(strings.TrimSpace(args), " ")
	id, err := strconv.ParseInt(strings.TrimPrefix(first, "#"), 10, 64)
	return id, strings.TrimSpace(rest), err == nil && id > 0
}

func (b *Bot) topicProject(ctx context.Context, m *telego.Message) *store.Project {
	if m.MessageThreadID == 0 {
		return nil
	}
	p, err := b.Store.ProjectByTopic(ctx, m.Chat.ID, int64(m.MessageThreadID))
	if err != nil {
		return nil
	}
	return p
}

func (b *Bot) onMessage(ctx context.Context, m *telego.Message) {
	text := strings.TrimSpace(m.Text)
	if text == "" {
		text = strings.TrimSpace(m.Caption)
	}
	cmd, args, isCmd := ParseCommand(text)
	if isCmd {
		b.react(ctx, m, "👀")
		if err := b.handleCommand(ctx, m, cmd, args); err != nil {
			b.rejectMessage(ctx, m, err)
			return
		}
		b.react(ctx, m, "✅")
		return
	}
	if !supportedMessage(m) {
		return
	}
	b.react(ctx, m, "👀")
	if r := m.ReplyToMessage; r != nil {
		if p, err := b.Store.TelegramReplyPrompt(ctx, m.Chat.ID, int64(r.MessageID)); err == nil {
			if err := b.handlePromptReply(ctx, m, p); err != nil {
				b.rejectMessage(ctx, m, err)
				return
			}
			b.react(ctx, m, "✅")
			return
		}
		if taskID, err := b.Store.TelegramTaskByMessage(ctx, m.Chat.ID, int64(r.MessageID)); err == nil {
			if err := b.routeTaskReply(ctx, m, taskID); err != nil {
				b.rejectMessage(ctx, m, err)
				return
			}
			b.react(ctx, m, "✅")
			return
		}
	}
	if err := b.mergeDraft(ctx, m); err != nil {
		b.rejectMessage(ctx, m, err)
		return
	}
	b.react(ctx, m, "✅")
}

func supportedMessage(m *telego.Message) bool {
	return strings.TrimSpace(m.Text) != "" || strings.TrimSpace(m.Caption) != "" || len(m.Photo) > 0 ||
		m.Voice != nil || m.Audio != nil || m.Document != nil && strings.HasPrefix(m.Document.MimeType, "image/")
}

func (b *Bot) rejectMessage(ctx context.Context, m *telego.Message, err error) {
	b.react(ctx, m, "❌")
	text := errText(err)
	if strings.HasPrefix(text, "Voice messages are not enabled") {
		_, _ = b.reply(ctx, m, esc(text), nil)
		return
	}
	_, _ = b.reply(ctx, m, "❌ "+esc(text), nil)
}

func (b *Bot) handleCommand(ctx context.Context, m *telego.Message, cmd, args string) error {
	switch cmd {
	case "start", "help":
		_, err := b.reply(ctx, m, help, nil)
		return err
	case "new":
		if p := b.topicProject(ctx, m); p != nil {
			return b.newTask(ctx, m, p.ID, 0, args)
		}
		project, prompt, _ := strings.Cut(args, " ")
		if project == "" || strings.TrimSpace(prompt) == "" {
			_, err := b.reply(ctx, m, "Usage: /new &lt;project&gt; &lt;prompt&gt;", nil)
			return err
		}
		return b.newTask(ctx, m, project, 0, prompt)
	case "tasks":
		b.listTasks(ctx, m)
		return nil
	case "task":
		id, _, ok := splitID(args)
		if !ok {
			_, err := b.reply(ctx, m, "Usage: /task &lt;id&gt;", nil)
			return err
		}
		b.showTask(ctx, target{m.Chat.ID, int64(m.MessageThreadID)}, id)
		return nil
	case "msg":
		id, message, ok := splitID(args)
		if !ok || message == "" {
			_, err := b.reply(ctx, m, "Usage: /msg &lt;id&gt; &lt;text&gt;", nil)
			return err
		}
		_, err := b.Tasks.Message(ctx, id, message, nil)
		return err
	case "cancel", "retry":
		id, _, ok := splitID(args)
		if !ok {
			_, err := b.reply(ctx, m, "Usage: /"+cmd+" &lt;id&gt;", nil)
			return err
		}
		if cmd == "cancel" {
			_, err := b.Tasks.Cancel(ctx, id)
			return err
		}
		_, err := b.Tasks.Retry(ctx, id)
		return err
	case "projects":
		b.listProjects(ctx, m)
		return nil
	case "bind":
		if m.MessageThreadID == 0 {
			_, err := b.reply(ctx, m, "Use /bind inside a forum topic.", nil)
			return err
		}
		if err := b.Store.BindProjectTopic(ctx, strings.TrimSpace(args), m.Chat.ID, int64(m.MessageThreadID)); err != nil {
			return err
		}
		_, err := b.reply(ctx, m, fmt.Sprintf("This topic is now <b>%s</b>.", esc(args)), nil)
		return err
	case "app":
		btn, ok := b.appButton("📱 Open Anora", "/", m.Chat.Type == "private")
		if !ok {
			_, err := b.reply(ctx, m, "WEB_APP_URL is not configured.", nil)
			return err
		}
		_, err := b.reply(ctx, m, "Anora", tu.InlineKeyboard(tu.InlineKeyboardRow(btn)))
		return err
	default:
		_, err := b.reply(ctx, m, help, nil)
		return err
	}
}

func (b *Bot) handlePromptReply(ctx context.Context, m *telego.Message, p store.TelegramReplyPrompt) error {
	text, _, err := b.incomingContent(ctx, m)
	if err != nil {
		return err
	}
	switch p.Kind {
	case "draft_edit":
		if strings.TrimSpace(text) == "" {
			return fmt.Errorf("text is required")
		}
		if err := b.Store.SetTelegramDraftText(ctx, p.DraftID, text); err != nil {
			return err
		}
		return b.refreshDraft(ctx, p.DraftID)
	case "question":
		if strings.TrimSpace(text) == "" {
			return fmt.Errorf("an answer is required")
		}
		_, err := b.Tasks.Answer(ctx, p.TaskID, p.QuestionID, text, "telegram")
		return err
	default:
		return fmt.Errorf("this reply prompt has expired")
	}
}

func (b *Bot) routeTaskReply(ctx context.Context, m *telego.Message, taskID int64) error {
	text, images, err := b.incomingContent(ctx, m)
	if err != nil {
		return err
	}
	if m.Voice != nil || m.Audio != nil {
		t, e := b.Store.GetTask(ctx, taskID)
		if e == nil {
			_, _ = b.sendTask(ctx, t, "transcript", "🎙 <b>Transcript</b>\n"+esc(cut(text, 1500)), nil, true)
		}
	}
	v, err := b.Tasks.Get(ctx, taskID)
	if err != nil {
		return err
	}
	if v.OpenQuestion != nil {
		if text == "" && len(images) > 0 {
			text = "See the attached image(s)."
		}
		_, err = b.Tasks.Answer(ctx, taskID, v.OpenQuestion.ID, text, "telegram")
		if err != nil || len(images) == 0 {
			return err
		}
		_, err = b.Tasks.Message(ctx, taskID, "Images attached to my answer.", images)
		return err
	}
	if text == "" && len(images) > 0 {
		text = "Review the attached image(s)."
	}
	_, err = b.Tasks.Message(ctx, taskID, text, images)
	return err
}

func (b *Bot) newTask(ctx context.Context, m *telego.Message, projectID string, repositoryID int64, prompt string) error {
	b.createMu.Lock()
	v, err := b.Tasks.Create(ctx, tasks.CreateInput{ProjectID: projectID, RepositoryID: repositoryID, Prompt: prompt, Source: "telegram"})
	if err == nil {
		err = b.Store.SetTaskTelegram(ctx, v.ID, store.TelegramTarget{ChatID: m.Chat.ID, ThreadID: int64(m.MessageThreadID)})
	}
	b.createMu.Unlock()
	if err != nil {
		return err
	}
	b.ensureStatus(ctx, v.Task)
	return b.Store.SetTelegramChatProject(ctx, m.Chat.ID, int64(m.MessageThreadID), projectID, v.RepositoryID)
}

func (b *Bot) listTasks(ctx context.Context, m *telego.Message) {
	active := []string{tasks.Queued, tasks.Preparing, tasks.Running, tasks.WaitingInput, tasks.Paused, tasks.Pushing, tasks.PROpen}
	f := store.TaskFilter{Statuses: active, Limit: 20}
	if p := b.topicProject(ctx, m); p != nil {
		f.ProjectID = p.ID
	}
	list, err := b.Tasks.List(ctx, f)
	if err != nil {
		_, _ = b.reply(ctx, m, "❌ "+esc(errText(err)), nil)
		return
	}
	if len(list) == 0 {
		_, _ = b.reply(ctx, m, "No active tasks.", nil)
		return
	}
	var sb strings.Builder
	var rows [][]telego.InlineKeyboardButton
	sb.WriteString("<b>Active tasks</b>\n")
	for _, t := range list {
		fmt.Fprintf(&sb, "\n%s #%d · %s", statusIcon[t.Status], t.ID, esc(cut(t.Title, 60)))
		if btn, ok := b.appButton(fmt.Sprintf("📱 #%d", t.ID), fmt.Sprintf("/tasks/%d", t.ID), m.Chat.Type == "private"); ok {
			rows = append(rows, tu.InlineKeyboardRow(btn))
		}
	}
	_, _ = b.reply(ctx, m, sb.String(), tu.InlineKeyboard(rows...))
}

func (b *Bot) showTask(ctx context.Context, to target, id int64) {
	t, err := b.Store.GetTask(ctx, id)
	if err != nil {
		_, _ = b.send(ctx, to, "Task not found.", nil, 0)
		return
	}
	evs, _ := b.Store.LastEvents(ctx, id, []string{"tool_use", "status", "pr_opened", "ci", "question", "answer"}, 5)
	var recent []string
	for _, ev := range evs {
		var p payload
		_ = json.Unmarshal(ev.Payload, &p)
		switch ev.Type {
		case "tool_use":
			recent = append(recent, p.Summary)
		case "status":
			recent = append(recent, "status → "+p.Status)
		case "pr_opened":
			recent = append(recent, fmt.Sprintf("PR #%d opened", p.Number))
		case "ci":
			recent = append(recent, "CI "+p.Status)
		case "question":
			recent = append(recent, "asked: "+p.Text)
		case "answer":
			recent = append(recent, "answered: "+p.Text)
		}
	}
	kb := b.taskButtons(ctx, t, to.chatID == b.OwnerID)
	messageID, err := b.send(ctx, to, renderTaskDetails(t, t.Prompt, recent), kb, 0)
	if err == nil {
		_ = b.Store.AddTelegramTaskMessage(ctx, store.TelegramMessage{ChatID: to.chatID, MessageID: int64(messageID), TaskID: t.ID, Kind: "details", HasButtons: kb != nil})
	}
}

func (b *Bot) listProjects(ctx context.Context, m *telego.Message) {
	list, err := b.Store.ListProjects(ctx, false, "", 50)
	if err != nil {
		_, _ = b.reply(ctx, m, "❌ "+esc(errText(err)), nil)
		return
	}
	if len(list) == 0 {
		_, _ = b.reply(ctx, m, "No projects yet. Create one in the app.", nil)
		return
	}
	var sb strings.Builder
	sb.WriteString("<b>Projects</b>\n")
	for _, p := range list {
		var repos []string
		for _, r := range p.Repositories {
			repos = append(repos, r.FullName)
		}
		fmt.Fprintf(&sb, "\n<b>%s</b> — %s", esc(p.ID), esc(strings.Join(repos, ", ")))
	}
	_, _ = b.reply(ctx, m, sb.String(), nil)
}

func (b *Bot) mergeDraft(ctx context.Context, m *telego.Message) error {
	text, media, err := b.incomingDraftContent(ctx, m)
	if err != nil {
		return err
	}
	project, repo := "", int64(0)
	if p := b.topicProject(ctx, m); p != nil {
		project = p.ID
		repo = defaultRepo(p)
	} else if p, r, err := b.Store.TelegramChatProject(ctx, m.Chat.ID, int64(m.MessageThreadID)); err == nil {
		project, repo = p, r
	}
	d, err := b.Store.MergeTelegramDraft(ctx, m.Chat.ID, int64(m.MessageThreadID), int64(m.MessageID), project, repo, text, media, time.Now().Add(-b.MergeWindow))
	if err != nil {
		return err
	}
	if d.CardMessageID == 0 {
		kb := b.draftButtons(ctx, d)
		id, err := b.send(ctx, target{d.ChatID, d.ThreadID}, b.renderDraft(ctx, d), kb, m.MessageID)
		if err != nil {
			return err
		}
		return b.Store.SetTelegramDraftCard(ctx, d.ID, int64(id))
	}
	return b.refreshDraft(ctx, d.ID)
}

func defaultRepo(p *store.Project) int64 {
	for _, r := range p.Repositories {
		if r.IsDefault {
			return r.ID
		}
	}
	return 0
}

func (b *Bot) renderDraft(ctx context.Context, d *store.TelegramDraft) string {
	project, repo := d.ProjectID, "Choose a project"
	if project != "" {
		repo = "choose repository"
		if d.RepositoryID != 0 {
			if r, err := b.Store.GetRepository(ctx, d.RepositoryID); err == nil {
				repo = r.FullName
			}
		}
	}
	preview := cut(d.Text, 200)
	if preview == "" {
		preview = "Review the attached image(s)."
	}
	var bld strings.Builder
	bld.WriteString("📝 <b>Create a task?</b>\n")
	if project == "" {
		bld.WriteString("No project selected\n")
	} else {
		fmt.Fprintf(&bld, "%s / %s\n", esc(project), esc(repo))
	}
	bld.WriteString("\n" + esc(preview))
	if len(d.Media) > 0 {
		fmt.Fprintf(&bld, "\n+%d images", len(d.Media))
	}
	return bld.String()
}

func (b *Bot) draftButtons(ctx context.Context, d *store.TelegramDraft) *telego.InlineKeyboardMarkup {
	return tu.InlineKeyboard(
		tu.InlineKeyboardRow(
			tu.InlineKeyboardButton("✅ Create").WithCallbackData(b.callback(ctx, store.TelegramCallback{Action: "draft_create", DraftID: d.ID})),
			tu.InlineKeyboardButton("📁 Project").WithCallbackData(b.callback(ctx, store.TelegramCallback{Action: "draft_project", DraftID: d.ID})),
		),
		tu.InlineKeyboardRow(
			tu.InlineKeyboardButton("✏️ Edit text").WithCallbackData(b.callback(ctx, store.TelegramCallback{Action: "draft_edit", DraftID: d.ID})),
			tu.InlineKeyboardButton("✖ Cancel").WithCallbackData(b.callback(ctx, store.TelegramCallback{Action: "draft_cancel", DraftID: d.ID})),
		),
	)
}

func (b *Bot) refreshDraft(ctx context.Context, id int64) error {
	d, err := b.Store.TelegramDraft(ctx, id)
	if err != nil {
		return err
	}
	if d.CardMessageID != 0 {
		b.edit(ctx, d.ChatID, int(d.CardMessageID), b.renderDraft(ctx, d), b.draftButtons(ctx, d))
	}
	return nil
}
