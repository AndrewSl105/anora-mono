package api

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strconv"

	"github.com/andrewsl105/fleetpocket/server/internal/attachments"
)

// maxUploadBody bounds a multipart request: the files plus room for the text fields.
const maxUploadBody = attachments.MaxRequestBytes + maxBody

func isMultipart(r *http.Request) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mt == "multipart/form-data"
}

// readMultipart reads a multipart/form-data body: text fields named in
// `fields` and image files in `files[]`, each validated and cleaned with
// attachments.Process while the body streams. It writes a 400 and returns
// false on any problem; unknown fields are rejected like unknown JSON fields.
func readMultipart(w http.ResponseWriter, r *http.Request, fields ...string) (map[string]string, []*attachments.Image, bool) {
	allowed := map[string]bool{}
	for _, f := range fields {
		allowed[f] = true
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBody)
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid", "multipart body: "+err.Error())
		return nil, nil, false
	}
	values := map[string]string{}
	var imgs []*attachments.Image
	var total int64
	fail := func(msg string) (map[string]string, []*attachments.Image, bool) {
		writeError(w, http.StatusBadRequest, "invalid", msg)
		return nil, nil, false
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return fail(fmt.Sprintf("the upload is larger than %d MB", attachments.MaxRequestBytes>>20))
		}
		if err != nil {
			return fail("multipart body: " + err.Error())
		}
		name := part.FormName()
		switch {
		case name == "files[]" || name == "files":
			if len(imgs) == attachments.MaxFiles {
				return fail(fmt.Sprintf("at most %d images per request", attachments.MaxFiles))
			}
			data, err := io.ReadAll(io.LimitReader(part, attachments.MaxFileBytes+1))
			if errors.As(err, &tooBig) {
				return fail(fmt.Sprintf("the upload is larger than %d MB", attachments.MaxRequestBytes>>20))
			}
			if err != nil {
				return fail("multipart body: " + err.Error())
			}
			if total += int64(len(data)); total > attachments.MaxRequestBytes {
				return fail(fmt.Sprintf("the images are larger than %d MB together", attachments.MaxRequestBytes>>20))
			}
			img, err := attachments.Process(part.FileName(), data)
			if err != nil {
				return fail(err.Error())
			}
			imgs = append(imgs, img)
		case allowed[name]:
			if _, dup := values[name]; dup {
				return fail("duplicate field " + name)
			}
			b, err := io.ReadAll(io.LimitReader(part, maxBody+1))
			if err != nil {
				return fail("multipart body: " + err.Error())
			}
			if len(b) > maxBody {
				return fail("field " + name + " is too long")
			}
			values[name] = string(b)
		default:
			return fail(fmt.Sprintf("unknown field %q", name))
		}
	}
	return values, imgs, true
}

// formInt64 parses an optional integer form field (empty = 0).
func formInt64(values map[string]string, name string) (int64, error) {
	v := values[name]
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return n, nil
}

// attachment serves an attachment (or its thumbnail with ?thumb=1) to the
// owner. A removed attachment answers 410 gone.
func (s *Server) attachment(w http.ResponseWriter, r *http.Request) {
	taskID, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	id, ok := pathID(w, r, "attachmentId")
	if !ok {
		return
	}
	if s.Files == nil {
		writeError(w, http.StatusNotFound, "not_found", "attachments are not configured")
		return
	}
	a, err := s.Store.GetAttachment(r.Context(), taskID, id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if a.DeletedAt != nil {
		writeError(w, http.StatusGone, "gone", "the attachment was removed")
		return
	}
	path, ctype, name := s.Files.Path(a), a.Mime, a.Name
	if r.URL.Query().Get("thumb") == "1" {
		path, ctype, name = s.Files.ThumbPath(a), "image/jpeg", "thumb-"+name
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusGone, "gone", "the attachment was removed")
		return
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		writeErr(w, r, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "private, max-age=86400")
	h.Set("Content-Disposition", "inline; filename*=UTF-8''"+url.PathEscape(name))
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	http.ServeContent(w, r, "", st.ModTime(), f)
}
