package api

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"
)

type ctxKey int

const sessionKindKey ctxKey = 1

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController and websocket upgrades reach the original writer.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		defer func() {
			if v := recover(); v != nil {
				slog.Error("panic", "path", r.URL.Path, "panic", v)
				writeError(rec, http.StatusInternalServerError, "internal", "internal error")
			}
			if r.URL.Path == "/healthz" || r.URL.Path == "/metrics" {
				return
			}
			slog.Info("http", "method", r.Method, "path", r.URL.Path, "status", rec.status, "dur_ms", time.Since(start).Milliseconds())
		}()
		next.ServeHTTP(rec, r)
	})
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && slices.Contains(s.Cfg.CORSOrigins, origin) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Set("Access-Control-Allow-Headers", "Content-Type, X-Requested-With")
			h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
			h.Add("Vary", "Origin")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// csrf requires X-Requested-With: anora on state-changing requests.
func csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if r.Header.Get("X-Requested-With") != "anora" {
				writeError(w, http.StatusForbidden, "csrf", "missing X-Requested-With: anora header")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, err := s.Auth.Session(r.Context(), w, r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "login required")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), sessionKindKey, sess.Kind)))
	}
}

// clientIP trusts X-Forwarded-For only when the direct peer is a private
// address (the reverse proxy); it then uses the rightmost entry.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsPrivate() || ip.IsLoopback()) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	return host
}

// internalOnly allows direct requests from private networks that did not
// pass through the reverse proxy (no X-Forwarded-For).
func internalOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		ip := net.ParseIP(host)
		if ip == nil || !(ip.IsPrivate() || ip.IsLoopback()) || r.Header.Get("X-Forwarded-For") != "" {
			writeError(w, http.StatusForbidden, "forbidden", "internal network only")
			return
		}
		next.ServeHTTP(w, r)
	})
}
