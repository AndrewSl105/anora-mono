// Package hosts abstracts where a task's workspace and containers live: the
// server's own Docker (Local) or a node connected over a WebSocket (Remote).
// See docs/design/remote-nodes.md.
package hosts

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/andrewsl105/fleetpocket/server/internal/runnermgr"
	"github.com/andrewsl105/fleetpocket/server/internal/workspace"
)

// ErrOffline is returned by every call to a node that is not connected.
var ErrOffline = errors.New("node is offline")

// ServerID is the placement value clients use for the server itself; tasks
// and projects store it as NULL.
const ServerID = "server"

// AttachmentFile is one attachment of a run: Name is the file name in the
// agent's /attachments folder, Path the file on the server.
type AttachmentFile struct {
	Name string
	Path string
}

// StartSpec describes a runner container. The host adds the workspace and
// attachment mounts and the agent's credentials.
type StartSpec struct {
	TaskID  int64             `json:"task_id"`
	RunID   int64             `json:"run_id"`
	Project string            `json:"project"`
	Image   string            `json:"image"`
	Agent   string            `json:"agent"`
	Env     map[string]string `json:"env"`
	Memory  string            `json:"memory"`
	CPUs    float64           `json:"cpus"`
	User    string            `json:"user"`
	// Attachments mounts the task's attachment folder read-only.
	Attachments bool `json:"attachments"`
}

// Host is everything the scheduler, the publisher, maintenance and the API
// do with a task's workspace and containers.
type Host interface {
	// ID is "" for the server, else the node id.
	ID() string

	PrepareWorkspace(ctx context.Context, taskID int64, repo workspace.Repo, branch string) error
	UnpushedCommits(ctx context.Context, taskID int64, base, branch string) (int, error)
	Diff(ctx context.Context, taskID int64, base string, limit int) (string, bool, error)
	DiffStat(ctx context.Context, taskID int64, base string) (string, error)
	// Validate checks the diff before a push (workspace.Validate with the
	// host's secrets scanner); rejections wrap workspace.ErrInvalidDiff.
	Validate(ctx context.Context, taskID int64, base string, requireScanner bool) error
	Push(ctx context.Context, taskID int64, repo workspace.Repo, branch string) error
	RemoveWorkspace(ctx context.Context, taskID int64) error
	// EventLog returns the lines of <workspace>/.fleetpocket/events.jsonl
	// whose seq is greater than afterSeq (nil when there is no log).
	EventLog(ctx context.Context, taskID int64, afterSeq int64) ([][]byte, error)

	// PrepareAttachments makes the files available for the task's
	// /attachments mount.
	PrepareAttachments(ctx context.Context, taskID int64, files []AttachmentFile) error

	HasImage(ctx context.Context, image string) error
	EnsureNetwork(ctx context.Context, taskID int64, project string) error
	EnsureVolume(ctx context.Context, taskID int64, project string) error
	EnsureSidecars(ctx context.Context, taskID int64, project string, services []string) (map[string]string, error)
	StartRunner(ctx context.Context, spec StartSpec) (*runnermgr.Runner, error)
	Attach(ctx context.Context, containerID string) (*runnermgr.Runner, error)
	IsRunning(ctx context.Context, containerID string) (bool, error)
	Wait(ctx context.Context, containerID string) (int, error)
	Stop(ctx context.Context, containerID string, grace time.Duration) error
	Remove(ctx context.Context, containerID string) error
	RemoveAll(ctx context.Context, taskID int64) error
	RemoveSidecars(ctx context.Context, taskID int64) error
	RemoveVolume(ctx context.Context, taskID int64) error
	ListRunners(ctx context.Context) ([]runnermgr.ContainerInfo, error)
	TaskIDsWithResources(ctx context.Context) (map[int64]bool, error)
	Ping(ctx context.Context) error
}

// FilterEventLog returns the event lines of r with seq > afterSeq.
func FilterEventLog(r io.Reader, afterSeq int64) ([][]byte, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 8<<20)
	var out [][]byte
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var head struct {
			Seq int64 `json:"seq"`
		}
		if json.Unmarshal(line, &head) != nil || head.Seq <= afterSeq {
			continue
		}
		out = append(out, append([]byte(nil), line...))
	}
	return out, sc.Err()
}
