package api

import (
	"net/http"

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
