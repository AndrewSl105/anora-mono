package api

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/andrewsl105/anora-runner/internal/store"
)

const wsPingInterval = 30 * time.Second

type wsIn struct {
	Op     string `json:"op"`
	TaskID int64  `json:"task_id"`
	Scope  string `json:"scope"`
}

type wsOut struct {
	Op      string       `json:"op"`
	TaskID  int64        `json:"task_id,omitempty"`
	Event   *store.Event `json:"event,omitempty"`
	Task    *store.Task  `json:"task,omitempty"`
	Message string       `json:"message,omitempty"`
}

type wsSubs struct {
	mu    sync.Mutex
	tasks map[int64]bool
	all   bool
}

func (s *wsSubs) wants(taskID int64, isEvent bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tasks[taskID] {
		return true
	}
	return s.all && !isEvent
}

func (s *Server) websocket(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: originHosts(s.Cfg.CORSOrigins)})
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	msgs, unsubscribe := s.Hub.Subscribe(256)
	defer unsubscribe()
	subs := &wsSubs{tasks: map[int64]bool{}}

	out := make(chan wsOut, 64)
	go func() {
		defer cancel()
		for {
			var in wsIn
			if err := wsjson.Read(ctx, c, &in); err != nil {
				return
			}
			subs.mu.Lock()
			switch {
			case in.Op == "subscribe" && in.TaskID > 0:
				subs.tasks[in.TaskID] = true
			case in.Op == "subscribe" && in.Scope == "tasks":
				subs.all = true
			case in.Op == "unsubscribe" && in.TaskID > 0:
				delete(subs.tasks, in.TaskID)
			case in.Op == "unsubscribe" && in.Scope == "tasks":
				subs.all = false
			default:
				select {
				case out <- wsOut{Op: "error", Message: "expected {op: subscribe|unsubscribe, task_id | scope: \"tasks\"}"}:
				default:
				}
			}
			subs.mu.Unlock()
		}
	}()

	ping := time.NewTicker(wsPingInterval)
	defer ping.Stop()
	write := func(v wsOut) bool {
		wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
		defer wcancel()
		return wsjson.Write(wctx, c, v) == nil
	}
	for {
		select {
		case <-ctx.Done():
			c.Close(websocket.StatusNormalClosure, "")
			return
		case m := <-out:
			if !write(m) {
				return
			}
		case m, ok := <-msgs:
			if !ok {
				return
			}
			switch {
			case m.Event != nil && subs.wants(m.TaskID, true):
				if !write(wsOut{Op: "event", TaskID: m.TaskID, Event: m.Event}) {
					return
				}
			case m.Task != nil && subs.wants(m.TaskID, false):
				if !write(wsOut{Op: "task", Task: m.Task}) {
					return
				}
			}
		case <-ping.C:
			pctx, pcancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.Ping(pctx)
			pcancel()
			if err != nil {
				slog.Debug("ws ping failed", "err", err)
				return
			}
		}
	}
}

// originHosts turns CORS origins (https://host:port) into Accept's host patterns.
func originHosts(origins []string) []string {
	var out []string
	for _, o := range origins {
		if u, err := urlHost(o); err == nil && u != "" {
			out = append(out, u)
		}
	}
	return out
}
