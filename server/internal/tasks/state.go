// Package tasks holds the task model, the state machine and the task service
// shared by the REST API and the Telegram bot.
package tasks

const (
	Queued       = "queued"
	Preparing    = "preparing"
	Running      = "running"
	WaitingInput = "waiting_input"
	Paused       = "paused"
	Pushing      = "pushing"
	PROpen       = "pr_open"
	Completed    = "completed"
	Failed       = "failed"
	Cancelled    = "cancelled"
)

var transitions = map[string][]string{
	Queued:       {Preparing, Cancelled, Failed},
	Preparing:    {Running, Failed, Cancelled, Queued},
	Running:      {WaitingInput, Paused, Pushing, PROpen, Completed, Failed, Cancelled},
	WaitingInput: {Running, Paused, Pushing, PROpen, Completed, Failed, Cancelled},
	Paused:       {Queued, Cancelled, Failed},
	Pushing:      {PROpen, Completed, Failed, Cancelled},
	PROpen:       {Queued, Completed, Failed, Cancelled},
	Completed:    {Queued},
	Failed:       {Queued},
	Cancelled:    {Queued},
}

// CanTransition reports whether the state machine allows from → to.
func CanTransition(from, to string) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// From lists the states that may move to `to`.
func From(to string) []string {
	var out []string
	for from, tos := range transitions {
		for _, s := range tos {
			if s == to {
				out = append(out, from)
			}
		}
	}
	return out
}

func IsTerminal(s string) bool { return s == Completed || s == Failed || s == Cancelled }

// IsActive states hold a scheduler slot.
func IsActive(s string) bool {
	return s == Preparing || s == Running || s == WaitingInput || s == Pushing
}

// Live states have a runner container attached.
func IsLive(s string) bool { return s == Running || s == WaitingInput }

func Valid(s string) bool { _, ok := transitions[s]; return ok }

// Run kinds.
const (
	RunInitial  = "initial"
	RunResume   = "resume"
	RunFollowup = "followup"
	RunRetry    = "retry"
)

// MessageAction decides what POST /message does in a state (section 7).
type MessageAction int

const (
	MessageReject MessageAction = iota
	MessageInject
	MessageNewRun
)

func MessageActionFor(status string) MessageAction {
	switch status {
	case Running, WaitingInput:
		return MessageInject
	case Paused, PROpen, Completed:
		return MessageNewRun
	}
	return MessageReject
}
