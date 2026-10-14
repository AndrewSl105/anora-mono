// Package runnermgr starts and supervises runner containers, their task
// networks, sidecars and Claude state volumes through the Docker API.
package runnermgr

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// Labels on every object Anora Runner creates.
const (
	LabelTaskID  = "anora.task_id"
	LabelProject = "anora.project"
	LabelRunID   = "anora.run_id"
	LabelKind    = "anora.kind"
	KindRunner   = "runner"
	KindSidecar  = "sidecar"
	KindNetwork  = "network"
	KindVolume   = "claude-state"
)

func NetworkName(taskID int64) string { return fmt.Sprintf("anora-task-%d", taskID) }
func VolumeName(taskID int64) string  { return fmt.Sprintf("anora-claude-%d", taskID) }
func RunnerName(taskID int64) string  { return fmt.Sprintf("anora-agent-%d", taskID) }
func SidecarName(taskID int64, svc string) string {
	return fmt.Sprintf("anora-svc-%d-%s", taskID, svc)
}

type Manager struct {
	Docker        *client.Client
	SidecarMemory string
}

func NewDockerClient(host string) (*client.Client, error) {
	var opts []client.Opt
	if host != "" {
		opts = append(opts, client.WithHost(host))
	} else {
		opts = append(opts, client.FromEnv)
	}
	return client.New(opts...)
}

func (m *Manager) Ping(ctx context.Context) error {
	_, err := m.Docker.Ping(ctx, client.PingOptions{})
	return err
}

func labels(taskID int64, project, kind string, runID int64) map[string]string {
	l := map[string]string{
		LabelTaskID:  strconv.FormatInt(taskID, 10),
		LabelProject: project,
		LabelKind:    kind,
	}
	if runID != 0 {
		l[LabelRunID] = strconv.FormatInt(runID, 10)
	}
	return l
}

// EnsureNetwork creates the task's bridge network if it does not exist.
func (m *Manager) EnsureNetwork(ctx context.Context, taskID int64, project string) (string, error) {
	name := NetworkName(taskID)
	res, err := m.Docker.NetworkList(ctx, client.NetworkListOptions{Filters: client.Filters{}.Add("name", name)})
	if err != nil {
		return "", err
	}
	for _, n := range res.Items {
		if n.Name == name {
			return name, nil
		}
	}
	_, err = m.Docker.NetworkCreate(ctx, name, client.NetworkCreateOptions{
		Driver: "bridge",
		Labels: labels(taskID, project, KindNetwork, 0),
	})
	return name, err
}

// EnsureVolume creates the Claude state volume so sessions can resume in a new container.
func (m *Manager) EnsureVolume(ctx context.Context, taskID int64, project string) (string, error) {
	name := VolumeName(taskID)
	_, err := m.Docker.VolumeCreate(ctx, client.VolumeCreateOptions{Name: name, Labels: labels(taskID, project, KindVolume, 0)})
	return name, err
}

// ---- sidecars ----

type sidecarSpec struct {
	image  string
	alias  string
	env    func(secret string) []string
	health []string
	url    func(secret string) (string, string)
}

var sidecars = map[string]sidecarSpec{
	"postgres": {
		image: "postgres:17-alpine",
		alias: "postgres",
		env: func(pw string) []string {
			return []string{"POSTGRES_USER=app", "POSTGRES_PASSWORD=" + pw, "POSTGRES_DB=app"}
		},
		health: []string{"CMD-SHELL", "pg_isready -U app -d app"},
		url: func(pw string) (string, string) {
			return "DATABASE_URL", "postgres://app:" + pw + "@postgres:5432/app?sslmode=disable"
		},
	},
	"redis": {
		image:  "redis:7-alpine",
		alias:  "redis",
		env:    func(string) []string { return nil },
		health: []string{"CMD", "redis-cli", "ping"},
		url:    func(string) (string, string) { return "REDIS_URL", "redis://redis:6379/0" },
	},
}

// EnsureSidecars starts the project's services on the task network (reusing
// running ones) and returns the env the runner needs to reach them.
func (m *Manager) EnsureSidecars(ctx context.Context, taskID int64, project string, services []string) (map[string]string, error) {
	env := map[string]string{}
	for _, svc := range services {
		spec, ok := sidecars[svc]
		if !ok {
			return nil, fmt.Errorf("unsupported service %q", svc)
		}
		name := SidecarName(taskID, svc)
		secret, err := m.startSidecar(ctx, taskID, project, name, spec)
		if err != nil {
			return nil, fmt.Errorf("sidecar %s: %w", svc, err)
		}
		k, v := spec.url(secret)
		env[k] = v
	}
	for _, svc := range services {
		if err := m.waitHealthy(ctx, SidecarName(taskID, svc), 90*time.Second); err != nil {
			return nil, fmt.Errorf("sidecar %s: %w", svc, err)
		}
	}
	return env, nil
}

func (m *Manager) startSidecar(ctx context.Context, taskID int64, project, name string, spec sidecarSpec) (string, error) {
	if res, err := m.Docker.ContainerInspect(ctx, name, client.ContainerInspectOptions{}); err == nil {
		secret := envValue(res.Container.Config.Env, "POSTGRES_PASSWORD")
		if !res.Container.State.Running {
			if _, err := m.Docker.ContainerStart(ctx, name, client.ContainerStartOptions{}); err != nil {
				return "", err
			}
		}
		return secret, nil
	}
	secret := randomHex(16)
	memory, err := ParseMemory(m.SidecarMemory)
	if err != nil {
		return "", err
	}
	pids := int64(512)
	netName := NetworkName(taskID)
	_, err = m.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: name,
		Config: &container.Config{
			Image:  spec.image,
			Env:    spec.env(secret),
			Labels: labels(taskID, project, KindSidecar, 0),
			Healthcheck: &container.HealthConfig{
				Test: spec.health, Interval: 2 * time.Second, Timeout: 3 * time.Second, Retries: 30,
			},
		},
		HostConfig: &container.HostConfig{
			NetworkMode: container.NetworkMode(netName),
			Resources:   container.Resources{Memory: memory, PidsLimit: &pids},
			SecurityOpt: []string{"no-new-privileges"},
		},
		NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{
			netName: {Aliases: []string{spec.alias}},
		}},
	})
	if err != nil {
		return "", err
	}
	_, err = m.Docker.ContainerStart(ctx, name, client.ContainerStartOptions{})
	return secret, err
}

func (m *Manager) waitHealthy(ctx context.Context, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		res, err := m.Docker.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
		if err != nil {
			return err
		}
		st := res.Container.State
		if st.Health != nil && st.Health.Status == container.Healthy {
			return nil
		}
		if !st.Running {
			return fmt.Errorf("container exited (code %d)", st.ExitCode)
		}
		if time.Now().After(deadline) {
			return errors.New("not healthy in time")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// ---- runner ----

type RunnerSpec struct {
	TaskID    int64
	RunID     int64
	Project   string
	Image     string
	Workspace string // host path, bind-mounted at /workspace
	Env       map[string]string
	Memory    string
	CPUs      float64
}

// Runner is an attached runner container.
type Runner struct {
	ContainerID string
	Events      <-chan Event // closed when stdout ends
	BadLines    <-chan string

	conn    *client.HijackedResponse
	writeMu sync.Mutex
	done    chan struct{}
}

// Send writes one command line to the runner's stdin.
func (r *Runner) Send(cmd any) error {
	b, err := EncodeCommand(cmd)
	if err != nil {
		return err
	}
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	_, err = r.conn.Conn.Write(b)
	return err
}

// Done is closed when the output stream ends.
func (r *Runner) Done() <-chan struct{} { return r.done }

func (r *Runner) Close() { r.conn.Close() }

// StartRunner creates the runner container, attaches to it, then starts it,
// so no output is lost.
func (m *Manager) StartRunner(ctx context.Context, s RunnerSpec) (*Runner, error) {
	memory, err := ParseMemory(s.Memory)
	if err != nil {
		return nil, err
	}
	pids := int64(1024)
	env := make([]string, 0, len(s.Env))
	for k, v := range s.Env {
		env = append(env, k+"="+v)
	}
	name := RunnerName(s.TaskID)
	// A leftover container from a crashed run would block the name.
	_, _ = m.Docker.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: true})
	created, err := m.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: name,
		Config: &container.Config{
			Image:        s.Image,
			Env:          env,
			Labels:       labels(s.TaskID, s.Project, KindRunner, s.RunID),
			OpenStdin:    true,
			StdinOnce:    false,
			AttachStdin:  true,
			AttachStdout: true,
			AttachStderr: true,
			User:         "1000:1000",
		},
		HostConfig: &container.HostConfig{
			NetworkMode: container.NetworkMode(NetworkName(s.TaskID)),
			Binds: []string{
				s.Workspace + ":/workspace",
				VolumeName(s.TaskID) + ":/home/runner/.claude",
			},
			Resources: container.Resources{
				Memory:    memory,
				NanoCPUs:  int64(s.CPUs * 1e9),
				PidsLimit: &pids,
			},
			CapDrop:     []string{"ALL"},
			SecurityOpt: []string{"no-new-privileges"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create runner: %w", err)
	}
	r, err := m.attach(ctx, created.ID)
	if err != nil {
		_, _ = m.Docker.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true})
		return nil, err
	}
	if _, err := m.Docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		r.Close()
		_, _ = m.Docker.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true})
		return nil, fmt.Errorf("start runner: %w", err)
	}
	return r, nil
}

// Attach re-attaches to a running runner (reconciliation after restart).
func (m *Manager) Attach(ctx context.Context, containerID string) (*Runner, error) {
	return m.attach(ctx, containerID)
}

func (m *Manager) attach(ctx context.Context, id string) (*Runner, error) {
	res, err := m.Docker.ContainerAttach(ctx, id, client.ContainerAttachOptions{Stream: true, Stdin: true, Stdout: true, Stderr: true})
	if err != nil {
		return nil, fmt.Errorf("attach runner: %w", err)
	}
	hr := res.HijackedResponse
	events := make(chan Event, 256)
	bad := make(chan string, 16)
	r := &Runner{ContainerID: id, Events: events, BadLines: bad, conn: &hr, done: make(chan struct{})}

	outR, outW := io.Pipe()
	errR, errW := io.Pipe()
	go func() {
		_, err := stdcopy.StdCopy(outW, errW, hr.Reader)
		outW.CloseWithError(err)
		errW.CloseWithError(err)
	}()
	go func() {
		sc := bufio.NewScanner(errR)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		for sc.Scan() {
			slog.Info("runner stderr", "container", short(id), "line", truncate(sc.Text(), 500))
		}
	}()
	go func() {
		defer close(r.done)
		defer close(events)
		defer close(bad)
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 64*1024), 8<<20)
		for sc.Scan() {
			line := append([]byte(nil), sc.Bytes()...)
			ev, err := ParseEvent(line)
			if err != nil {
				select {
				case bad <- fmt.Sprintf("%v: %s", err, truncate(string(line), 200)):
				default:
				}
				continue
			}
			events <- ev
		}
	}()
	return r, nil
}

// Wait blocks until the container stops and returns its exit code.
func (m *Manager) Wait(ctx context.Context, containerID string) (int, error) {
	res := m.Docker.ContainerWait(ctx, containerID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case w := <-res.Result:
		if w.Error != nil && w.Error.Message != "" {
			return int(w.StatusCode), errors.New(w.Error.Message)
		}
		return int(w.StatusCode), nil
	case err := <-res.Error:
		return -1, err
	}
}

// Stop sends SIGTERM (the runner treats it as cancel) and kills after the grace period.
func (m *Manager) Stop(ctx context.Context, containerID string, grace time.Duration) error {
	secs := int(grace.Seconds())
	_, err := m.Docker.ContainerStop(ctx, containerID, client.ContainerStopOptions{Timeout: &secs})
	return err
}

func (m *Manager) Remove(ctx context.Context, containerID string) error {
	_, err := m.Docker.ContainerRemove(ctx, containerID, client.ContainerRemoveOptions{Force: true})
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "no such container") {
		return nil
	}
	return err
}

// ContainerInfo is what reconciliation needs to know about a runner.
type ContainerInfo struct {
	ID       string
	TaskID   int64
	RunID    int64
	Running  bool
	ExitCode int
}

// ListRunners returns all runner containers, running or exited.
func (m *Manager) ListRunners(ctx context.Context) ([]ContainerInfo, error) {
	res, err := m.Docker.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: client.Filters{}.Add("label", LabelKind+"="+KindRunner)})
	if err != nil {
		return nil, err
	}
	var out []ContainerInfo
	for _, c := range res.Items {
		info := ContainerInfo{ID: c.ID, Running: c.State == container.StateRunning}
		info.TaskID, _ = strconv.ParseInt(c.Labels[LabelTaskID], 10, 64)
		info.RunID, _ = strconv.ParseInt(c.Labels[LabelRunID], 10, 64)
		if !info.Running {
			if in, err := m.Docker.ContainerInspect(ctx, c.ID, client.ContainerInspectOptions{}); err == nil {
				info.ExitCode = in.Container.State.ExitCode
			}
		}
		out = append(out, info)
	}
	return out, nil
}

// RemoveSidecars removes the task's sidecars and network (the volume stays).
func (m *Manager) RemoveSidecars(ctx context.Context, taskID int64) error {
	var errs []error
	res, err := m.Docker.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: client.Filters{}.
		Add("label", LabelTaskID+"="+strconv.FormatInt(taskID, 10)).Add("label", LabelKind+"="+KindSidecar)})
	if err != nil {
		return err
	}
	for _, c := range res.Items {
		errs = append(errs, m.Remove(ctx, c.ID))
	}
	errs = append(errs, m.removeNetwork(ctx, taskID))
	return errors.Join(errs...)
}

// RemoveAll removes every container and the network of a task.
func (m *Manager) RemoveAll(ctx context.Context, taskID int64) error {
	var errs []error
	res, err := m.Docker.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: client.Filters{}.
		Add("label", LabelTaskID+"="+strconv.FormatInt(taskID, 10))})
	if err != nil {
		return err
	}
	for _, c := range res.Items {
		errs = append(errs, m.Remove(ctx, c.ID))
	}
	errs = append(errs, m.removeNetwork(ctx, taskID))
	return errors.Join(errs...)
}

func (m *Manager) RemoveVolume(ctx context.Context, taskID int64) error {
	_, err := m.Docker.VolumeRemove(ctx, VolumeName(taskID), client.VolumeRemoveOptions{Force: true})
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "no such volume") {
		return nil
	}
	return err
}

func (m *Manager) removeNetwork(ctx context.Context, taskID int64) error {
	_, err := m.Docker.NetworkRemove(ctx, NetworkName(taskID), client.NetworkRemoveOptions{})
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "not found") {
		return nil
	}
	return err
}

// TaskIDsWithResources lists task ids that own any labelled container or network.
func (m *Manager) TaskIDsWithResources(ctx context.Context) (map[int64]bool, error) {
	out := map[int64]bool{}
	cs, err := m.Docker.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: client.Filters{}.Add("label", LabelTaskID)})
	if err != nil {
		return nil, err
	}
	for _, c := range cs.Items {
		if id, err := strconv.ParseInt(c.Labels[LabelTaskID], 10, 64); err == nil {
			out[id] = true
		}
	}
	ns, err := m.Docker.NetworkList(ctx, client.NetworkListOptions{Filters: client.Filters{}.Add("label", LabelTaskID)})
	if err != nil {
		return nil, err
	}
	for _, n := range ns.Items {
		if id, err := strconv.ParseInt(n.Labels[LabelTaskID], 10, 64); err == nil {
			out[id] = true
		}
	}
	return out, nil
}

// HasImage reports whether the image exists locally (runners are never pulled).
func (m *Manager) HasImage(ctx context.Context, image string) error {
	_, err := m.Docker.ImageInspect(ctx, image)
	if err != nil {
		return fmt.Errorf("runner image %s is not available locally (docker pull it first): %w", image, err)
	}
	return nil
}

// ParseMemory parses "3g", "512m", "1024k" or plain bytes. Empty means no limit.
func ParseMemory(s string) (int64, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 0, nil
	}
	mult := int64(1)
	switch s[len(s)-1] {
	case 'k':
		mult, s = 1<<10, s[:len(s)-1]
	case 'm':
		mult, s = 1<<20, s[:len(s)-1]
	case 'g':
		mult, s = 1<<30, s[:len(s)-1]
	}
	n, err := strconv.ParseInt(strings.TrimSuffix(s, "b"), 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid memory size %q", s)
	}
	return n * mult, nil
}

func envValue(env []string, key string) string {
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok && k == key {
			return v
		}
	}
	return ""
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
