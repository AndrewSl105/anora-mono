package api

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/andrewsl105/fleetpocket/server/internal/nodes"
	"github.com/andrewsl105/fleetpocket/server/internal/store"
)

// ---- nodes (computers that run tasks next to the server) ----

func (s *Server) listNodes(w http.ResponseWriter, r *http.Request) {
	ns, err := s.Nodes.List(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": ns})
}

func (s *Server) createNode(w http.ResponseWriter, r *http.Request) {
	var in nodes.CreateInput
	if !decode(w, r, &in) {
		return
	}
	n, tok, err := s.Nodes.Create(r.Context(), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"node": n, "token": tok})
}

func (s *Server) updateNode(w http.ResponseWriter, r *http.Request) {
	var in store.NodeFields
	if !decode(w, r, &in) {
		return
	}
	n, err := s.Nodes.Update(r.Context(), r.PathValue("id"), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (s *Server) rotateNodeToken(w http.ResponseWriter, r *http.Request) {
	tok, err := s.Nodes.RotateToken(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": tok})
}

func (s *Server) deleteNode(w http.ResponseWriter, r *http.Request) {
	if err := s.Nodes.Delete(r.Context(), r.PathValue("id")); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---- pairing (fleetpocket-node setup) ----

// startPairing is POST /node/pair: a computer asks for a pairing code. No
// session: the code is worthless until the owner approves it.
func (s *Server) startPairing(w http.ResponseWriter, r *http.Request) {
	var in nodes.PairingRequest
	if !decode(w, r, &in) {
		return
	}
	res, err := s.Nodes.StartPairing(in, clientIP(r))
	if errors.Is(err, nodes.ErrTooManyPairings) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", err.Error())
		return
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	res.ApproveURL = s.publicURL(r) + "/settings?pair=" + url.QueryEscape(res.Code)
	writeJSON(w, http.StatusCreated, res)
}

// pollPairing is GET /node/pair/{id}: the computer waits for approval; the
// token is in the first approved answer only.
func (s *Server) pollPairing(w http.ResponseWriter, r *http.Request) {
	res, err := s.Nodes.PollPairing(r.PathValue("id"))
	if errors.Is(err, nodes.ErrPairingNotFound) {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, res)
}

// lookupPairing is GET /nodes/pair/{code} (owner): the computer waiting with this code.
func (s *Server) lookupPairing(w http.ResponseWriter, r *http.Request) {
	p, err := s.Nodes.LookupPairing(r.PathValue("code"))
	if errors.Is(err, nodes.ErrPairingNotFound) {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// approvePairing is POST /nodes/pair (owner): registers the computer whose
// setup shows this code.
func (s *Server) approvePairing(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	n, err := s.Nodes.ApprovePairing(r.Context(), in.Code, in.Name)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, n)
}

// installScript is GET /node/install: `curl -fsSL <server>/api/v1/node/install | sh`.
func (s *Server) installScript(w http.ResponseWriter, r *http.Request) {
	dl := s.Cfg.NodeDownloadURL
	if dl == "" {
		dl = nodes.DefaultDownloadURL
	}
	b, err := nodes.InstallScript(s.publicURL(r), dl)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(b)
}

// publicURL is PUBLIC_BASE_URL, else the URL the request came in on.
func (s *Server) publicURL(r *http.Request) string {
	if s.Cfg.PublicBaseURL != "" {
		return s.Cfg.PublicBaseURL
	}
	scheme := "https"
	if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") != "https" && s.Cfg.CookieInsecure {
		scheme = "http"
	}
	return scheme + "://" + r.Host
}
