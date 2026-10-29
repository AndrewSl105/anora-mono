// Package nodes registers the owner's computers that run tasks next to the
// server, authenticates them and serves their WebSocket
// (docs/design/remote-nodes.md).
package nodes

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/andrewsl105/fleetpocket/server/internal/events"
	"github.com/andrewsl105/fleetpocket/server/internal/hosts"
	"github.com/andrewsl105/fleetpocket/server/internal/store"
)

var (
	ErrInvalid      = errors.New("invalid input")
	ErrUnauthorized = errors.New("invalid node token")
	idRe            = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
)

const tokenPrefix = "fpn_"

// NewToken returns a node token "fpn_<id>.<secret>" and its SHA-256.
func NewToken(id string) (string, []byte) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	tok := tokenPrefix + id + "." + base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(tok))
	return tok, h[:]
}

// TokenNode returns the node id a token names ("" when malformed).
func TokenNode(tok string) string {
	rest, ok := strings.CutPrefix(tok, tokenPrefix)
	if !ok {
		return ""
	}
	id, _, ok := strings.Cut(rest, ".")
	if !ok || !idRe.MatchString(id) {
		return ""
	}
	return id
}

func tokenMatches(tok string, hash []byte) bool {
	h := sha256.Sum256([]byte(tok))
	return subtle.ConstantTimeCompare(h[:], hash) == 1
}

// Service is the owner API for nodes plus the node connection endpoint.
type Service struct {
	Store    *store.Store
	Hub      *events.Hub
	Registry *hosts.Registry
}

// View returns a node as clients see it.
func (s *Service) View(ctx context.Context, n *store.Node) *store.NodeView {
	v := &store.NodeView{Node: n, Online: s.Registry.IsOnline(n.ID)}
	if counts, err := s.Store.ActiveTasksByNode(ctx); err == nil {
		v.ActiveTasks = counts[n.ID]
	}
	return v
}

func (s *Service) List(ctx context.Context) ([]*store.NodeView, error) {
	ns, err := s.Store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	counts, err := s.Store.ActiveTasksByNode(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*store.NodeView, 0, len(ns))
	for _, n := range ns {
		out = append(out, &store.NodeView{Node: n, Online: s.Registry.IsOnline(n.ID), ActiveTasks: counts[n.ID]})
	}
	return out, nil
}

// CreateInput registers a node.
type CreateInput struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Create registers a node and returns it with its token (shown once).
func (s *Service) Create(ctx context.Context, in CreateInput) (*store.NodeView, string, error) {
	in.ID = strings.TrimSpace(in.ID)
	in.Name = strings.TrimSpace(in.Name)
	if !idRe.MatchString(in.ID) || in.ID == hosts.ServerID {
		return nil, "", fmt.Errorf("%w: id must be 1-32 lowercase letters, digits or '-' (not \"server\")", ErrInvalid)
	}
	if in.Name == "" {
		in.Name = in.ID
	}
	tok, hash := NewToken(in.ID)
	n, err := s.Store.CreateNode(ctx, in.ID, in.Name, hash)
	if err != nil {
		return nil, "", err
	}
	v := s.View(ctx, n)
	s.Hub.PublishNode(v)
	return v, tok, nil
}

func (s *Service) Update(ctx context.Context, id string, f store.NodeFields) (*store.NodeView, error) {
	if f.Name != nil {
		name := strings.TrimSpace(*f.Name)
		if name == "" {
			return nil, fmt.Errorf("%w: name must not be empty", ErrInvalid)
		}
		f.Name = &name
	}
	if f.MaxConcurrent != nil && (*f.MaxConcurrent < 1 || *f.MaxConcurrent > 32) {
		return nil, fmt.Errorf("%w: max_concurrent must be 1-32", ErrInvalid)
	}
	n, err := s.Store.UpdateNode(ctx, id, f)
	if err != nil {
		return nil, err
	}
	v := s.View(ctx, n)
	s.Hub.PublishNode(v)
	return v, nil
}

// RotateToken issues a new token; the old one stops working at once (a
// connected node keeps its connection until it reconnects).
func (s *Service) RotateToken(ctx context.Context, id string) (string, error) {
	tok, hash := NewToken(id)
	if err := s.Store.SetNodeToken(ctx, id, hash); err != nil {
		return "", err
	}
	// The connected node used the old token: it must reconnect with the new one.
	s.Registry.Kick(id)
	return tok, nil
}

func (s *Service) Delete(ctx context.Context, id string) error {
	if err := s.Store.DeleteNode(ctx, id); err != nil {
		return err
	}
	s.Registry.Kick(id)
	return nil
}

// Changed publishes a node's snapshot (the registry calls it on connect and
// disconnect).
func (s *Service) Changed(id string, online bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var n *store.Node
	var err error
	if online {
		n, err = s.Store.GetNode(ctx, id)
	} else {
		n, err = s.Store.NodeSeen(ctx, id, nil, "", "")
	}
	if err != nil {
		return
	}
	s.Hub.PublishNode(s.View(ctx, n))
}

// ---- connection ----

const (
	readLimit    = 128 << 20
	pingInterval = 25 * time.Second
	helloTimeout = 15 * time.Second
)

type wsConn struct{ c *websocket.Conn }

func (w *wsConn) Close() {
	_ = w.c.Close(websocket.StatusPolicyViolation, "connection replaced or token revoked")
}

func (w *wsConn) Send(ctx context.Context, m hosts.Message) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	return wsjson.Write(ctx, w.c, m)
}

// Authenticate checks a node bearer token and returns its node.
func (s *Service) Authenticate(ctx context.Context, r *http.Request) (*store.Node, error) {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return nil, ErrUnauthorized
	}
	id := TokenNode(tok)
	if id == "" {
		return nil, ErrUnauthorized
	}
	n, err := s.Store.GetNode(ctx, id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !tokenMatches(tok, n.TokenHash)) {
		return nil, ErrUnauthorized
	}
	return n, err
}

// Serve is GET /api/v1/node/connect: the node's WebSocket. It returns when
// the connection ends.
func (s *Service) Serve(w http.ResponseWriter, r *http.Request) {
	n, err := s.Authenticate(r.Context(), r)
	if err != nil {
		if errors.Is(err, ErrUnauthorized) {
			http.Error(w, err.Error(), http.StatusUnauthorized)
		} else {
			slog.Error("node auth", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}
	if s.Registry.IsOnline(n.ID) {
		// One connection per node: the new one wins and Connect closes the
		// old one (it may be half-dead after a network change).
		slog.Warn("node reconnected while still connected", "node", n.ID)
	}
	// A node is not a browser: no Origin check applies.
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(readLimit)
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	defer cancel()

	hctx, hcancel := context.WithTimeout(ctx, helloTimeout)
	var first hosts.Message
	err = wsjson.Read(hctx, c, &first)
	hcancel()
	if err != nil || first.Op != hosts.OpHello || first.Hello == nil {
		c.Close(websocket.StatusPolicyViolation, "expected hello")
		return
	}
	h := first.Hello
	if h.Agents == nil {
		h.Agents = []string{}
	}
	if _, err := s.Store.NodeSeen(ctx, n.ID, h.Agents, h.Platform, h.Version); err != nil {
		slog.Error("node hello", "node", n.ID, "err", err)
	}
	conn := &wsConn{c: c}
	if err := conn.Send(ctx, hosts.Message{Op: hosts.OpWelcome, Welcome: &hosts.Welcome{NodeID: n.ID, Name: n.Name}}); err != nil {
		return
	}
	slog.Info("node connected", "node", n.ID, "platform", h.Platform, "version", h.Version, "agents", h.Agents, "runners", len(h.Runners))

	remote := s.Registry.Node(n.ID)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer cancel()
		for {
			var m hosts.Message
			if err := wsjson.Read(ctx, c, &m); err != nil {
				return
			}
			remote.Handle(m)
		}
	}()
	// Connect's background part makes calls (re-attach, reconciliation): the
	// read loop above must already run to deliver their results.
	s.Registry.Connect(ctx, n.ID, conn, h)

	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	for {
		select {
		case <-done:
			s.Registry.Disconnect(n.ID, conn)
			slog.Info("node disconnected", "node", n.ID)
			return
		case <-ping.C:
			pctx, pcancel := context.WithTimeout(ctx, 15*time.Second)
			err := c.Ping(pctx)
			pcancel()
			if err != nil {
				cancel()
			}
		}
	}
}
