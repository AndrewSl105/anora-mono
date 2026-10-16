package telegram

import (
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/andrewsl105/anora/server/internal/store"
)

const maxMessage = 4000

var statusIcon = map[string]string{
	"queued": "⏳", "preparing": "⏳", "running": "⏳", "waiting_input": "❓", "paused": "⏸️",
	"pushing": "⏳", "pr_open": "✅", "completed": "✅", "failed": "❌", "cancelled": "🚫",
}

func esc(s string) string { return html.EscapeString(s) }

func cut(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

func clamp(s string) string {
	if utf8.RuneCountInString(s) <= maxMessage {
		return s
	}
	return string([]rune(s)[:maxMessage-1]) + "…"
}

type cardData struct {
	Task *store.Task
	Repo string
	Run  *store.Run
	Now  time.Time
}

func renderCard(c cardData) string {
	t := c.Task
	if c.Now.IsZero() {
		c.Now = time.Now()
	}
	repo := c.Repo
	if repo == "" {
		repo = "repository"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s <b>#%d · %s</b>\n%s / %s\n", statusIcon[t.Status], t.ID, esc(cut(t.Title, 120)), esc(t.ProjectID), esc(repo))
	switch t.Status {
	case "queued":
		b.WriteString("⏳ In queue")
	case "preparing":
		b.WriteString("⏳ Preparing…")
	case "running", "pushing":
		started := t.CreatedAt
		if started.IsZero() {
			started = c.Now
		}
		if c.Run != nil && c.Run.StartedAt != nil {
			started = *c.Run.StartedAt
		}
		label := "Running"
		if t.Status == "pushing" {
			label = "Opening PR"
		}
		fmt.Fprintf(&b, "⏳ %s · %s · %d turns · $%.2f", label, shortDuration(c.Now.Sub(started)), t.Turns, t.CostUSDTotal)
		if t.LastActivity != nil && strings.TrimSpace(*t.LastActivity) != "" {
			if activity := activityLine(*t.LastActivity); activity != "" {
				fmt.Fprintf(&b, "\n%s", esc(activity))
			}
		}
	case "waiting_input":
		b.WriteString("❓ Waiting for your answer")
	case "pr_open":
		created := t.CreatedAt
		if created.IsZero() {
			created = c.Now
		}
		ci := "…"
		if t.CIStatus != nil {
			switch *t.CIStatus {
			case "success":
				ci = "✓"
			case "failure":
				ci = "✗"
			}
		}
		fmt.Fprintf(&b, "✅ PR #%d · CI %s · $%.2f · %s", deref(t.PRNumber), ci, t.CostUSDTotal, shortDuration(c.Now.Sub(created)))
	case "failed":
		b.WriteString("❌ " + esc(humanizeError(deref(t.StatusReason)).Summary))
	case "cancelled":
		b.WriteString("🚫 Cancelled")
	case "completed":
		fmt.Fprintf(&b, "✅ Completed · $%.2f · %s", t.CostUSDTotal, shortDuration(c.Now.Sub(t.CreatedAt)))
	case "paused":
		b.WriteString("⏸️ Paused")
	default:
		b.WriteString(esc(strings.ReplaceAll(t.Status, "_", " ")))
	}
	return clamp(b.String())
}

func renderStatus(t *store.Task, _ string) string { return renderCard(cardData{Task: t}) }

func shortDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	h, m := int(d.Hours()), int(d.Minutes())%60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}

func activityLine(s string) string {
	s = cut(strings.TrimSpace(strings.SplitN(s, "\n", 2)[0]), 180)
	l := strings.ToLower(s)
	for _, status := range []string{"queued", "preparing", "running", "waiting_input", "paused", "pushing", "pr_open", "completed", "failed", "cancelled"} {
		if l == status || strings.HasPrefix(l, status+":") {
			return ""
		}
	}
	switch {
	case strings.HasPrefix(l, "edit "):
		return "✏️ Editing " + strings.TrimSpace(s[5:])
	case strings.HasPrefix(l, "write "):
		return "✏️ Writing " + strings.TrimSpace(s[6:])
	case strings.Contains(l, "test"):
		return "🧪 " + s
	case strings.Contains(l, "read"):
		return "🔎 " + s
	default:
		return "💭 " + s
	}
}

type humanError struct{ Summary, Hint, Raw string }

var whitespace = regexp.MustCompile(`\s+`)

func humanizeError(raw string) humanError {
	raw = strings.TrimSpace(raw)
	l := strings.ToLower(raw)
	h := humanError{Summary: "The task failed", Raw: raw}
	switch {
	case strings.Contains(l, "claude_code_oauth_token") || strings.Contains(l, "invalid") && strings.Contains(l, "oauth") || strings.Contains(l, "authentication") && strings.Contains(l, "claude"):
		h.Summary, h.Hint = "Claude authentication failed", "Check CLAUDE_CODE_OAUTH_TOKEN and retry."
	case strings.Contains(l, "branch") && (strings.Contains(l, "not found") || strings.Contains(l, "remote ref") || strings.Contains(l, "couldn't find")):
		h.Summary, h.Hint = "The base branch was not found", "Check the repository's base branch."
	case strings.Contains(l, "budget") || strings.Contains(l, "max budget"):
		h.Summary, h.Hint = "The budget limit was reached", "Raise the task budget or retry with a smaller request."
	case strings.Contains(l, "turn limit") || strings.Contains(l, "max turns") || strings.Contains(l, "too many turns"):
		h.Summary, h.Hint = "The turn limit was reached", "Raise the turn limit or continue with a focused follow-up."
	case strings.Contains(l, "time limit") || strings.Contains(l, "timed out") || strings.Contains(l, "deadline exceeded"):
		h.Summary, h.Hint = "The time limit was reached", "Raise the time limit or retry."
	case strings.Contains(l, "lost run") || strings.Contains(l, "container") && (strings.Contains(l, "missing") || strings.Contains(l, "not found")):
		h.Summary, h.Hint = "The running agent was lost", "Retry the task to start a fresh run."
	case raw != "":
		h.Summary = cut(whitespace.ReplaceAllString(raw, " "), 140)
	}
	return h
}

func renderErrorNotification(t *store.Task, heading, raw string) string {
	h := humanizeError(raw)
	var b strings.Builder
	fmt.Fprintf(&b, "❌ <b>#%d · %s</b>\n%s", t.ID, esc(cut(heading, 100)), esc(h.Summary))
	if h.Hint != "" {
		fmt.Fprintf(&b, "\n%s", esc(h.Hint))
	}
	if h.Raw != "" {
		fmt.Fprintf(&b, "\n\n<blockquote expandable>%s</blockquote>", esc(cut(h.Raw, 2200)))
	}
	return clamp(b.String())
}

func renderQuestion(t *store.Task, q *store.Question) string {
	var b strings.Builder
	fmt.Fprintf(&b, "❓ <b>#%d · %s</b>\n\n%s", t.ID, esc(cut(t.Title, 80)), esc(cut(q.Text, 3000)))
	if q.Answer != nil && q.AnsweredVia != nil && *q.AnsweredVia != "expired" {
		fmt.Fprintf(&b, "\n\n<b>Answer</b> (%s): %s", esc(viaLabel(*q.AnsweredVia)), esc(cut(*q.Answer, 500)))
	} else {
		b.WriteString("\n\n<i>Choose an option or write your own answer.</i>")
	}
	return clamp(b.String())
}

func viaLabel(via string) string {
	switch via {
	case "telegram":
		return "Telegram"
	case "web":
		return "web"
	default:
		return via
	}
}

func renderTaskDetails(t *store.Task, prompt string, recent []string) string {
	var b strings.Builder
	b.WriteString(renderStatus(t, ""))
	fmt.Fprintf(&b, "\n\n<b>Prompt</b>\n%s", esc(cut(prompt, 800)))
	if t.Branch != nil {
		fmt.Fprintf(&b, "\n\nBranch: <code>%s</code>", esc(*t.Branch))
	}
	if len(recent) > 0 {
		b.WriteString("\n\n<b>Recent</b>")
		for _, r := range recent {
			fmt.Fprintf(&b, "\n• %s", esc(cut(r, 150)))
		}
	}
	return clamp(b.String())
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}
