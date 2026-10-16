package telegram

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mymmrac/telego"

	"github.com/andrewsl105/anora/server/internal/events"
	"github.com/andrewsl105/anora/server/internal/projects"
	"github.com/andrewsl105/anora/server/internal/store"
	"github.com/andrewsl105/anora/server/internal/tasks"
	"github.com/andrewsl105/anora/server/internal/testutil"
)

type transcriberFunc func(context.Context, string, string, []byte) (string, error)

func (f transcriberFunc) Transcribe(ctx context.Context, name, mime string, data []byte) (string, error) {
	return f(ctx, name, mime, data)
}

func TestTranscribeHTTPMultipart(t *testing.T) {
	var gotPath, gotAuth, gotModel, gotName, gotType, gotAudio string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("multipart: %v", err)
			w.WriteHeader(400)
			return
		}
		gotModel = r.FormValue("model")
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Errorf("file: %v", err)
			w.WriteHeader(400)
			return
		}
		defer file.Close()
		gotName, gotType = header.Filename, header.Header.Get("Content-Type")
		raw, _ := io.ReadAll(file)
		gotAudio = string(raw)
		_, _ = io.WriteString(w, `{"text":"Привіт from the API"}`)
	}))
	defer srv.Close()

	got, err := transcribeHTTP(context.Background(), srv.Client(), srv.URL+"/v1/audio/transcriptions", "secret",
		"bad\r\n\"\\voice.ogg", "", []byte("audio"), map[string]string{"model": "gpt-transcribe"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "Привіт from the API" || gotPath != "/v1/audio/transcriptions" || gotAuth != "Bearer secret" || gotModel != "gpt-transcribe" || gotName != "badvoice.ogg" || gotType != "application/octet-stream" || gotAudio != "audio" {
		t.Fatalf("transcription=%q path=%q auth=%q model=%q name=%q type=%q audio=%q", got, gotPath, gotAuth, gotModel, gotName, gotType, gotAudio)
	}
}

func TestWhisperCPPRequest(t *testing.T) {
	var path, format, language, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		_ = r.ParseMultipartForm(1 << 20)
		format, language = r.FormValue("response_format"), r.FormValue("language")
		_, _ = io.WriteString(w, `{"text":"hello"}`)
	}))
	defer srv.Close()
	got, err := (&WhisperCPPTranscriber{URL: srv.URL, Client: srv.Client()}).Transcribe(context.Background(), "voice.ogg", "audio/ogg", []byte("audio"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello" || path != "/inference" || format != "json" || language != "auto" || auth != "" {
		t.Fatalf("got=%q path=%q format=%q language=%q auth=%q", got, path, format, language, auth)
	}
}

func TestVoiceDisabledUsesExactHint(t *testing.T) {
	st := testutil.DB(t)
	api := &fakeAPI{}
	hub := events.NewHub()
	b := &Bot{API: api, Store: st, Tasks: &tasks.Service{Store: st, Hub: hub}, Hub: hub, OwnerID: owner}
	b.init()
	m := &telego.Message{MessageID: 7, From: &telego.User{ID: owner}, Chat: telego.Chat{ID: owner, Type: "private"}, Voice: &telego.Voice{FileID: "voice"}}
	b.onMessage(context.Background(), m)
	sends, _, _ := api.snapshot()
	if len(sends) != 1 || sends[0].text != "Voice messages are not enabled — dictate with the keyboard microphone instead." {
		t.Fatalf("voice-disabled reply: %+v", sends)
	}
}

func TestVoiceTranscriptAppearsInDraft(t *testing.T) {
	st := testutil.DB(t)
	ctx := context.Background()
	api := &fakeAPI{}
	hub := events.NewHub()
	name := "Demo"
	_, _ = (&projects.Service{Store: st}).Create(ctx, projects.CreateInput{ID: "demo", ProjectFields: store.ProjectFields{Name: &name}})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "audio") }))
	defer srv.Close()
	b := &Bot{API: api, Store: st, Tasks: &tasks.Service{Store: st, Hub: hub}, Hub: hub, OwnerID: owner, STTEnabled: true,
		STT: transcriberFunc(func(_ context.Context, name, mime string, data []byte) (string, error) {
			if name != "voice.ogg" || mime != "audio/ogg" || string(data) != "audio" {
				t.Fatalf("STT input %q %q %q", name, mime, data)
			}
			return "Зроби українську локалізацію", nil
		})}
	b.init()
	m := &telego.Message{MessageID: 8, From: &telego.User{ID: owner}, Chat: telego.Chat{ID: owner, Type: "private"}, Voice: &telego.Voice{FileID: srv.URL, MimeType: "audio/ogg"}}
	b.onMessage(ctx, m)
	var text string
	if err := st.Pool.QueryRow(ctx, `SELECT text FROM telegram_drafts ORDER BY id DESC LIMIT 1`).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if text != "Зроби українську локалізацію" {
		t.Fatalf("draft transcript %q", text)
	}
	sends, _, _ := api.snapshot()
	if len(sends) == 0 || !strings.Contains(sends[len(sends)-1].text, "Зроби українську локалізацію") {
		t.Fatalf("draft card: %+v", sends)
	}
}
