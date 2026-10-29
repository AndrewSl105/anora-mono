package hosts

import (
	"encoding/json"

	"github.com/andrewsl105/fleetpocket/server/internal/runnermgr"
	"github.com/andrewsl105/fleetpocket/server/internal/workspace"
)

// Wire protocol between the server and fleetpocket-node: one JSON object per
// WebSocket text message (docs/design/remote-nodes.md, section 3.4).

// Message ops.
const (
	OpHello   = "hello"   // node → server, first message
	OpWelcome = "welcome" // server → node, after hello
	OpCall    = "call"    // server → node
	OpResult  = "result"  // node → server
	OpLine    = "line"    // node → server: one runner stdout line
	OpExited  = "exited"  // node → server: a runner's container stopped
)

// Call methods (each mirrors a Host method).
const (
	MPrepareWorkspace = "prepare_workspace"
	MUnpushed         = "unpushed_commits"
	MDiff             = "diff"
	MDiffStat         = "diff_stat"
	MValidate         = "validate"
	MPush             = "push"
	MRemoveWorkspace  = "remove_workspace"
	MEventLog         = "event_log"
	MAttachments      = "attachments"
	MHasImage         = "has_image"
	MEnsureNetwork    = "ensure_network"
	MEnsureVolume     = "ensure_volume"
	MEnsureSidecars   = "ensure_sidecars"
	MStartRunner      = "start_runner"
	MAttach           = "attach"
	MSend             = "send"
	MIsRunning        = "is_running"
	MStop             = "stop"
	MRemove           = "remove"
	MRemoveAll        = "remove_all"
	MRemoveSidecars   = "remove_sidecars"
	MRemoveVolume     = "remove_volume"
	MListRunners      = "list_runners"
	MTaskIDs          = "task_ids"
	MPing             = "ping"
)

// ErrKindInvalidDiff marks a Validate rejection (workspace.ErrInvalidDiff).
const ErrKindInvalidDiff = "invalid_diff"

// Message is every frame in both directions; unused fields are omitted.
type Message struct {
	Op string `json:"op"`

	// call / result
	ID        int64           `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     string          `json:"error,omitempty"`
	ErrorKind string          `json:"error_kind,omitempty"`

	// line / exited
	Container string `json:"container,omitempty"`
	Line      string `json:"line,omitempty"`
	Code      int    `json:"code,omitempty"`

	Hello   *Hello   `json:"hello,omitempty"`
	Welcome *Welcome `json:"welcome,omitempty"`
}

// Hello is the node's first message after connecting.
type Hello struct {
	Version  string   `json:"version"`
	Platform string   `json:"platform"`
	Agents   []string `json:"agents"`
	// Scanner is true when the node can run the secrets scan (gitleaks).
	Scanner bool                      `json:"scanner"`
	Runners []runnermgr.ContainerInfo `json:"runners"`
}

// Welcome answers Hello.
type Welcome struct {
	NodeID string `json:"node_id"`
	Name   string `json:"name"`
}

// RepoParams is a repository plus the short-lived token for this one git
// operation (never stored by the node).
type RepoParams struct {
	Repo  workspace.Repo `json:"repo"`
	Token string         `json:"token,omitempty"`
}

type TaskParams struct {
	TaskID  int64    `json:"task_id"`
	Project string   `json:"project,omitempty"`
	Base    string   `json:"base,omitempty"`
	Branch  string   `json:"branch,omitempty"`
	Limit   int      `json:"limit,omitempty"`
	After   int64    `json:"after_seq,omitempty"`
	Strict  bool     `json:"require_scanner,omitempty"`
	Service []string `json:"services,omitempty"`
	RepoParams
}

type ContainerParams struct {
	Container string `json:"container"`
	Line      string `json:"line,omitempty"`
	GraceSec  int    `json:"grace_sec,omitempty"`
	// AfterSeq (attach): first send the events.jsonl lines after this seq,
	// then the live output.
	AfterSeq int64 `json:"after_seq,omitempty"`
}

type ImageParams struct {
	Image string `json:"image"`
}

type AttachmentParams struct {
	TaskID int64             `json:"task_id"`
	Files  map[string][]byte `json:"files"`
}

type DiffResult struct {
	Diff      string `json:"diff"`
	Truncated bool   `json:"truncated"`
}

type StartResult struct {
	Container string `json:"container"`
}
