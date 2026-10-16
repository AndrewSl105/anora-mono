package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"

	"github.com/andrewsl105/anora/server/internal/store"
	"github.com/andrewsl105/anora/server/internal/tasks"
)

var commandList = []telego.BotCommand{
	{Command: "new", Description: "New task: /new <project> <prompt>"},
	{Command: "tasks", Description: "Active tasks"},
	{Command: "task", Description: "Task details: /task <id>"},
	{Command: "msg", Description: "Follow-up for a task: /msg <id> <text>"},
	{Command: "cancel", Description: "Cancel: /cancel <id>"},
	{Command: "retry", Description: "Retry: /retry <id>"},
	{Command: "projects", Description: "List projects"},
	{Command: "bind", Description: "Link this topic: /bind <project>"},
	{Command: "app", Description: "Open the app"},
}

const help = `<b>Anora Runner</b>
/new &lt;project&gt; &lt;prompt&gt; — new task (in a bound topic: /new &lt;prompt&gt; or just write)
/tasks — active tasks
/task &lt;id&gt; — details
/msg &lt;id&gt; &lt;text&gt; — follow-up (queued while a run is active)
/cancel &lt;id&gt; · /retry &lt;id&gt;
/projects — projects
/bind &lt;project&gt; — link this forum topic to a project
/app — open the app
Reply to a question message to answer it.`

// ParseCommand splits "/cmd@bot args" into ("cmd", "args").
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

// splitID parses "<id> <rest>".
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
		return
	}
	// A reply to a question message answers it.
	if r := m.ReplyToMessage; r != nil {
		if q, err := b.Store.QuestionByTelegramMessage(ctx, int64(r.MessageID)); err == nil {
			if _, err := b.Tasks.Answer(ctx, q.TaskID, q.ID, text, "telegram"); err != nil {
				b.reply(ctx, m, "⚠️ "+esc(errText(err)), nil)
			}
			return
		}
	}
	cmd, args, isCmd := ParseCommand(text)
	if !isCmd {
		if p := b.topicProject(ctx, m); p != nil {
			b.newTask(ctx, m, p.ID, text)
			return
		}
		b.reply(ctx, m, help, nil)
		return
	}
	switch cmd {
	case "start", "help":
		b.reply(ctx, m, help, nil)
	case "new":
		if p := b.topicProject(ctx, m); p != nil {
			b.newTask(ctx, m, p.ID, args)
			return
		}
		project, prompt, _ := strings.Cut(args, " ")
		if project == "" || strings.TrimSpace(prompt) == "" {
			b.reply(ctx, m, "Usage: /new &lt;project&gt; &lt;prompt&gt;", nil)
			return
		}
		b.newTask(ctx, m, project, prompt)
	case "tasks":
		b.listTasks(ctx, m)
	case "task":
		id, _, ok := splitID(args)
		if !ok {
			b.reply(ctx, m, "Usage: /task &lt;id&gt;", nil)
			return
		}
		b.showTask(ctx, target{m.Chat.ID, int64(m.MessageThreadID)}, id)
	case "msg":
		id, text, ok := splitID(args)
		if !ok || text == "" {
			b.reply(ctx, m, "Usage: /msg &lt;id&gt; &lt;text&gt;", nil)
			return
		}
		v, err := b.Tasks.Message(ctx, id, text, nil)
		if err != nil {
			b.reply(ctx, m, "⚠️ "+esc(errText(err)), nil)
			return
		}
		if v.QueuedMessage != nil {
			b.reply(ctx, m, fmt.Sprintf("Queued for #%d: it starts as a new run when the current run ends.", id), nil)
		} else {
			b.reply(ctx, m, fmt.Sprintf("Started a new run for #%d.", id), nil)
		}
	case "cancel", "retry":
		id, _, ok := splitID(args)
		if !ok {
			b.reply(ctx, m, "Usage: /"+cmd+" &lt;id&gt;", nil)
			return
		}
		var err error
		if cmd == "cancel" {
			_, err = b.Tasks.Cancel(ctx, id)
		} else {
			_, err = b.Tasks.Retry(ctx, id)
		}
		if err != nil {
			b.reply(ctx, m, "⚠️ "+esc(errText(err)), nil)
		}
	case "projects":
		b.listProjects(ctx, m)
	case "bind":
		if m.MessageThreadID == 0 {
			b.reply(ctx, m, "Use /bind inside a forum topic.", nil)
			return
		}
		if err := b.Store.BindProjectTopic(ctx, strings.TrimSpace(args), m.Chat.ID, int64(m.MessageThreadID)); err != nil {
			b.reply(ctx, m, "⚠️ "+esc(errText(err)), nil)
			return
		}
		b.reply(ctx, m, fmt.Sprintf("This topic is now <b>%s</b>. Write a message here to start a task.", esc(args)), nil)
	case "app":
		btn, ok := b.appButton("Open Anora", "/", m.Chat.Type == "private")
		if !ok {
			b.reply(ctx, m, "WEB_APP_URL is not configured.", nil)
			return
		}
		b.reply(ctx, m, "Anora Runner", tu.InlineKeyboard(tu.InlineKeyboardRow(btn)))
	default:
		b.reply(ctx, m, help, nil)
	}
}

func (b *Bot) newTask(ctx context.Context, m *telego.Message, projectID, prompt string) {
	b.createMu.Lock()
	v, err := b.Tasks.Create(ctx, tasks.CreateInput{ProjectID: projectID, Prompt: prompt, Source: "telegram"})
	if err == nil {
		err = b.Store.SetTaskTelegram(ctx, v.ID, store.TelegramTarget{ChatID: m.Chat.ID, ThreadID: int64(m.MessageThreadID)})
	}
	b.createMu.Unlock()
	if err != nil {
		b.reply(ctx, m, "⚠️ "+esc(errText(err)), nil)
		return
	}
	b.ensureStatus(ctx, v.Task)
}

func (b *Bot) listTasks(ctx context.Context, m *telego.Message) {
	active := []string{tasks.Queued, tasks.Preparing, tasks.Running, tasks.WaitingInput, tasks.Paused, tasks.Pushing, tasks.PROpen}
	f := store.TaskFilter{Statuses: active, Limit: 20}
	if p := b.topicProject(ctx, m); p != nil {
		f.ProjectID = p.ID
	}
	list, err := b.Tasks.List(ctx, f)
	if err != nil {
		b.reply(ctx, m, "⚠️ "+esc(errText(err)), nil)
		return
	}
	if len(list) == 0 {
		b.reply(ctx, m, "No active tasks.", nil)
		return
	}
	var sb strings.Builder
	var rows [][]telego.InlineKeyboardButton
	sb.WriteString("<b>Active tasks</b>\n")
	for _, t := range list {
		fmt.Fprintf(&sb, "\n%s #%d %s · %s", statusIcon[t.Status], t.ID, esc(cut(t.Title, 60)), esc(t.ProjectID))
		rows = append(rows, tu.InlineKeyboardRow(
			tu.InlineKeyboardButton(fmt.Sprintf("#%d %s", t.ID, cut(t.Title, 30))).WithCallbackData(fmt.Sprintf("task|%d", t.ID)),
		))
	}
	b.reply(ctx, m, sb.String(), tu.InlineKeyboard(rows...))
}

func (b *Bot) showTask(ctx context.Context, to target, id int64) {
	t, err := b.Store.GetTask(ctx, id)
	if err != nil {
		_, _ = b.send(ctx, to, "Task not found.", nil)
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
	_, _ = b.send(ctx, to, renderTaskDetails(t, t.Prompt, recent), b.taskButtons(t, to.chatID == b.OwnerID))
}

func (b *Bot) listProjects(ctx context.Context, m *telego.Message) {
	list, err := b.Store.ListProjects(ctx, false, "", 50)
	if err != nil {
		b.reply(ctx, m, "⚠️ "+esc(errText(err)), nil)
		return
	}
	if len(list) == 0 {
		b.reply(ctx, m, "No projects yet. Create one in the app.", nil)
		return
	}
	var sb strings.Builder
	sb.WriteString("<b>Projects</b>\n")
	for _, p := range list {
		repos := make([]string, 0, len(p.Repositories))
		for _, r := range p.Repositories {
			repos = append(repos, r.FullName)
		}
		fmt.Fprintf(&sb, "\n<b>%s</b> — %s", esc(p.ID), esc(strings.Join(repos, ", ")))
	}
	b.reply(ctx, m, sb.String(), nil)
}
