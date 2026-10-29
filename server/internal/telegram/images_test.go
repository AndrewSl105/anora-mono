package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/andrewsl105/fleetpocket/server/internal/attachments"
	"github.com/andrewsl105/fleetpocket/server/internal/events"
	"github.com/andrewsl105/fleetpocket/server/internal/projects"
	"github.com/andrewsl105/fleetpocket/server/internal/store"
	"github.com/andrewsl105/fleetpocket/server/internal/tasks"
	"github.com/andrewsl105/fleetpocket/server/internal/testutil"
)

// imageEnv is a bot whose Telegram files are served by a test server: the
// fake API's GetFile returns the file id as the download URL.
type imageEnv struct {
	st    *store.Store
	bot   *Bot
	api   *fakeAPI
	files string // base URL; a file id is files + "/<name>"
	task  int64
	card  int // a bot message of the task, for replies
}

func newImageEnv(t *testing.T) *imageEnv {
	t.Helper()
	st := testutil.DB(t)
	ctx := context.Background()
	var img bytes.Buffer
	_ = png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 20, 10)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(img.Bytes()) }))
	t.Cleanup(srv.Close)

	hub := events.NewHub()
	svc := &tasks.Service{Store: st, Hub: hub, Files: &attachments.Files{Dir: t.TempDir()}}
	name := "Demo"
	_, _ = (&projects.Service{Store: st}).Create(ctx, projects.CreateInput{ID: "demo", ProjectFields: store.ProjectFields{Name: &name}})
	_, _ = (&projects.Service{Store: st}).AddRepository(ctx, "demo", projects.RepoInput{FullName: "o/r", InstallationID: 1})
	v, err := svc.Create(ctx, tasks.CreateInput{ProjectID: "demo", Prompt: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddTelegramTaskMessage(ctx, store.TelegramMessage{ChatID: owner, MessageID: 500, TaskID: v.ID, Kind: "card"}); err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{}
	b := &Bot{API: api, Store: st, Tasks: svc, Hub: hub, OwnerID: owner, AlbumWait: 50 * time.Millisecond}
	b.init()
	return &imageEnv{st: st, bot: b, api: api, files: srv.URL, task: v.ID, card: 500}
}

func (e *imageEnv) photo(id int, caption, group string, replyTo int) *telego.Message {
	m := &telego.Message{
		MessageID: id, From: &telego.User{ID: owner}, Chat: telego.Chat{ID: owner, Type: "private"},
		Caption: caption, MediaGroupID: group,
		Photo: []telego.PhotoSize{{FileID: e.files + "/small"}, {FileID: e.files + "/large"}},
	}
	if replyTo != 0 {
		m.ReplyToMessage = &telego.Message{MessageID: replyTo}
	}
	return m
}

// messageEvents returns the task's message payloads.
func (e *imageEnv) messageEvents(t *testing.T) []map[string]any {
	t.Helper()
	evs, err := e.st.ListEvents(context.Background(), e.task, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, ev := range evs {
		if ev.Type == "message" {
			var p map[string]any
			_ = json.Unmarshal(ev.Payload, &p)
			out = append(out, p)
		}
	}
	return out
}

func TestAlbumReplyBecomesOneMessage(t *testing.T) {
	e := newImageEnv(t)
	ctx := context.Background()
	// Telegram sends an album as separate messages; the caption is on one.
	e.bot.onMessage(ctx, e.photo(1, "", "album-1", e.card))
	e.bot.onMessage(ctx, e.photo(2, "Make it look like these", "album-1", e.card))
	e.bot.onMessage(ctx, e.photo(3, "", "album-1", e.card))

	eventually(t, "the album message", func() bool { return len(e.messageEvents(t)) > 0 })
	time.Sleep(150 * time.Millisecond) // nothing else may follow
	msgs := e.messageEvents(t)
	if len(msgs) != 1 {
		t.Fatalf("want one message for the album, got %v", msgs)
	}
	if msgs[0]["text"] != "Make it look like these" || len(msgs[0]["attachments"].([]any)) != 3 {
		t.Fatalf("album message: %v", msgs[0])
	}
}

func TestImageWithoutCaptionAsksForText(t *testing.T) {
	e := newImageEnv(t)
	ctx := context.Background()
	e.bot.onMessage(ctx, e.photo(1, "", "", e.card))

	sends, _, _ := e.api.snapshot()
	var prompt *sent
	for i := range sends {
		if strings.Contains(sends[i].text, "What should Claude do with the image(s)?") {
			prompt = &sends[i]
		}
	}
	if prompt == nil {
		t.Fatalf("no text prompt; sent %+v", sends)
	}
	if _, ok := prompt.markup.(*telego.ForceReply); !ok {
		t.Fatalf("prompt markup %T, want ForceReply", prompt.markup)
	}
	if taskID, err := e.st.TelegramTaskByMessage(ctx, owner, int64(prompt.msgID)); err != nil || taskID != e.task {
		t.Fatalf("image prompt is not registered as a task message: task=%d err=%v", taskID, err)
	}
	if len(e.messageEvents(t)) != 0 {
		t.Fatal("images without text must not reach the task yet")
	}

	reply := &telego.Message{MessageID: 2, From: &telego.User{ID: owner}, Chat: telego.Chat{ID: owner, Type: "private"},
		Text: "Fix the overlap shown here", ReplyToMessage: &telego.Message{MessageID: prompt.msgID}}
	e.bot.onMessage(ctx, reply)
	msgs := e.messageEvents(t)
	if len(msgs) != 1 || msgs[0]["text"] != "Fix the overlap shown here" || len(msgs[0]["attachments"].([]any)) != 1 {
		t.Fatalf("message after the text reply: %v", msgs)
	}
}

func TestAlbumAcknowledgesEveryPart(t *testing.T) {
	e := newImageEnv(t)
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		e.bot.onMessage(ctx, e.photo(i, map[bool]string{true: "Use these"}[i == 2], "album-react", e.card))
	}
	eventually(t, "album delivery reactions", func() bool {
		reactions := e.api.reactionSnapshot()
		seen, delivered := 0, 0
		for _, reaction := range reactions {
			if reaction == "👀" {
				seen++
			}
			if reaction == "✅" {
				delivered++
			}
		}
		return seen >= 3 && delivered >= 3
	})
}

func TestAlbumCollectionStillMergesWithRecentText(t *testing.T) {
	e := newImageEnv(t)
	// The window is measured from the draft's last write, which happens before
	// the card is sent: leave room for that work on a slow CI runner.
	e.bot.MergeWindow = 500 * time.Millisecond
	// The album is handled long after the window closed; it still merges
	// because the window counts from the album's first part.
	e.bot.AlbumWait = 800 * time.Millisecond
	ctx := context.Background()
	e.bot.onMessage(ctx, &telego.Message{MessageID: 10, From: &telego.User{ID: owner}, Chat: telego.Chat{ID: owner, Type: "private"}, Text: "Use the screenshots"})
	time.Sleep(40 * time.Millisecond)
	e.bot.onMessage(ctx, e.photo(11, "", "album-draft", 0))
	e.bot.onMessage(ctx, e.photo(12, "", "album-draft", 0))
	eventually(t, "merged text and album draft", func() bool {
		var count, images int
		if err := e.st.Pool.QueryRow(ctx, `SELECT count(*), coalesce(sum(jsonb_array_length(media)), 0) FROM telegram_drafts`).Scan(&count, &images); err != nil {
			return false
		}
		return count == 1 && images == 2
	})
}

func TestOnlyTaskNotificationsUseSound(t *testing.T) {
	e := newImageEnv(t)
	ctx := context.Background()
	task, err := e.st.GetTask(ctx, e.task)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.bot.send(ctx, target{owner, 0}, "draft", nil, 0); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"question", "pr", "failed", "ci_failed", "limit", "transcript"} {
		if _, err := e.bot.sendTask(ctx, task, kind, kind, nil, false); err != nil {
			t.Fatal(err)
		}
	}
	sends, _, _ := e.api.snapshot()
	wantSound := map[string]bool{"question": true, "pr": true, "failed": true, "ci_failed": true, "limit": true}
	for _, message := range sends {
		if message.text == "draft" && !message.silent {
			t.Fatal("draft message was audible")
		}
		if expected := wantSound[message.text]; message.text != "draft" && message.silent == expected {
			t.Errorf("kind %q silent=%v, want sound=%v", message.text, message.silent, expected)
		}
	}
}

func TestNewCommandCaptionAttachesPhoto(t *testing.T) {
	e := newImageEnv(t)
	ctx := context.Background()
	e.bot.onMessage(ctx, e.photo(1, "/new demo Build this screen", "", 0))

	all, _ := e.st.ListTasks(ctx, store.TaskFilter{Limit: 10})
	var created *store.Task
	for _, tk := range all {
		if tk.Prompt == "Build this screen" {
			created = tk
		}
	}
	if created == nil {
		t.Fatal("/new with a photo created no task")
	}
	live, _ := e.st.LiveAttachments(ctx, created.ID)
	if len(live) != 1 || live[0].Width != 20 {
		t.Fatalf("attachments of the new task: %+v", live)
	}
}

func TestImageDocumentWithGenericMIMEIsAccepted(t *testing.T) {
	e := newImageEnv(t)
	ctx := context.Background()
	m := &telego.Message{MessageID: 22, From: &telego.User{ID: owner}, Chat: telego.Chat{ID: owner, Type: "private"},
		Caption: "Use this exact image", ReplyToMessage: &telego.Message{MessageID: e.card},
		Document: &telego.Document{FileID: e.files + "/screen", FileName: "screen.PNG", MimeType: "application/octet-stream"}}
	e.bot.onMessage(ctx, m)
	msgs := e.messageEvents(t)
	if len(msgs) != 1 || len(msgs[0]["attachments"].([]any)) != 1 {
		t.Fatalf("image document message: %v", msgs)
	}
}
