package auth

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

// TicketTTL is how long a WebSocket ticket can be redeemed.
const TicketTTL = 30 * time.Second

// Tickets are short-lived, single-use WebSocket credentials. A browser cannot
// set headers on a WebSocket handshake, so a Telegram session that works by
// bearer token (no cookie in a cross-site iframe) exchanges it for a ticket
// and passes that as ?ticket=. In memory: a restart only forces a new ticket.
type Tickets struct {
	mu  sync.Mutex
	m   map[string]ticket
	now func() time.Time
	TTL time.Duration
}

type ticket struct {
	sessionID string
	expires   time.Time
}

func NewTickets() *Tickets {
	return &Tickets{m: map[string]ticket{}, now: time.Now, TTL: TicketTTL}
}

// Issue returns a new ticket for a session.
func (t *Tickets) Issue(sessionID string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	v := base64.RawURLEncoding.EncodeToString(b)
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	for k, x := range t.m {
		if now.After(x.expires) {
			delete(t.m, k)
		}
	}
	t.m[v] = ticket{sessionID: sessionID, expires: now.Add(t.TTL)}
	return v, nil
}

// Redeem consumes a ticket and returns its session id; a ticket works once.
func (t *Tickets) Redeem(v string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	x, ok := t.m[v]
	if !ok {
		return "", false
	}
	delete(t.m, v)
	if t.now().After(x.expires) {
		return "", false
	}
	return x.sessionID, true
}
