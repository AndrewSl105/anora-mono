package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/andrewsl105/anora/server/internal/api"
	"github.com/andrewsl105/anora/server/internal/attachments"
	"github.com/andrewsl105/anora/server/internal/auth"
	"github.com/andrewsl105/anora/server/internal/config"
	"github.com/andrewsl105/anora/server/internal/events"
	"github.com/andrewsl105/anora/server/internal/projects"
	"github.com/andrewsl105/anora/server/internal/store"
	"github.com/andrewsl105/anora/server/internal/tasks"
	"github.com/andrewsl105/anora/server/internal/testutil"
)

func setupWithFiles(t *testing.T) (*client, *store.Store) {
	t.Helper()
	st := testutil.DB(t)
	hash, _ := auth.HashPassword("pw-for-tests")
	secret, _, _ := auth.NewTOTP("t")
	hub := events.NewHub()
	files := &attachments.Files{Dir: t.TempDir()}
	srv := &api.Server{
		Cfg: &config.Config{}, Store: st, Hub: hub, Files: files,
		Tasks:    &tasks.Service{Store: st, Hub: hub, Files: files},
		Projects: &projects.Service{Store: st},
		Auth:     &auth.Manager{Store: st, PasswordHash: hash, TOTPSecret: secret, SessionSecret: []byte("s"), CookieInsecure: true},
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	c := &client{t: t, base: ts.URL, http: &http.Client{Jar: jar}}
	otp, _ := totp.GenerateCode(secret, time.Now())
	if code, body := c.do("POST", "/api/v1/auth/login", map[string]string{"password": "pw-for-tests", "totp": otp}); code != 200 {
		t.Fatalf("login: %d %v", code, body)
	}
	c.do("POST", "/api/v1/projects", map[string]any{"id": "demo", "name": "Demo"})
	c.do("POST", "/api/v1/projects/demo/repositories", map[string]any{"full_name": "o/r", "installation_id": 1, "base_branch": "main"})
	return c, st
}

type part struct {
	field, name string
	data        []byte
}

func (c *client) multipart(path string, parts ...part) (int, map[string]any) {
	c.t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	for _, p := range parts {
		var w io.Writer
		if p.name != "" {
			w, _ = mw.CreateFormFile(p.field, p.name)
		} else {
			w, _ = mw.CreateFormField(p.field)
		}
		_, _ = w.Write(p.data)
	}
	_ = mw.Close()
	req, _ := http.NewRequest("POST", c.base+path, &b)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Requested-With", "anora")
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (c *client) get(path string) (*http.Response, []byte) {
	c.t.Helper()
	resp, err := c.http.Get(c.base + path)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func pngBytes(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 0x80
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

func jpegBytes(w, h int) []byte {
	img := image.NewGray(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.White)
	var b bytes.Buffer
	_ = jpeg.Encode(&b, img, nil)
	return b.Bytes()
}

// eventsOf returns a task's events of one type.
func (c *client) eventsOf(taskID int64, typ string) []map[string]any {
	c.t.Helper()
	_, body := c.do("GET", fmt.Sprintf("/api/v1/tasks/%d/events", taskID), nil)
	var out []map[string]any
	for _, it := range body["items"].([]any) {
		if ev := it.(map[string]any); ev["type"] == typ {
			out = append(out, ev)
		}
	}
	return out
}

func errCode(body map[string]any) string {
	if e, ok := body["error"].(map[string]any); ok {
		return fmt.Sprint(e["code"], ": ", e["message"])
	}
	return ""
}

func TestAttachmentUploadAndDownload(t *testing.T) {
	c, st := setupWithFiles(t)

	code, task := c.multipart("/api/v1/tasks",
		part{field: "project_id", data: []byte("demo")},
		part{field: "prompt", data: []byte("Make it look like this")},
		part{field: "files[]", name: "mock.png", data: pngBytes(1200, 600)},
		part{field: "files[]", name: "photo.jpeg", data: jpegBytes(30, 20)},
	)
	if code != 201 || task["status"] != "queued" {
		t.Fatalf("multipart create: %d %v", code, task)
	}
	id := int64(task["id"].(float64))

	created := c.eventsOf(id, "created")
	if len(created) != 1 {
		t.Fatalf("created events: %v", created)
	}
	atts := created[0]["payload"].(map[string]any)["attachments"].([]any)
	if len(atts) != 2 {
		t.Fatalf("attachments: %v", atts)
	}
	first := atts[0].(map[string]any)
	if first["name"] != "mock.png" || first["mime"] != "image/png" || first["width"] != 1200.0 || first["height"] != 600.0 || first["size"].(float64) <= 0 {
		t.Fatalf("first attachment: %v", first)
	}
	if second := atts[1].(map[string]any); second["mime"] != "image/jpeg" || second["name"] != "photo.jpeg" {
		t.Fatalf("second attachment: %v", second)
	}
	aid := int64(first["id"].(float64))

	resp, body := c.get(fmt.Sprintf("/api/v1/tasks/%d/attachments/%d", id, aid))
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" || int64(len(body)) != int64(first["size"].(float64)) {
		t.Fatalf("download: %d %s %d bytes", resp.StatusCode, resp.Header.Get("Content-Type"), len(body))
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(resp.Header.Get("Cache-Control"), "private") {
		t.Fatalf("headers: %v", resp.Header)
	}
	resp, body = c.get(fmt.Sprintf("/api/v1/tasks/%d/attachments/%d?thumb=1", id, aid))
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("thumb: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if cfg, err := jpeg.DecodeConfig(bytes.NewReader(body)); err != nil || cfg.Width != 480 || cfg.Height != 240 {
		t.Fatalf("thumb size: %+v %v", cfg, err)
	}
	if resp, _ := c.get(fmt.Sprintf("/api/v1/tasks/%d/attachments/%d", id+1, aid)); resp.StatusCode != 404 {
		t.Fatalf("attachment of another task: %d", resp.StatusCode)
	}

	// A queued message carries its own attachments.
	code, view := c.multipart(fmt.Sprintf("/api/v1/tasks/%d/message", id),
		part{field: "text", data: []byte("and this one")},
		part{field: "files[]", name: "more.png", data: pngBytes(10, 10)},
	)
	if code != 200 || view["queued_message"] != "and this one" {
		t.Fatalf("multipart message: %d %v", code, view)
	}
	msgs := c.eventsOf(id, "message")
	if len(msgs) != 1 {
		t.Fatalf("message events: %v", msgs)
	}
	mp := msgs[0]["payload"].(map[string]any)
	if mp["delivery"] != "queued" || len(mp["attachments"].([]any)) != 1 {
		t.Fatalf("message payload: %v", mp)
	}
	// JSON messages still work and carry no attachments field.
	if code, _ := c.do("POST", fmt.Sprintf("/api/v1/tasks/%d/message", id), map[string]any{"text": "plain"}); code != 200 {
		t.Fatalf("json message: %d", code)
	}
	if mp := c.eventsOf(id, "message")[1]["payload"].(map[string]any); mp["attachments"] != nil {
		t.Fatalf("plain message payload: %v", mp)
	}

	// Removed attachments answer 410.
	if _, err := st.DeleteAttachments(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if resp, _ := c.get(fmt.Sprintf("/api/v1/tasks/%d/attachments/%d", id, aid)); resp.StatusCode != 410 {
		t.Fatalf("removed attachment: %d", resp.StatusCode)
	}
}

func TestAttachmentUploadRejects(t *testing.T) {
	c, _ := setupWithFiles(t)
	fields := []part{{field: "project_id", data: []byte("demo")}, {field: "prompt", data: []byte("p")}}
	many := append([]part{}, fields...)
	for i := 0; i < attachments.MaxFiles+1; i++ {
		many = append(many, part{field: "files[]", name: fmt.Sprintf("%d.png", i), data: pngBytes(2, 2)})
	}
	cases := map[string]struct {
		parts []part
		want  string
	}{
		"not an image":  {append(append([]part{}, fields...), part{field: "files[]", name: "notes.png", data: []byte("hello")}), `"notes.png" is not a PNG, JPEG, WebP or GIF`},
		"too many":      {many, "at most 10 images"},
		"unknown field": {append(append([]part{}, fields...), part{field: "secret", data: []byte("x")}), `unknown field "secret"`},
		"no prompt":     {[]part{fields[0], {field: "files[]", name: "a.png", data: pngBytes(2, 2)}}, "prompt is required"},
		"bad repo id":   {append(append([]part{}, fields...), part{field: "repository_id", data: []byte("x")}), "repository_id must be"},
	}
	for name, tc := range cases {
		code, body := c.multipart("/api/v1/tasks", tc.parts...)
		if code != 400 || !strings.Contains(errCode(body), tc.want) {
			t.Errorf("%s: %d %q, want 400 containing %q", name, code, errCode(body), tc.want)
		}
	}
	// Nothing was created by the rejected requests.
	if _, body := c.do("GET", "/api/v1/tasks", nil); len(body["items"].([]any)) != 0 {
		t.Fatalf("rejected uploads created tasks: %v", body["items"])
	}
}
