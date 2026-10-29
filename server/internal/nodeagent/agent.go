// Package nodeagent is the node side of docs/design/remote-nodes.md: it
// connects to the server over an outbound WebSocket and runs the server's
// calls against this computer's Docker and workspace folder (hosts.Local).
package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/andrewsl105/fleetpocket/server/internal/hosts"
	"github.com/andrewsl105/fleetpocket/server/internal/runnermgr"
	"github.com/andrewsl105/fleetpocket/server/internal/workspace"
)

// Host is what the node runs calls against: hosts.Local in production.
type Host interface {
	hosts.Host
	// WriteAttachmentFiles stores files sent by the server for /attachments.
	WriteAttachmentFiles(taskID int64, files map[string][]byte) error
	ScannerAvailable() bool
	// Git returns the host to use for one git operation with this token.
	Git(token string) Host
}

// LocalHost adapts hosts.Local to Host.
type LocalHost struct{ *hosts.Local }

func (l LocalHost) Git(token string) Host { return LocalHost{l.Local.WithGitToken(token)} }

// Agent is one fleetpocket-node process.
type Agent struct {
	Server  string // https://fleetpocket.example.com
	Token   string
	Version string
	Local   Host
	// Agents are the coding agents this computer has credentials for.
	Agents []string
	// KeepAwake runs while at least one runner is active (macOS: caffeinate).
	KeepAwake func(active bool)
	// HTTPClient dials the WebSocket; nil uses the default client.
	HTTPClient *http.Client

	mu    sync.Mutex
	conn  *websocket.Conn
	pumps map[string]*pump
	awake bool
}

// Run connects and serves until ctx ends, reconnecting with backoff.
func (a *Agent) Run(ctx context.Context) error {
	a.mu.Lock()
	if a.pumps == nil {
		a.pumps = map[string]*pump{}
	}
	a.mu.Unlock()
	backoff := time.Second
	for {
		start := time.Now()
		err := a.session(ctx)
		if ctx.Err() != nil {
			return nil
		}
		var unauthorized *unauthorizedError
		if errors.As(err, &unauthorized) {
			return err
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		wait := backoff + time.Duration(rand.Int64N(int64(backoff/2)+1))
		slog.Warn("disconnected from server; retrying", "err", err, "in", wait.Round(time.Second))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		backoff = min(backoff*2, time.Minute)
	}
}

type unauthorizedError struct{ msg string }

func (e *unauthorizedError) Error() string { return e.msg }

func (a *Agent) connectURL() (string, error) {
	u, err := url.Parse(strings.TrimRight(a.Server, "/"))
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("server URL must start with https:// (got %q)", a.Server)
	}
	u.Path += "/api/v1/node/connect"
	return u.String(), nil
}

// session runs one connection.
func (a *Agent) session(ctx context.Context) error {
	target, err := a.connectURL()
	if err != nil {
		return err
	}
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	c, resp, err := websocket.Dial(dctx, target, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + a.Token}},
		HTTPClient: a.HTTPClient,
	})
	cancel()
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return &unauthorizedError{"the server rejected the node token (rotate it in Settings → Computers)"}
		}
		return err
	}
	defer c.CloseNow()
	c.SetReadLimit(64 << 20)
	ctx, cancel = context.WithCancel(ctx)
	defer cancel()

	runners, err := a.Local.ListRunners(ctx)
	if err != nil {
		return fmt.Errorf("docker: %w", err)
	}
	hello := hosts.Hello{
		Version: a.Version, Platform: runtime.GOOS + "/" + runtime.GOARCH,
		Agents: a.Agents, Scanner: a.Local.ScannerAvailable(), Runners: runners,
	}
	if hello.Agents == nil {
		hello.Agents = []string{}
	}
	if err := wsjson.Write(ctx, c, hosts.Message{Op: hosts.OpHello, Hello: &hello}); err != nil {
		return err
	}
	var w hosts.Message
	if err := wsjson.Read(ctx, c, &w); err != nil {
		return err
	}
	if w.Op != hosts.OpWelcome || w.Welcome == nil {
		return fmt.Errorf("unexpected first message %q", w.Op)
	}
	slog.Info("connected", "server", a.Server, "node", w.Welcome.NodeID, "name", w.Welcome.Name, "runners", len(runners))

	a.mu.Lock()
	a.conn = c
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		if a.conn == c {
			a.conn = nil
		}
		// Streams stop sending until the server re-attaches them.
		for _, p := range a.pumps {
			p.pause()
		}
		a.mu.Unlock()
	}()

	for {
		var m hosts.Message
		if err := wsjson.Read(ctx, c, &m); err != nil {
			return err
		}
		if m.Op != hosts.OpCall {
			continue
		}
		go a.handle(ctx, c, m)
	}
}

// send writes to the current connection; false when there is none.
func (a *Agent) send(m hosts.Message) bool {
	a.mu.Lock()
	c := a.conn
	a.mu.Unlock()
	if c == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return wsjson.Write(ctx, c, m) == nil
}

func (a *Agent) handle(ctx context.Context, c *websocket.Conn, m hosts.Message) {
	res, err := a.dispatch(ctx, m.Method, m.Params)
	out := hosts.Message{Op: hosts.OpResult, ID: m.ID}
	if err != nil {
		out.Error = err.Error()
		if errors.Is(err, workspace.ErrInvalidDiff) {
			out.ErrorKind = hosts.ErrKindInvalidDiff
		}
		if m.Method != hosts.MSend {
			slog.Warn("call failed", "method", m.Method, "err", err)
		}
	} else if res != nil {
		b, merr := json.Marshal(res)
		if merr != nil {
			out.Error = merr.Error()
		} else {
			out.Result = b
		}
	}
	wctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	_ = wsjson.Write(wctx, c, out)
}

func (a *Agent) dispatch(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	l := a.Local
	var tp hosts.TaskParams
	var cp hosts.ContainerParams
	switch method {
	case hosts.MPrepareWorkspace, hosts.MUnpushed, hosts.MDiff, hosts.MDiffStat, hosts.MValidate, hosts.MPush,
		hosts.MRemoveWorkspace, hosts.MEventLog, hosts.MEnsureNetwork, hosts.MEnsureVolume, hosts.MEnsureSidecars,
		hosts.MRemoveAll, hosts.MRemoveSidecars, hosts.MRemoveVolume:
		if err := json.Unmarshal(raw, &tp); err != nil {
			return nil, err
		}
	case hosts.MAttach, hosts.MSend, hosts.MStop, hosts.MRemove:
		if err := json.Unmarshal(raw, &cp); err != nil {
			return nil, err
		}
	}
	switch method {
	case hosts.MPing:
		return nil, l.Ping(ctx)
	case hosts.MPrepareWorkspace:
		return nil, a.Local.Git(tp.Token).PrepareWorkspace(ctx, tp.TaskID, tp.Repo, tp.Branch)
	case hosts.MUnpushed:
		return l.UnpushedCommits(ctx, tp.TaskID, tp.Base, tp.Branch)
	case hosts.MDiff:
		d, trunc, err := l.Diff(ctx, tp.TaskID, tp.Base, tp.Limit)
		return hosts.DiffResult{Diff: d, Truncated: trunc}, err
	case hosts.MDiffStat:
		return l.DiffStat(ctx, tp.TaskID, tp.Base)
	case hosts.MValidate:
		return nil, l.Validate(ctx, tp.TaskID, tp.Base, tp.Strict)
	case hosts.MPush:
		return nil, a.Local.Git(tp.Token).Push(ctx, tp.TaskID, tp.Repo, tp.Branch)
	case hosts.MRemoveWorkspace:
		return nil, l.RemoveWorkspace(ctx, tp.TaskID)
	case hosts.MEventLog:
		lines, err := l.EventLog(ctx, tp.TaskID, tp.After)
		out := make([]string, len(lines))
		for i, b := range lines {
			out[i] = string(b)
		}
		return out, err
	case hosts.MAttachments:
		var p hosts.AttachmentParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return nil, l.WriteAttachmentFiles(p.TaskID, p.Files)
	case hosts.MHasImage:
		var p hosts.ImageParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return nil, l.HasImage(ctx, p.Image)
	case hosts.MEnsureNetwork:
		return nil, l.EnsureNetwork(ctx, tp.TaskID, tp.Project)
	case hosts.MEnsureVolume:
		return nil, l.EnsureVolume(ctx, tp.TaskID, tp.Project)
	case hosts.MEnsureSidecars:
		return l.EnsureSidecars(ctx, tp.TaskID, tp.Project, tp.Service)
	case hosts.MStartRunner:
		var s hosts.StartSpec
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		r, err := l.StartRunner(ctx, s)
		if err != nil {
			return nil, err
		}
		a.startPump(r, s.TaskID, true)
		return hosts.StartResult{Container: r.ContainerID}, nil
	case hosts.MAttach:
		return nil, a.attach(ctx, cp.Container, cp.AfterSeq)
	case hosts.MSend:
		a.mu.Lock()
		p, ok := a.pumps[cp.Container]
		a.mu.Unlock()
		if !ok {
			return nil, fmt.Errorf("runner %s is not attached", short(cp.Container))
		}
		return nil, p.runner.WriteLine([]byte(cp.Line))
	case hosts.MStop:
		return nil, l.Stop(ctx, cp.Container, time.Duration(cp.GraceSec)*time.Second)
	case hosts.MRemove:
		return nil, l.Remove(ctx, cp.Container)
	case hosts.MRemoveAll:
		return nil, l.RemoveAll(ctx, tp.TaskID)
	case hosts.MRemoveSidecars:
		return nil, l.RemoveSidecars(ctx, tp.TaskID)
	case hosts.MRemoveVolume:
		return nil, l.RemoveVolume(ctx, tp.TaskID)
	case hosts.MListRunners:
		return l.ListRunners(ctx)
	case hosts.MTaskIDs:
		ids, err := l.TaskIDsWithResources(ctx)
		out := []int64{}
		for id := range ids {
			out = append(out, id)
		}
		return out, err
	}
	return nil, fmt.Errorf("unknown method %q", method)
}

// ---- runner output ----

// pump forwards one runner's stdout lines to the server. While paused (no
// connection, or a re-attach in progress) lines are buffered; on re-attach
// the event log fills what the server missed.
type pump struct {
	a      *Agent
	runner *runnermgr.Runner
	taskID int64

	mu      sync.Mutex
	live    bool
	buf     [][]byte
	lastSeq int64
}

func (p *pump) pause() {
	p.mu.Lock()
	p.live = false
	p.buf = nil
	p.mu.Unlock()
}

func seqOf(line []byte) int64 {
	var h struct {
		Seq int64 `json:"seq"`
	}
	_ = json.Unmarshal(line, &h)
	return h.Seq
}

func (p *pump) line(raw []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.live {
		if len(p.buf) < 20000 {
			p.buf = append(p.buf, raw)
		}
		return
	}
	if seq := seqOf(raw); seq > p.lastSeq {
		p.lastSeq = seq
	}
	if !p.a.send(hosts.Message{Op: hosts.OpLine, Container: p.runner.ContainerID, Line: string(raw)}) {
		p.live = false
	}
}

// resume sends the log lines after afterSeq, then the buffered lines the log
// did not cover, then goes live.
func (p *pump) resume(ctx context.Context, afterSeq int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	lines, err := p.a.Local.EventLog(ctx, p.taskID, afterSeq)
	if err != nil {
		return err
	}
	last := afterSeq
	for _, l := range lines {
		if !p.a.send(hosts.Message{Op: hosts.OpLine, Container: p.runner.ContainerID, Line: string(l)}) {
			return errors.New("connection lost during re-attach")
		}
		last = max(last, seqOf(l))
	}
	for _, l := range p.buf {
		if seq := seqOf(l); seq > last || seq == 0 {
			if !p.a.send(hosts.Message{Op: hosts.OpLine, Container: p.runner.ContainerID, Line: string(l)}) {
				return errors.New("connection lost during re-attach")
			}
			last = max(last, seq)
		}
	}
	p.buf = nil
	p.lastSeq = last
	p.live = true
	return nil
}

func (a *Agent) startPump(r *runnermgr.Runner, taskID int64, live bool) *pump {
	p := &pump{a: a, runner: r, taskID: taskID, live: live}
	a.mu.Lock()
	a.pumps[r.ContainerID] = p
	a.keepAwakeLocked()
	a.mu.Unlock()
	go func() {
		for ev := range r.Events {
			p.line(ev.Raw)
		}
		// stdout ended: report the exit once the container has stopped.
		code, err := a.Local.Wait(context.Background(), r.ContainerID)
		if err != nil {
			code = -1
		}
		r.Close()
		a.mu.Lock()
		delete(a.pumps, r.ContainerID)
		a.keepAwakeLocked()
		a.mu.Unlock()
		slog.Info("runner exited", "container", short(r.ContainerID), "task_id", taskID, "code", code)
		// If this is lost, the next hello reports the exited container.
		a.send(hosts.Message{Op: hosts.OpExited, Container: r.ContainerID, Code: code})
	}()
	return p
}

// attach (re)connects a runner's stream to the server.
func (a *Agent) attach(ctx context.Context, container string, afterSeq int64) error {
	a.mu.Lock()
	p, ok := a.pumps[container]
	a.mu.Unlock()
	if !ok {
		var taskID int64
		runners, err := a.Local.ListRunners(ctx)
		if err != nil {
			return err
		}
		for _, c := range runners {
			if c.ID == container {
				taskID = c.TaskID
			}
		}
		if taskID == 0 {
			return fmt.Errorf("no runner %s", short(container))
		}
		r, err := a.Local.Attach(ctx, container)
		if err != nil {
			return err
		}
		p = a.startPump(r, taskID, false)
	} else {
		p.pause()
	}
	return p.resume(ctx, afterSeq)
}

func (a *Agent) keepAwakeLocked() {
	active := len(a.pumps) > 0
	if a.KeepAwake != nil && active != a.awake {
		a.awake = active
		a.KeepAwake(active)
	}
}

func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
