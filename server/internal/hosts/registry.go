package hosts

import (
	"context"
	"sort"
	"sync"

	"github.com/andrewsl105/fleetpocket/server/internal/runnermgr"
	"github.com/andrewsl105/fleetpocket/server/internal/workspace"
)

// Registry maps a task's placement to its Host: nil / "" is the server,
// anything else a node (a Remote, connected or not).
type Registry struct {
	Local Host
	// Auth and MaxSeq are given to every Remote (see Remote).
	Auth   workspace.Auth
	MaxSeq func(ctx context.Context, taskID int64) (int64, error)
	// OnConnect runs after a node's hello with the runners the server did
	// not know about (the scheduler reconciles them).
	OnConnect func(ctx context.Context, h *Remote, runners []runnermgr.ContainerInfo)
	// OnChange runs when a node connects or disconnects.
	OnChange func(nodeID string, online bool)

	mu    sync.Mutex
	nodes map[string]*Remote
}

// For returns the host of a placement.
func (r *Registry) For(node *string) Host {
	if node == nil || *node == "" || *node == ServerID {
		return r.Local
	}
	return r.Node(*node)
}

// Node returns the Remote for a node id, creating it on first use.
func (r *Registry) Node(id string) *Remote {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.nodes == nil {
		r.nodes = map[string]*Remote{}
	}
	n, ok := r.nodes[id]
	if !ok {
		n = NewRemote(id)
		n.Auth = r.Auth
		n.MaxSeq = r.MaxSeq
		r.nodes[id] = n
	}
	return n
}

// Online lists the connected node ids, sorted.
func (r *Registry) Online() []string {
	r.mu.Lock()
	nodes := make([]*Remote, 0, len(r.nodes))
	for _, n := range r.nodes {
		nodes = append(nodes, n)
	}
	r.mu.Unlock()
	var out []string
	for _, n := range nodes {
		if n.Online() {
			out = append(out, n.ID())
		}
	}
	sort.Strings(out)
	return out
}

// IsOnline reports whether a node is connected.
func (r *Registry) IsOnline(id string) bool {
	r.mu.Lock()
	n, ok := r.nodes[id]
	r.mu.Unlock()
	return ok && n.Online()
}

// Connect installs a node connection after its hello, then (in the
// background) resumes known runners and lets the scheduler reconcile the
// rest. The connection's read loop must already be running. Call Disconnect
// when the connection ends.
func (r *Registry) Connect(ctx context.Context, id string, c Conn, h *Hello) {
	n := r.Node(id)
	n.Install(c, h)
	if r.OnChange != nil {
		r.OnChange(id, true)
	}
	go func() {
		unknown := n.Resume(ctx, h)
		if r.OnConnect != nil && ctx.Err() == nil {
			r.OnConnect(ctx, n, unknown)
		}
	}()
}

func (r *Registry) Disconnect(id string, c Conn) {
	n := r.Node(id)
	n.Disconnected(c)
	if r.OnChange != nil && !n.Online() {
		r.OnChange(id, false)
	}
}

// Kick closes a node's connection (token rotated, node removed).
func (r *Registry) Kick(id string) {
	r.mu.Lock()
	n, ok := r.nodes[id]
	r.mu.Unlock()
	if ok {
		n.Kick()
	}
}
