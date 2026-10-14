package api

import "net/http"

func (s *Server) websocket(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotImplemented, "not_implemented", "WebSocket arrives in phase 6")
}
