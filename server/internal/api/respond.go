package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/andrewsl105/anora-runner/internal/projects"
	"github.com/andrewsl105/anora-runner/internal/store"
	"github.com/andrewsl105/anora-runner/internal/tasks"
)

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	var b errorBody
	b.Error.Code, b.Error.Message = code, msg
	writeJSON(w, status, b)
}

// writeErr maps service errors to HTTP responses.
func writeErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, tasks.ErrInvalid), errors.Is(err, projects.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
	case errors.Is(err, tasks.ErrNotRunning):
		writeError(w, http.StatusConflict, "not_running", err.Error())
	case errors.Is(err, errGitHubDisabled):
		writeError(w, http.StatusServiceUnavailable, "github_disabled", err.Error())
	default:
		slog.ErrorContext(r.Context(), "request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
	}
}

const maxBody = 1 << 20

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return false
	}
	return true
}

type page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor"`
}

// limitParam reads ?limit= (default 50, max 200).
func limitParam(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n <= 0 {
		return 50
	}
	return min(n, 200)
}

func pathID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid", name+" must be a positive integer")
		return 0, false
	}
	return id, true
}
