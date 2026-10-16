package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"

	"github.com/mymmrac/telego"

	"github.com/andrewsl105/anora/server/internal/attachments"
	"github.com/andrewsl105/anora/server/internal/store"
)

type Transcriber interface {
	Transcribe(context.Context, string, string, []byte) (string, error)
}

type OpenAITranscriber struct {
	APIKey string
	Client *http.Client
}

func (o *OpenAITranscriber) Transcribe(ctx context.Context, name, mime string, data []byte) (string, error) {
	return transcribeHTTP(ctx, o.Client, "https://api.openai.com/v1/audio/transcriptions", o.APIKey, name, mime, data,
		map[string]string{"model": "gpt-transcribe"})
}

type WhisperCPPTranscriber struct {
	URL    string
	Client *http.Client
}

func (w *WhisperCPPTranscriber) Transcribe(ctx context.Context, name, mime string, data []byte) (string, error) {
	return transcribeHTTP(ctx, w.Client, strings.TrimRight(w.URL, "/")+"/inference", "", name, mime, data,
		map[string]string{"response_format": "json", "language": "auto"})
}

func transcribeHTTP(ctx context.Context, client *http.Client, endpoint, apiKey, name, mime string, data []byte, fields map[string]string) (string, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for key, value := range fields {
		_ = w.WriteField(key, value)
	}
	name = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == '"' {
			return -1
		}
		return r
	}, name)
	if name == "" {
		name = "audio.ogg"
	}
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, name))
	h.Set("Content-Type", mime)
	part, err := w.CreatePart(h)
	if err != nil {
		return "", err
	}
	if _, err = part.Write(data); err != nil {
		return "", err
	}
	_ = w.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return "", err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("speech-to-text failed (%s): %s", resp.Status, cut(string(raw), 300))
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.Text) == "" {
		return "", fmt.Errorf("speech-to-text returned an empty transcript")
	}
	return strings.TrimSpace(out.Text), nil
}

func (b *Bot) incomingDraftContent(ctx context.Context, m *telego.Message) (string, []store.TelegramDraftMedia, error) {
	text := strings.TrimSpace(m.Text)
	if text == "" {
		text = strings.TrimSpace(m.Caption)
	}
	var media []store.TelegramDraftMedia
	if len(m.Photo) > 0 {
		p := m.Photo[len(m.Photo)-1]
		media = append(media, store.TelegramDraftMedia{FileID: p.FileID, Name: "photo.jpg", Kind: "image"})
	}
	if m.Document != nil && strings.HasPrefix(m.Document.MimeType, "image/") {
		media = append(media, store.TelegramDraftMedia{FileID: m.Document.FileID, Name: m.Document.FileName, Kind: "image"})
	}
	if m.Voice != nil || m.Audio != nil {
		if !b.STTEnabled || b.STT == nil {
			return "", nil, fmt.Errorf("Voice messages are not enabled — dictate with the keyboard microphone instead.")
		}
		fileID, name, mime := "", "voice.ogg", "audio/ogg"
		if m.Voice != nil {
			fileID = m.Voice.FileID
			if m.Voice.MimeType != "" {
				mime = m.Voice.MimeType
			}
		} else {
			fileID, name, mime = m.Audio.FileID, m.Audio.FileName, m.Audio.MimeType
		}
		raw, err := b.download(ctx, fileID)
		if err != nil {
			return "", nil, err
		}
		transcript, err := b.STT.Transcribe(ctx, name, mime, raw)
		if err != nil {
			return "", nil, err
		}
		if text != "" {
			text += "\n\n"
		}
		text += transcript
	}
	return text, media, nil
}

func (b *Bot) incomingContent(ctx context.Context, m *telego.Message) (string, []*attachments.Image, error) {
	text, media, err := b.incomingDraftContent(ctx, m)
	if err != nil {
		return "", nil, err
	}
	images, err := b.downloadDraftImages(ctx, media)
	return text, images, err
}

func (b *Bot) downloadDraftImages(ctx context.Context, media []store.TelegramDraftMedia) ([]*attachments.Image, error) {
	if len(media) > attachments.MaxFiles {
		return nil, fmt.Errorf("at most %d images are allowed", attachments.MaxFiles)
	}
	var out []*attachments.Image
	total := 0
	for _, m := range media {
		raw, err := b.download(ctx, m.FileID)
		if err != nil {
			return nil, err
		}
		total += len(raw)
		if total > attachments.MaxRequestBytes {
			return nil, fmt.Errorf("images are larger than %d MB in total", attachments.MaxRequestBytes>>20)
		}
		im, err := attachments.Process(m.Name, raw)
		if err != nil {
			return nil, err
		}
		out = append(out, im)
	}
	return out, nil
}

func (b *Bot) download(ctx context.Context, fileID string) ([]byte, error) {
	f, err := b.API.GetFile(ctx, &telego.GetFileParams{FileID: fileID})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.API.FileDownloadURL(f.FilePath), nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("download Telegram file: %s", resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, attachments.MaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > attachments.MaxFileBytes {
		return nil, fmt.Errorf("Telegram file is larger than %d MB", attachments.MaxFileBytes>>20)
	}
	return raw, nil
}
