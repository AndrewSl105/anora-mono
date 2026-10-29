package hosts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/andrewsl105/fleetpocket/server/internal/runnermgr"
	"github.com/andrewsl105/fleetpocket/server/internal/workspace"
)

// Conn is the node connection a Remote talks over (a WebSocket in
// production, a pipe in tests). Send must be safe for concurrent use.
type Conn interface {
	Send(ctx context.Context, m Message) error
}

// Remote is a node. It outlives connections: runners it supervises survive a
// dropped WebSocket and resume when the node says hello again.
type Remote struct {
	id string
	// Auth mints a short-lived token for one git operation (GitHub App).
	Auth workspace.Auth
	// MaxSeq is the highest persisted event seq of a task, so a re-attach
	// resends only what the server has not stored.
	MaxSeq func(ctx context.Context, taskID int64) (int64, error)

	mu      sync.Mutex
	conn    Conn
	hello   *Hello
	nextID  int64
	calls   map[int64]chan Message
	runners map[string]*remoteRunner
	// pending holds lines of containers whose runner is not registered yet
	// (output that arrives before the start_runner result is handled).
	pending map[string][][]byte
}

type remoteRunner struct {
	runner *runnermgr.Runner
	stream *runnermgr.Stream
	taskID int64
	exited chan struct{}
	code   int
}

var _ Host = (*Remote)(nil)

func NewRemote(id string) *Remote {
	return &Remote{id: id, calls: map[int64]chan Message{}, runners: map[string]*remoteRunner{}, pending: map[string][][]byte{}}
}

func (r *Remote) ID() string { return r.id }

// Online reports whether the node is connected.
func (r *Remote) Online() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.conn != nil
}

// Hello is the node's latest hello (nil before the first connection).
func (r *Remote) Hello() *Hello {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hello
}

// Install sets a new connection after the node's hello.
func (r *Remote) Install(c Conn, h *Hello) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.conn = c
	r.hello = h
}

// Resume runs after Install: it resumes the streams of runners this server
// already supervises and returns the node's other runners, which the
// scheduler reconciles.
func (r *Remote) Resume(ctx context.Context, h *Hello) []runnermgr.ContainerInfo {
	r.mu.Lock()
	known := map[string]*remoteRunner{}
	for id, rr := range r.runners {
		known[id] = rr
	}
	r.mu.Unlock()

	reported := map[string]bool{}
	var unknown []runnermgr.ContainerInfo
	for _, info := range h.Runners {
		reported[info.ID] = true
		rr, ok := known[info.ID]
		if !ok {
			unknown = append(unknown, info)
			continue
		}
		after := r.maxSeq(ctx, rr.taskID)
		if info.Running {
			if err := r.call(ctx, MAttach, ContainerParams{Container: info.ID, AfterSeq: after}, nil); err != nil {
				slog.Warn("node: re-attach runner", "node", r.id, "container", short(info.ID), "err", err)
			}
			continue
		}
		// Exited while disconnected: deliver what the log has, then the exit.
		var lines []string
		if err := r.call(ctx, MEventLog, TaskParams{TaskID: rr.taskID, After: after}, &lines); err == nil {
			for _, l := range lines {
				rr.stream.Line([]byte(l))
			}
		}
		r.exit(info.ID, info.ExitCode)
	}
	for id := range known {
		if !reported[id] {
			slog.Warn("node: runner vanished while disconnected", "node", r.id, "container", short(id))
			r.exit(id, -1)
		}
	}
	return unknown
}

// Disconnected drops the connection; calls in flight fail with ErrOffline.
func (r *Remote) Disconnected(c Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conn != c {
		return
	}
	r.conn = nil
	for id, ch := range r.calls {
		ch <- Message{Op: OpResult, ID: id, Error: ErrOffline.Error(), ErrorKind: "offline"}
		delete(r.calls, id)
	}
}

// Handle processes one message from the node (not hello).
func (r *Remote) Handle(m Message) {
	switch m.Op {
	case OpResult:
		r.mu.Lock()
		ch, ok := r.calls[m.ID]
		delete(r.calls, m.ID)
		r.mu.Unlock()
		if ok {
			ch <- m
		}
	case OpLine:
		r.mu.Lock()
		rr, ok := r.runners[m.Container]
		if !ok {
			if len(r.pending[m.Container]) < 10000 {
				r.pending[m.Container] = append(r.pending[m.Container], []byte(m.Line))
			}
			r.mu.Unlock()
			return
		}
		r.mu.Unlock()
		rr.stream.Line([]byte(m.Line))
	case OpExited:
		r.exit(m.Container, m.Code)
	}
}

func (r *Remote) exit(container string, code int) {
	r.mu.Lock()
	rr, ok := r.runners[container]
	r.mu.Unlock()
	if !ok {
		return
	}
	select {
	case <-rr.exited:
		return
	default:
	}
	rr.code = code
	rr.stream.End()
	close(rr.exited)
}

func (r *Remote) maxSeq(ctx context.Context, taskID int64) int64 {
	if r.MaxSeq == nil {
		return 0
	}
	n, err := r.MaxSeq(ctx, taskID)
	if err != nil {
		return 0
	}
	return n
}

// call sends one call and waits for its result. out may be nil.
func (r *Remote) call(ctx context.Context, method string, params, out any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	r.mu.Lock()
	c := r.conn
	if c == nil {
		r.mu.Unlock()
		return ErrOffline
	}
	r.nextID++
	id := r.nextID
	ch := make(chan Message, 1)
	r.calls[id] = ch
	r.mu.Unlock()

	if err := c.Send(ctx, Message{Op: OpCall, ID: id, Method: method, Params: raw}); err != nil {
		r.mu.Lock()
		delete(r.calls, id)
		r.mu.Unlock()
		return fmt.Errorf("%w: %v", ErrOffline, err)
	}
	select {
	case m := <-ch:
		switch {
		case m.ErrorKind == "offline":
			return ErrOffline
		case m.ErrorKind == ErrKindInvalidDiff:
			return fmt.Errorf("%w: %s", workspace.ErrInvalidDiff, m.Error)
		case m.Error != "":
			return fmt.Errorf("node %s: %s", r.id, m.Error)
		}
		if out != nil && len(m.Result) > 0 {
			return json.Unmarshal(m.Result, out)
		}
		return nil
	case <-ctx.Done():
		r.mu.Lock()
		delete(r.calls, id)
		r.mu.Unlock()
		return ctx.Err()
	}
}

func (r *Remote) repo(ctx context.Context, repo workspace.Repo) (RepoParams, error) {
	p := RepoParams{Repo: repo}
	if r.Auth != nil {
		tok, err := r.Auth(ctx, repo.FullName, repo.InstallationID)
		if err != nil {
			return p, err
		}
		p.Token = tok
	}
	return p, nil
}

// ---- Host ----

func (r *Remote) PrepareWorkspace(ctx context.Context, taskID int64, repo workspace.Repo, branch string) error {
	rp, err := r.repo(ctx, repo)
	if err != nil {
		return err
	}
	return r.call(ctx, MPrepareWorkspace, TaskParams{TaskID: taskID, Branch: branch, RepoParams: rp}, nil)
}

func (r *Remote) UnpushedCommits(ctx context.Context, taskID int64, base, branch string) (int, error) {
	var n int
	err := r.call(ctx, MUnpushed, TaskParams{TaskID: taskID, Base: base, Branch: branch}, &n)
	return n, err
}

func (r *Remote) Diff(ctx context.Context, taskID int64, base string, limit int) (string, bool, error) {
	var res DiffResult
	err := r.call(ctx, MDiff, TaskParams{TaskID: taskID, Base: base, Limit: limit}, &res)
	return res.Diff, res.Truncated, err
}

func (r *Remote) DiffStat(ctx context.Context, taskID int64, base string) (string, error) {
	var s string
	err := r.call(ctx, MDiffStat, TaskParams{TaskID: taskID, Base: base}, &s)
	return s, err
}

func (r *Remote) Validate(ctx context.Context, taskID int64, base string, requireScanner bool) error {
	return r.call(ctx, MValidate, TaskParams{TaskID: taskID, Base: base, Strict: requireScanner}, nil)
}

func (r *Remote) Push(ctx context.Context, taskID int64, repo workspace.Repo, branch string) error {
	rp, err := r.repo(ctx, repo)
	if err != nil {
		return err
	}
	return r.call(ctx, MPush, TaskParams{TaskID: taskID, Branch: branch, RepoParams: rp}, nil)
}

func (r *Remote) RemoveWorkspace(ctx context.Context, taskID int64) error {
	return r.call(ctx, MRemoveWorkspace, TaskParams{TaskID: taskID}, nil)
}

func (r *Remote) EventLog(ctx context.Context, taskID int64, afterSeq int64) ([][]byte, error) {
	var lines []string
	if err := r.call(ctx, MEventLog, TaskParams{TaskID: taskID, After: afterSeq}, &lines); err != nil {
		return nil, err
	}
	out := make([][]byte, len(lines))
	for i, l := range lines {
		out[i] = []byte(l)
	}
	return out, nil
}

func (r *Remote) PrepareAttachments(ctx context.Context, taskID int64, files []AttachmentFile) error {
	p := AttachmentParams{TaskID: taskID, Files: map[string][]byte{}}
	for _, f := range files {
		b, err := readAll(f.Path)
		if err != nil {
			return err
		}
		p.Files[filepath.Base(f.Name)] = b
	}
	return r.call(ctx, MAttachments, p, nil)
}

func (r *Remote) HasImage(ctx context.Context, image string) error {
	return r.call(ctx, MHasImage, ImageParams{Image: image}, nil)
}

func (r *Remote) EnsureNetwork(ctx context.Context, taskID int64, project string) error {
	return r.call(ctx, MEnsureNetwork, TaskParams{TaskID: taskID, Project: project}, nil)
}

func (r *Remote) EnsureVolume(ctx context.Context, taskID int64, project string) error {
	return r.call(ctx, MEnsureVolume, TaskParams{TaskID: taskID, Project: project}, nil)
}

func (r *Remote) EnsureSidecars(ctx context.Context, taskID int64, project string, services []string) (map[string]string, error) {
	env := map[string]string{}
	err := r.call(ctx, MEnsureSidecars, TaskParams{TaskID: taskID, Project: project, Service: services}, &env)
	return env, err
}

// register creates the runner for a container (or returns the existing one).
func (r *Remote) register(container string, taskID int64) *runnermgr.Runner {
	r.mu.Lock()
	if rr, ok := r.runners[container]; ok {
		r.mu.Unlock()
		return rr.runner
	}
	runner, stream := runnermgr.NewStreamRunner(container, func(b []byte) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return r.call(ctx, MSend, ContainerParams{Container: container, Line: string(b)}, nil)
	}, nil)
	rr := &remoteRunner{runner: runner, stream: stream, taskID: taskID, exited: make(chan struct{})}
	r.runners[container] = rr
	lines := r.pending[container]
	delete(r.pending, container)
	r.mu.Unlock()
	for _, l := range lines {
		stream.Line(l)
	}
	return runner
}

func (r *Remote) StartRunner(ctx context.Context, s StartSpec) (*runnermgr.Runner, error) {
	var res StartResult
	if err := r.call(ctx, MStartRunner, s, &res); err != nil {
		return nil, err
	}
	return r.register(res.Container, s.TaskID), nil
}

// Attach registers a runner the node reported (reconciliation) and asks the
// node to stream it, starting with the log lines the server has not stored.
func (r *Remote) Attach(ctx context.Context, container string) (*runnermgr.Runner, error) {
	var taskID int64
	if h := r.Hello(); h != nil {
		for _, c := range h.Runners {
			if c.ID == container {
				taskID = c.TaskID
			}
		}
	}
	runner := r.register(container, taskID)
	if err := r.call(ctx, MAttach, ContainerParams{Container: container, AfterSeq: r.maxSeq(ctx, taskID)}, nil); err != nil {
		return nil, err
	}
	return runner, nil
}

// IsRunning is true until the node reports the exit. A remote runner's event
// stream ends only with its exit, so supervise never needs to re-attach.
func (r *Remote) IsRunning(_ context.Context, container string) (bool, error) {
	r.mu.Lock()
	rr, ok := r.runners[container]
	r.mu.Unlock()
	if !ok {
		return false, nil
	}
	select {
	case <-rr.exited:
		return false, nil
	default:
		return true, nil
	}
}

func (r *Remote) Wait(ctx context.Context, container string) (int, error) {
	r.mu.Lock()
	rr, ok := r.runners[container]
	r.mu.Unlock()
	if !ok {
		return -1, fmt.Errorf("node %s: unknown runner %s", r.id, short(container))
	}
	select {
	case <-rr.exited:
		return rr.code, nil
	case <-ctx.Done():
		return -1, ctx.Err()
	}
}

func (r *Remote) Stop(ctx context.Context, container string, grace time.Duration) error {
	return r.call(ctx, MStop, ContainerParams{Container: container, GraceSec: int(grace.Seconds())}, nil)
}

func (r *Remote) Remove(ctx context.Context, container string) error {
	err := r.call(ctx, MRemove, ContainerParams{Container: container}, nil)
	if err == nil {
		r.forget(container)
	}
	return err
}

func (r *Remote) forget(container string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rr, ok := r.runners[container]; ok {
		select {
		case <-rr.exited:
			delete(r.runners, container)
		default:
		}
	}
}

func (r *Remote) RemoveAll(ctx context.Context, taskID int64) error {
	return r.call(ctx, MRemoveAll, TaskParams{TaskID: taskID}, nil)
}

func (r *Remote) RemoveSidecars(ctx context.Context, taskID int64) error {
	return r.call(ctx, MRemoveSidecars, TaskParams{TaskID: taskID}, nil)
}

func (r *Remote) RemoveVolume(ctx context.Context, taskID int64) error {
	return r.call(ctx, MRemoveVolume, TaskParams{TaskID: taskID}, nil)
}

func (r *Remote) ListRunners(ctx context.Context) ([]runnermgr.ContainerInfo, error) {
	var out []runnermgr.ContainerInfo
	err := r.call(ctx, MListRunners, struct{}{}, &out)
	return out, err
}

func (r *Remote) TaskIDsWithResources(ctx context.Context) (map[int64]bool, error) {
	var ids []int64
	if err := r.call(ctx, MTaskIDs, struct{}{}, &ids); err != nil {
		return nil, err
	}
	out := map[int64]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

func (r *Remote) Ping(ctx context.Context) error { return r.call(ctx, MPing, struct{}{}, nil) }

// IsOffline reports whether err means the node was not reachable.
func IsOffline(err error) bool { return errors.Is(err, ErrOffline) }

func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// TaskLabel is a task id as the label value runner containers carry.
func TaskLabel(id int64) string { return strconv.FormatInt(id, 10) }
