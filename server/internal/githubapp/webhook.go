package githubapp

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/go-github/v92/github"

	"github.com/andrewsl105/anora-runner/internal/events"
	"github.com/andrewsl105/anora-runner/internal/store"
	"github.com/andrewsl105/anora-runner/internal/tasks"
)

// CIFailure describes a failed check for the OnCIFailed hook.
type CIFailure struct {
	Task *store.Task
	Name string
	URL  string
}

// Webhook receives GitHub events at POST /webhooks/github.
type Webhook struct {
	Secret []byte
	Store  *store.Store
	Hub    *events.Hub
	Tasks  *tasks.Service
	// Cleanup removes a task's containers once its PR is merged or closed.
	Cleanup func(ctx context.Context, taskID int64)

	// OnCIFailed is the hook point for future automatic CI fixing (not in v1):
	// it is called once per failed check suite or workflow run of a task's
	// branch. v1 only records the result and notifies.
	OnCIFailed func(ctx context.Context, f CIFailure)
}

func (h *Webhook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	payload, err := github.ValidatePayload(r, h.Secret)
	if err != nil {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	typ := github.WebHookType(r)
	ev, err := github.ParseWebHook(typ, payload)
	if err != nil {
		// Unknown or unparsable event types are acknowledged and ignored.
		w.WriteHeader(http.StatusOK)
		return
	}
	ctx := r.Context()
	switch e := ev.(type) {
	case *github.PullRequestEvent:
		err = h.pullRequest(ctx, e)
	case *github.CheckSuiteEvent:
		cs := e.GetCheckSuite()
		err = h.ci(ctx, e.GetRepo().GetFullName(), cs.GetHeadBranch(), e.GetAction(), cs.GetStatus(), cs.GetConclusion(),
			cs.GetApp().GetName(), "")
	case *github.WorkflowRunEvent:
		wr := e.GetWorkflowRun()
		err = h.ci(ctx, e.GetRepo().GetFullName(), wr.GetHeadBranch(), e.GetAction(), wr.GetStatus(), wr.GetConclusion(),
			wr.GetName(), wr.GetHTMLURL())
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		slog.Error("webhook", "type", typ, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Webhook) pullRequest(ctx context.Context, e *github.PullRequestEvent) error {
	if e.GetAction() != "closed" {
		return nil
	}
	pr := e.GetPullRequest()
	t, err := h.Store.TaskByPR(ctx, e.GetRepo().GetFullName(), pr.GetNumber())
	if err != nil {
		return err
	}
	merged := pr.GetMerged()
	h.event(ctx, t.ID, "pr_closed", map[string]any{"number": pr.GetNumber(), "merged": merged})
	if tasks.IsTerminal(t.Status) {
		return nil
	}
	reason := "PR closed without merging"
	if merged {
		reason = "PR merged"
	}
	if h.Cleanup != nil {
		h.Cleanup(ctx, t.ID)
	}
	if _, err := h.Tasks.SetStatus(ctx, t.ID, tasks.Completed, reason); err != nil && !errors.Is(err, store.ErrConflict) {
		return err
	}
	return nil
}

// CIStatus maps GitHub's action/status/conclusion to pending|success|failure.
// Returns "" for events that do not change the status.
func CIStatus(action, status, conclusion string) string {
	switch action {
	case "requested", "rerequested", "in_progress":
		return "pending"
	case "completed":
	default:
		return ""
	}
	if status != "" && status != "completed" {
		return "pending"
	}
	switch conclusion {
	case "success", "neutral", "skipped":
		return "success"
	case "failure", "timed_out", "cancelled", "action_required", "startup_failure", "stale":
		return "failure"
	}
	return ""
}

func (h *Webhook) ci(ctx context.Context, repo, branch, action, status, conclusion, name, url string) error {
	ci := CIStatus(action, status, conclusion)
	if ci == "" || branch == "" {
		return nil
	}
	t, err := h.Store.TaskByBranch(ctx, repo, branch)
	if err != nil {
		return err
	}
	// A failure sticks until the next push starts new checks (pending).
	if ci == "success" && t.CIStatus != nil && *t.CIStatus == "failure" {
		ci = "failure"
	} else if err := h.Store.SetCIStatus(ctx, t.ID, ci); err != nil {
		return err
	}
	if ci != "pending" || action == "requested" {
		h.event(ctx, t.ID, "ci", map[string]any{"status": CIStatus(action, status, conclusion), "name": name, "url": url})
	}
	if nt, err := h.Store.GetTask(ctx, t.ID); err == nil {
		h.Hub.PublishTask(nt)
	}
	if CIStatus(action, status, conclusion) == "failure" && h.OnCIFailed != nil {
		h.OnCIFailed(ctx, CIFailure{Task: t, Name: name, URL: url})
	}
	return nil
}

func (h *Webhook) event(ctx context.Context, taskID int64, typ string, payload any) {
	ev, err := h.Store.AddBackendEvent(ctx, taskID, nil, typ, payload)
	if err != nil {
		slog.Error("webhook event", "task_id", taskID, "err", err)
		return
	}
	h.Hub.PublishEvent(ev)
}
