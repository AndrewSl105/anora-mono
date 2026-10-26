package runnermgr

// Mirror of runner/src/protocol.ts. See docs/PROTOCOL.md.

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Runner exit codes.
const (
	ExitDone          = 0
	ExitError         = 1
	ExitCancelled     = 2
	ExitPaused        = 3
	ExitLimitExceeded = 4
)

// Event types.
const (
	EvReady         = "ready"
	EvStarted       = "started"
	EvAssistantText = "assistant_text"
	EvToolUse       = "tool_use"
	EvToolResult    = "tool_result"
	EvQuestion      = "question"
	EvUsage         = "usage"
	EvCommitted     = "committed"
	EvDone          = "done"
	EvPaused        = "paused"
	EvError         = "error"
	EvHeartbeat     = "heartbeat"
)

// ---- Commands (stdin) ----

type Limits struct {
	MaxTurns           int     `json:"max_turns"`
	MaxBudgetUSD       float64 `json:"max_budget_usd"`
	MaxDurationSec     int     `json:"max_duration_sec"`
	QuestionTimeoutSec int     `json:"question_timeout_sec"`
}

type CommitInfo struct {
	Message     string `json:"message"`
	AuthorName  string `json:"author_name"`
	AuthorEmail string `json:"author_email"`
}

type RunCommand struct {
	Type               string     `json:"type"` // "run"
	TaskID             string     `json:"task_id"`
	Prompt             string     `json:"prompt"`
	AppendSystemPrompt string     `json:"append_system_prompt,omitempty"`
	ResumeSessionID    string     `json:"resume_session_id,omitempty"`
	Agent              string     `json:"agent,omitempty"`
	Model              string     `json:"model,omitempty"`
	Limits             Limits     `json:"limits"`
	Commit             CommitInfo `json:"commit"`
}

type AnswerCommand struct {
	Type       string `json:"type"` // "answer"
	QuestionID string `json:"question_id"`
	Text       string `json:"text"`
}

type MessageCommand struct {
	Type string `json:"type"` // "message"
	Text string `json:"text"`
}

type SimpleCommand struct {
	Type string `json:"type"` // "interrupt" | "cancel"
}

func NewRun(r RunCommand) RunCommand { r.Type = "run"; return r }
func NewAnswer(questionID, text string) AnswerCommand {
	return AnswerCommand{Type: "answer", QuestionID: questionID, Text: text}
}
func NewMessage(text string) MessageCommand { return MessageCommand{Type: "message", Text: text} }
func Interrupt() SimpleCommand              { return SimpleCommand{Type: "interrupt"} }
func Cancel() SimpleCommand                 { return SimpleCommand{Type: "cancel"} }

// EncodeCommand returns one JSON line including the trailing newline.
func EncodeCommand(cmd any) ([]byte, error) {
	b, err := json.Marshal(cmd)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// ---- Events (stdout) ----

// Event is one runner event. Raw keeps the full original line for persistence;
// the typed fields cover everything the backend acts on.
type Event struct {
	Type string    `json:"type"`
	TS   time.Time `json:"ts"`
	Seq  int64     `json:"seq"`

	RunnerVersion string   `json:"runner_version,omitempty"`
	SessionID     string   `json:"session_id,omitempty"`
	Model         string   `json:"model,omitempty"`
	Text          string   `json:"text,omitempty"`
	Tool          string   `json:"tool,omitempty"`
	Summary       string   `json:"summary,omitempty"`
	OK            *bool    `json:"ok,omitempty"`
	QuestionID    string   `json:"question_id,omitempty"`
	Options       []string `json:"options,omitempty"`
	CostUSDTotal  float64  `json:"cost_usd_total,omitempty"`
	InputTokens   int64    `json:"input_tokens,omitempty"`
	OutputTokens  int64    `json:"output_tokens,omitempty"`
	Turns         int      `json:"turns,omitempty"`
	SHA           string   `json:"sha,omitempty"`
	FilesChanged  int      `json:"files_changed,omitempty"`
	Insertions    int      `json:"insertions,omitempty"`
	Deletions     int      `json:"deletions,omitempty"`
	NoChanges     bool     `json:"no_changes,omitempty"`
	Reason        string   `json:"reason,omitempty"`
	Code          string   `json:"code,omitempty"`
	Message       string   `json:"message,omitempty"`

	Raw json.RawMessage `json:"-"`
}

var knownEvents = map[string]bool{
	EvReady: true, EvStarted: true, EvAssistantText: true, EvToolUse: true, EvToolResult: true,
	EvQuestion: true, EvUsage: true, EvCommitted: true, EvDone: true, EvPaused: true,
	EvError: true, EvHeartbeat: true,
}

// ParseEvent validates one stdout line.
func ParseEvent(line []byte) (Event, error) {
	var ev Event
	if err := json.Unmarshal(line, &ev); err != nil {
		return ev, fmt.Errorf("invalid event JSON: %w", err)
	}
	if !knownEvents[ev.Type] {
		return ev, fmt.Errorf("unknown event type %q", ev.Type)
	}
	if ev.Seq <= 0 {
		return ev, errors.New("event without seq")
	}
	if ev.TS.IsZero() {
		return ev, errors.New("event without ts")
	}
	switch ev.Type {
	case EvQuestion:
		if ev.QuestionID == "" || ev.Text == "" {
			return ev, errors.New("question needs question_id and text")
		}
	case EvDone, EvStarted:
		if ev.SessionID == "" {
			return ev, fmt.Errorf("%s needs session_id", ev.Type)
		}
	case EvPaused:
		if ev.SessionID == "" || ev.QuestionID == "" {
			return ev, errors.New("paused needs session_id and question_id")
		}
	case EvCommitted:
		if ev.SHA == "" {
			return ev, errors.New("committed needs sha")
		}
	case EvError:
		if ev.Code == "" {
			return ev, errors.New("error needs code")
		}
	}
	ev.Raw = append(json.RawMessage(nil), line...)
	return ev, nil
}
