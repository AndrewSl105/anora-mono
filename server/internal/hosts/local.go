package hosts

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/andrewsl105/fleetpocket/server/internal/runnermgr"
	"github.com/andrewsl105/fleetpocket/server/internal/workspace"
)

// Local runs tasks with this machine's Docker and workspace folder: the
// server itself, and the node binary on the owner's computer.
type Local struct {
	Docker    *runnermgr.Manager
	Workspace *workspace.Manager
	// AttachmentsDir is the task's attachment folder (bind-mounted at
	// /attachments). The server's is attachments.Files.TaskDir.
	AttachmentsDir func(taskID int64) string
	// WriteAttachments is true on a node: PrepareAttachments copies files
	// it is given into AttachmentsDir. The server's files are already there.
	WriteAttachments bool
	// Scanner is the secrets scanner used by Validate; nil skips the scan.
	Scanner workspace.SecretsScanner
	// AgentEnv returns the credentials a runner of the agent gets.
	AgentEnv func(agent string) map[string]string
	// ExtraHosts are added to every runner's /etc/hosts.
	ExtraHosts []string
	// ImageOverride replaces the runner image the server names (a node with
	// a locally built image).
	ImageOverride string
	// PullMissing pulls a runner image that is not there yet (nodes; the
	// server never pulls runner images).
	PullMissing bool
}

func (l *Local) image(name string) string {
	if l.ImageOverride != "" {
		return l.ImageOverride
	}
	return name
}

var _ Host = (*Local)(nil)

func (l *Local) ID() string { return "" }

func (l *Local) PrepareWorkspace(ctx context.Context, taskID int64, repo workspace.Repo, branch string) error {
	return l.Workspace.Prepare(ctx, taskID, repo, branch)
}

func (l *Local) UnpushedCommits(ctx context.Context, taskID int64, base, branch string) (int, error) {
	return l.Workspace.UnpushedCommits(ctx, taskID, base, branch)
}

func (l *Local) Diff(ctx context.Context, taskID int64, base string, limit int) (string, bool, error) {
	return l.Workspace.Diff(ctx, taskID, base, limit)
}

func (l *Local) DiffStat(ctx context.Context, taskID int64, base string) (string, error) {
	return l.Workspace.DiffStat(ctx, taskID, base)
}

func (l *Local) Validate(ctx context.Context, taskID int64, base string, requireScanner bool) error {
	if requireScanner && l.Scanner == nil {
		return errors.New("the secrets scan is required but gitleaks is not installed on this host")
	}
	return l.Workspace.Validate(ctx, taskID, base, l.Scanner)
}

func (l *Local) Push(ctx context.Context, taskID int64, repo workspace.Repo, branch string) error {
	return l.Workspace.Push(ctx, taskID, repo, branch)
}

func (l *Local) RemoveWorkspace(_ context.Context, taskID int64) error {
	return l.Workspace.Remove(taskID)
}

func (l *Local) EventLog(_ context.Context, taskID int64, afterSeq int64) ([][]byte, error) {
	f, err := os.Open(filepath.Join(l.Workspace.Path(taskID), ".fleetpocket", "events.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return FilterEventLog(f, afterSeq)
}

func (l *Local) PrepareAttachments(_ context.Context, taskID int64, files []AttachmentFile) error {
	dir := l.AttachmentsDir(taskID)
	if !l.WriteAttachments {
		// A missing bind source would be created by Docker as root.
		_, err := os.Stat(dir)
		return err
	}
	return fmt.Errorf("PrepareAttachments on a node takes file contents (use WriteAttachmentFiles)")
}

// ScannerAvailable reports whether Validate can run the secrets scan.
func (l *Local) ScannerAvailable() bool { return l.Scanner != nil }

// WithGitToken returns a copy whose git operations authenticate with token
// (a node: the server sends one per clone or push; it is never stored).
func (l *Local) WithGitToken(token string) *Local {
	c := *l
	ws := *l.Workspace
	ws.Auth = func(context.Context, string, int64) (string, error) { return token, nil }
	c.Workspace = &ws
	return &c
}

// WriteAttachmentFiles stores attachment files received from the server
// (node side). Names are plain file names; anything else is rejected.
func (l *Local) WriteAttachmentFiles(taskID int64, files map[string][]byte) error {
	dir := l.AttachmentsDir(taskID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, data := range files {
		if name == "" || name != filepath.Base(name) || name[0] == '.' {
			return fmt.Errorf("invalid attachment name %q", name)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (l *Local) HasImage(ctx context.Context, image string) error {
	err := l.Docker.HasImage(ctx, l.image(image))
	if err != nil && l.PullMissing {
		if perr := l.Docker.PullImage(ctx, l.image(image)); perr != nil {
			return fmt.Errorf("%w (pull: %v)", err, perr)
		}
		return nil
	}
	return err
}

func (l *Local) EnsureNetwork(ctx context.Context, taskID int64, project string) error {
	_, err := l.Docker.EnsureNetwork(ctx, taskID, project)
	return err
}

func (l *Local) EnsureVolume(ctx context.Context, taskID int64, project string) error {
	_, err := l.Docker.EnsureVolume(ctx, taskID, project)
	return err
}

func (l *Local) EnsureSidecars(ctx context.Context, taskID int64, project string, services []string) (map[string]string, error) {
	return l.Docker.EnsureSidecars(ctx, taskID, project, services)
}

func (l *Local) StartRunner(ctx context.Context, s StartSpec) (*runnermgr.Runner, error) {
	env := map[string]string{}
	for k, v := range s.Env {
		env[k] = v
	}
	// Only the task's agent gets credentials, and only its own.
	if l.AgentEnv != nil {
		for k, v := range l.AgentEnv(s.Agent) {
			env[k] = v
		}
	}
	att := ""
	if s.Attachments {
		att = l.AttachmentsDir(s.TaskID)
	}
	return l.Docker.StartRunner(ctx, runnermgr.RunnerSpec{
		TaskID: s.TaskID, RunID: s.RunID, Project: s.Project, Image: l.image(s.Image),
		Workspace: l.Workspace.Path(s.TaskID), Attachments: att, Env: env,
		Memory: s.Memory, CPUs: s.CPUs, User: s.User, ExtraHosts: l.ExtraHosts,
	})
}

func (l *Local) Attach(ctx context.Context, id string) (*runnermgr.Runner, error) {
	return l.Docker.Attach(ctx, id)
}

func (l *Local) IsRunning(ctx context.Context, id string) (bool, error) {
	return l.Docker.IsRunning(ctx, id)
}

func (l *Local) Wait(ctx context.Context, id string) (int, error) { return l.Docker.Wait(ctx, id) }

func (l *Local) Stop(ctx context.Context, id string, grace time.Duration) error {
	return l.Docker.Stop(ctx, id, grace)
}

func (l *Local) Remove(ctx context.Context, id string) error { return l.Docker.Remove(ctx, id) }

func (l *Local) RemoveAll(ctx context.Context, taskID int64) error {
	return l.Docker.RemoveAll(ctx, taskID)
}

func (l *Local) RemoveSidecars(ctx context.Context, taskID int64) error {
	return l.Docker.RemoveSidecars(ctx, taskID)
}

func (l *Local) RemoveVolume(ctx context.Context, taskID int64) error {
	return l.Docker.RemoveVolume(ctx, taskID)
}

func (l *Local) ListRunners(ctx context.Context) ([]runnermgr.ContainerInfo, error) {
	return l.Docker.ListRunners(ctx)
}

func (l *Local) TaskIDsWithResources(ctx context.Context) (map[int64]bool, error) {
	return l.Docker.TaskIDsWithResources(ctx)
}

func (l *Local) Ping(ctx context.Context) error { return l.Docker.Ping(ctx) }

// readAll reads a server-side attachment file (Remote sends its bytes).
func readAll(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 64<<20))
}
