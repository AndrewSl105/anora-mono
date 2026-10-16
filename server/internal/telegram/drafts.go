package telegram

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"

	"github.com/andrewsl105/anora/server/internal/store"
	"github.com/andrewsl105/anora/server/internal/tasks"
)

func callbackMessage(cq *telego.CallbackQuery) *telego.Message {
	if cq.Message == nil || !cq.Message.IsAccessible() {
		return nil
	}
	return cq.Message.Message()
}

func (b *Bot) handleDraftCallback(ctx context.Context, cq *telego.CallbackQuery, c store.TelegramCallback, answer func(string)) {
	d, err := b.Store.TelegramDraft(ctx, c.DraftID)
	if err != nil {
		answer("This draft has expired")
		return
	}
	switch c.Action {
	case "draft_cancel":
		if d.CardMessageID != 0 {
			b.edit(ctx, d.ChatID, int(d.CardMessageID), "✖ Draft cancelled", nil)
		}
		_ = b.Store.DeleteTelegramDraft(ctx, d.ID)
		answer("Cancelled")
	case "draft_edit":
		id, err := b.send(ctx, target{d.ChatID, d.ThreadID}, "✏️ Send the task text", &telego.ForceReply{ForceReply: true, InputFieldPlaceholder: "Task text"}, 0)
		if err == nil {
			err = b.Store.AddTelegramReplyPrompt(ctx, d.ChatID, int64(id), store.TelegramReplyPrompt{Kind: "draft_edit", DraftID: d.ID})
		}
		if err != nil {
			answer("Could not open the editor")
			return
		}
		answer("Send the new text")
	case "draft_project":
		projects, err := b.Store.ListProjects(ctx, false, "", 50)
		if err != nil {
			answer("Could not load projects")
			return
		}
		var buttons []telego.InlineKeyboardButton
		for _, p := range projects {
			buttons = append(buttons, tu.InlineKeyboardButton("📁 "+cut(p.Name, 30)).WithCallbackData(b.callback(ctx, store.TelegramCallback{Action: "draft_repo", DraftID: d.ID, Value: "p:" + p.ID})))
		}
		b.edit(ctx, d.ChatID, int(d.CardMessageID), b.renderDraft(ctx, d), twoColumn(buttons))
		answer("Choose a project")
	case "draft_repo":
		if strings.HasPrefix(c.Value, "p:") {
			projectID := strings.TrimPrefix(c.Value, "p:")
			p, err := b.Store.GetProject(ctx, projectID)
			if err != nil {
				answer("Project not found")
				return
			}
			if len(p.Repositories) == 1 {
				if err := b.Store.SetTelegramDraftProject(ctx, d.ID, p.ID, p.Repositories[0].ID); err != nil {
					answer("Could not select project")
					return
				}
				_ = b.refreshDraft(ctx, d.ID)
				answer("Project selected")
				return
			}
			var buttons []telego.InlineKeyboardButton
			for _, r := range p.Repositories {
				buttons = append(buttons, tu.InlineKeyboardButton("📦 "+cut(r.FullName, 35)).WithCallbackData(b.callback(ctx, store.TelegramCallback{Action: "draft_repo", DraftID: d.ID, Value: fmt.Sprintf("r:%s:%d", p.ID, r.ID)})))
			}
			b.edit(ctx, d.ChatID, int(d.CardMessageID), b.renderDraft(ctx, d), twoColumn(buttons))
			answer("Choose a repository")
			return
		}
		parts := strings.Split(c.Value, ":")
		if len(parts) != 3 || parts[0] != "r" {
			answer("Invalid repository")
			return
		}
		repoID, _ := strconv.ParseInt(parts[2], 10, 64)
		if err := b.Store.SetTelegramDraftProject(ctx, d.ID, parts[1], repoID); err != nil {
			answer("Could not select repository")
			return
		}
		_ = b.refreshDraft(ctx, d.ID)
		answer("Repository selected")
	case "draft_create":
		if d.ProjectID == "" {
			answer("Choose a project first")
			return
		}
		prompt := strings.TrimSpace(d.Text)
		if prompt == "" {
			prompt = "Review the attached image(s)."
		}
		images, err := b.downloadDraftImages(ctx, d.Media)
		if err != nil {
			answer(cut(errText(err), 190))
			return
		}
		b.createMu.Lock()
		v, err := b.Tasks.Create(ctx, tasks.CreateInput{ProjectID: d.ProjectID, RepositoryID: d.RepositoryID,
			Title: tasks.TitleFromPrompt(prompt), Prompt: prompt, Source: "telegram", Images: images})
		if err == nil {
			err = b.Store.SetTaskTelegram(ctx, v.ID, store.TelegramTarget{ChatID: d.ChatID, ThreadID: d.ThreadID})
		}
		b.createMu.Unlock()
		if err != nil {
			answer(cut(errText(err), 190))
			return
		}
		_ = b.Store.SetTelegramChatProject(ctx, d.ChatID, d.ThreadID, d.ProjectID, v.RepositoryID)
		b.ensureStatus(ctx, v.Task)
		if d.CardMessageID != 0 {
			b.edit(ctx, d.ChatID, int(d.CardMessageID), fmt.Sprintf("✅ Created <b>#%d · %s</b>", v.ID, esc(cut(v.Title, 100))), nil)
		}
		_ = b.Store.DeleteTelegramDraft(ctx, d.ID)
		answer("Task created")
	}
}

func twoColumn(buttons []telego.InlineKeyboardButton) *telego.InlineKeyboardMarkup {
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
