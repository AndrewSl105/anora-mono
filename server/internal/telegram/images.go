package telegram

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"

	"github.com/andrewsl105/fleetpocket/server/internal/store"
)

// Telegram delivers an album (media group) as one message per photo, with
// the caption on one of them. The bot collects a group for AlbumWait after
// its latest part and then handles it as a single message: one task, one
// draft update or one message to a task.

const defaultAlbumWait = 1500 * time.Millisecond

type imageState struct {
	mu      sync.Mutex
	albums  map[string]*album
	pending map[promptKey]pendingImages
}

type album struct {
	parts []*telego.Message
	timer *time.Timer
	first time.Time // when the first part arrived
}

type promptKey struct{ chatID, messageID int64 }

// pendingImages are images sent to a task without text, waiting for the
// owner's reply to the bot's "what should Claude do" prompt.
type pendingImages struct {
	taskID  int64
	media   []store.TelegramDraftMedia
	expires time.Time
}

type albumMediaKey struct{}

type arrivedKey struct{}

// withArrived records when a message (an album: its first part) arrived, so
// the draft merge window is measured from there, not from when the album was
// handled after collecting its parts.
func withArrived(ctx context.Context, t time.Time) context.Context {
	return context.WithValue(ctx, arrivedKey{}, t)
}

func arrived(ctx context.Context) time.Time {
	if t, ok := ctx.Value(arrivedKey{}).(time.Time); ok {
		return t
	}
	return time.Now()
}

type albumAcknowledgedKey struct{}

// withAlbumMedia carries the media of an album's other parts to
// incomingDraftContent, so the existing routes see the whole album.
func withAlbumMedia(ctx context.Context, media []store.TelegramDraftMedia) context.Context {
	ctx = context.WithValue(ctx, albumMediaKey{}, media)
	return context.WithValue(ctx, albumAcknowledgedKey{}, true)
}

func albumMedia(ctx context.Context) []store.TelegramDraftMedia {
	m, _ := ctx.Value(albumMediaKey{}).([]store.TelegramDraftMedia)
	return m
}

func albumAcknowledged(ctx context.Context) bool {
	acknowledged, _ := ctx.Value(albumAcknowledgedKey{}).(bool)
	return acknowledged
}

// messageMedia is the image of one message (photo or image document).
func messageMedia(m *telego.Message) []store.TelegramDraftMedia {
	var media []store.TelegramDraftMedia
	if len(m.Photo) > 0 {
		p := m.Photo[len(m.Photo)-1]
		media = append(media, store.TelegramDraftMedia{FileID: p.FileID, Name: "photo.jpg", Kind: "image"})
	}
	if isImageDocument(m.Document) {
		media = append(media, store.TelegramDraftMedia{FileID: m.Document.FileID, Name: m.Document.FileName, Kind: "image"})
	}
	return media
}

func isImageDocument(d *telego.Document) bool {
	if d == nil {
		return false
	}
	if strings.HasPrefix(strings.ToLower(d.MimeType), "image/") {
		return true
	}
	name := strings.ToLower(d.FileName)
	for _, ext := range []string{".png", ".jpg", ".jpeg", ".webp", ".gif"} {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}

// collectAlbum adds a part of a media group; the group is handled once no
// new part arrived for AlbumWait.
func (b *Bot) collectAlbum(ctx context.Context, m *telego.Message) {
	// Each Telegram update is acknowledged as soon as it arrives, even though
	// the group is delivered only after the short collection window.
	b.react(ctx, m, "👀")
	wait := b.AlbumWait
	if wait == 0 {
		wait = defaultAlbumWait
	}
	b.images.mu.Lock()
	defer b.images.mu.Unlock()
	if b.images.albums == nil {
		b.images.albums = map[string]*album{}
	}
	key := fmt.Sprintf("%d/%s", m.Chat.ID, m.MediaGroupID)
	a := b.images.albums[key]
	if a == nil {
		a = &album{first: time.Now()}
		b.images.albums[key] = a
	} else {
		a.timer.Stop()
	}
	a.parts = append(a.parts, m)
	a.timer = time.AfterFunc(wait, func() {
		b.images.mu.Lock()
		parts := a.parts
		delete(b.images.albums, key)
		b.images.mu.Unlock()
		b.handleAlbum(withArrived(ctx, a.first), parts)
	})
}

// handleAlbum handles an album as its leading part (the one with the
// caption, else the first) carrying the other parts' images and reply.
func (b *Bot) handleAlbum(ctx context.Context, parts []*telego.Message) {
	lead := parts[0]
	for _, p := range parts {
		if strings.TrimSpace(p.Caption) != "" {
			lead = p
			break
		}
	}
	var others []store.TelegramDraftMedia
	for _, p := range parts {
		if p == lead {
			continue
		}
		others = append(others, messageMedia(p)...)
		if lead.ReplyToMessage == nil && p.ReplyToMessage != nil {
			lead.ReplyToMessage = p.ReplyToMessage
		}
	}
	ok := b.handleMessage(withAlbumMedia(ctx, others), lead)
	for _, p := range parts {
		if p == lead {
			continue
		}
		if ok {
			b.react(ctx, p, "✅")
		} else {
			b.react(ctx, p, "❌")
		}
	}
}

// askImageText asks for the text that goes with images sent to a task
// without a caption; the reply to the prompt sends both.
func (b *Bot) askImageText(ctx context.Context, m *telego.Message, taskID int64, media []store.TelegramDraftMedia) error {
	id, err := b.send(ctx, target{m.Chat.ID, int64(m.MessageThreadID)}, "✏️ What should Claude do with the image(s)? Reply with the text.",
		&telego.ForceReply{ForceReply: true, InputFieldPlaceholder: "What should Claude do?"}, m.MessageID)
	if err != nil {
		return err
	}
	if err := b.Store.AddTelegramReplyPrompt(ctx, m.Chat.ID, int64(id), store.TelegramReplyPrompt{Kind: "task_images", TaskID: taskID}); err != nil {
		return err
	}
	if err := b.Store.AddTelegramTaskMessage(ctx, store.TelegramMessage{ChatID: m.Chat.ID, MessageID: int64(id), TaskID: taskID, Kind: "image_prompt"}); err != nil {
		return err
	}
	b.images.mu.Lock()
	defer b.images.mu.Unlock()
	if b.images.pending == nil {
		b.images.pending = map[promptKey]pendingImages{}
	}
	now := time.Now()
	for k, p := range b.images.pending {
		if now.After(p.expires) {
			delete(b.images.pending, k)
		}
	}
	b.images.pending[promptKey{m.Chat.ID, int64(id)}] = pendingImages{taskID: taskID, media: media, expires: now.Add(24 * time.Hour)}
	return nil
}

// takePendingImages returns (and forgets) the images waiting for the text
// that replies to prompt messageID.
func (b *Bot) takePendingImages(chatID, messageID int64) ([]store.TelegramDraftMedia, bool) {
	b.images.mu.Lock()
	defer b.images.mu.Unlock()
	p, ok := b.images.pending[promptKey{chatID, messageID}]
	delete(b.images.pending, promptKey{chatID, messageID})
	if !ok || time.Now().After(p.expires) {
		return nil, false
	}
	return p.media, true
}
