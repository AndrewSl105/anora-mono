package telegram

import (
	"fmt"
	"html"
	"strings"
	"unicode/utf8"

	"github.com/andrewsl105/anora/server/internal/store"
)

// Telegram allows 4096 characters per message; leave room for markup.
const maxMessage = 4000

var statusIcon = map[string]string{
	"queued": "🕓", "preparing": "⚙️", "running": "🏃", "waiting_input": "❓", "paused": "⏸",
	"pushing": "⬆️", "pr_open": "🔀", "completed": "✅", "failed": "❌", "cancelled": "🚫",
}

// esc escapes text for parse_mode HTML.
func esc(s string) string { return html.EscapeString(s) }

// cut shortens s to n runes with an ellipsis.
func cut(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

// clamp keeps a finished HTML message under the limit (callers cut user text
// before escaping, so this is only a safety net).
func clamp(s string) string {
	if utf8.RuneCountInString(s) <= maxMessage {
		return s
	}
	return string([]rune(s)[:maxMessage-1]) + "…"
}

// renderStatus is the one status message per task, edited in place.
func renderStatus(t *store.Task, activity string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s <b>#%d %s</b> · %s\n", statusIcon[t.Status], t.ID, esc(cut(t.Title, 120)), esc(t.ProjectID))
	status := strings.ReplaceAll(t.Status, "_", " ")
	if t.StatusReason != nil && *t.StatusReason != "" {
		status += " — " + cut(*t.StatusReason, 300)
	}
	fmt.Fprintf(&b, "Status: %s\n", esc(status))
	if activity != "" && (t.Status == "running" || t.Status == "waiting_input" || t.Status == "preparing") {
		fmt.Fprintf(&b, "<i>%s</i>\n", esc(cut(activity, 200)))
	}
	if t.PRURL != nil {
		fmt.Fprintf(&b, "PR: <a href=\"%s\">#%d</a>", esc(*t.PRURL), deref(t.PRNumber))
		if t.CIStatus != nil {
			fmt.Fprintf(&b, " · CI: %s", esc(*t.CIStatus))
		}
		b.WriteString("\n")
	}
	if t.CostUSDTotal > 0 {
		fmt.Fprintf(&b, "Cost: $%.2f\n", t.CostUSDTotal)
	}
	return clamp(strings.TrimRight(b.String(), "\n"))
}

func renderQuestion(t *store.Task, q *store.Question) string {
	var b strings.Builder
	fmt.Fprintf(&b, "❓ <b>#%d %s</b>\n\n%s", t.ID, esc(cut(t.Title, 80)), esc(cut(q.Text, 3000)))
	if q.Answer != nil && q.AnsweredVia != nil && *q.AnsweredVia != "expired" {
		fmt.Fprintf(&b, "\n\n<b>Answer</b> (%s): %s", esc(viaLabel(*q.AnsweredVia)), esc(cut(*q.Answer, 500)))
	} else {
		b.WriteString("\n\n<i>Reply to this message with text, or tap an option.</i>")
	}
	return clamp(b.String())
}

func viaLabel(via string) string {
	switch via {
	case "telegram":
		return "Telegram"
	case "web":
		return "web"
	}
	return via
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
