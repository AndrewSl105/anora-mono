// Package events is the in-process fan-out of task events and task updates
// to WebSocket clients and the Telegram bot.
package events

import (
	"sync"

	"github.com/andrewsl105/anora-runner/internal/store"
)

// Message is what subscribers receive: either a timeline event or a task snapshot.
type Message struct {
	TaskID int64
	Event  *store.Event // set for timeline events
	Task   *store.Task  // set for task changes
}

type Hub struct {
	mu   sync.Mutex
	subs map[chan Message]struct{}
}

func NewHub() *Hub { return &Hub{subs: map[chan Message]struct{}{}} }

// Subscribe returns a buffered channel and a cancel func. Slow subscribers
// lose messages rather than block publishers; clients recover via the API.
func (h *Hub) Subscribe(buffer int) (<-chan Message, func()) {
	ch := make(chan Message, buffer)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
		h.mu.Unlock()
	}
}

func (h *Hub) publish(m Message) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- m:
		default:
		}
	}
}

func (h *Hub) PublishEvent(ev *store.Event) {
	if ev != nil {
		h.publish(Message{TaskID: ev.TaskID, Event: ev})
	}
}

// PublishTask publishes a task snapshot, preceded by the status events that
// produced it, if any.
func (h *Hub) PublishTask(t *store.Task) {
	if t == nil {
		return
	}
	for _, ev := range t.StatusEvents {
		h.PublishEvent(ev)
	}
	h.publish(Message{TaskID: t.ID, Task: t})
}
