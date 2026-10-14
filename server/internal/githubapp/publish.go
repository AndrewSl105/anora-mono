package githubapp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/andrewsl105/anora/server/internal/scheduler"
	"github.com/andrewsl105/anora/server/internal/workspace"
)

// Publisher implements scheduler.Publisher: validate, push, open or update the PR.
type Publisher struct {
	App       *App
	Workspace *workspace.Manager
	Scanner   workspace.SecretsScanner
	// TaskURL links back to the task in the web app.
	TaskURL func(taskID int64) string
}

func (p *Publisher) Publish(ctx context.Context, in scheduler.PublishInput) (*scheduler.PublishResult, error) {
	t, repo := in.Task, in.Repo
	if err := p.Workspace.Validate(ctx, t.ID, repo.BaseBranch, p.Scanner); err != nil {
		if errors.Is(err, workspace.ErrInvalidDiff) {
			return nil, fmt.Errorf("%w: %v", scheduler.ErrRejected, err)
		}
		return nil, err
	}
	wsRepo := workspace.Repo{FullName: repo.FullName, InstallationID: repo.InstallationID, BaseBranch: repo.BaseBranch}
	if err := p.Workspace.Push(ctx, t.ID, wsRepo, in.Branch); err != nil {
		return nil, fmt.Errorf("push: %w", err)
	}
	stat, _ := p.Workspace.DiffStat(ctx, t.ID, repo.BaseBranch)

	number := 0
	if t.PRNumber != nil {
		number = *t.PRNumber
	} else if pr, err := p.App.FindOpenPR(ctx, repo.FullName, repo.InstallationID, in.Branch); err != nil {
		return nil, err
	} else if pr != nil {
		number = pr.Number // created before a restart
	}
	if number == 0 {
		pr, err := p.App.CreatePR(ctx, repo.FullName, repo.InstallationID, in.Branch, repo.BaseBranch, t.Title, p.body(in, stat))
		if err != nil {
			return nil, err
		}
		return &scheduler.PublishResult{PRNumber: pr.Number, PRURL: pr.URL, Created: true}, nil
	}
	if err := p.App.Comment(ctx, repo.FullName, repo.InstallationID, number, p.comment(in, stat)); err != nil {
		return nil, err
	}
	url := ""
	if t.PRURL != nil {
		url = *t.PRURL
	} else {
		url = fmt.Sprintf("https://github.com/%s/pull/%d", repo.FullName, number)
	}
	return &scheduler.PublishResult{PRNumber: number, PRURL: url}, nil
}

func (p *Publisher) body(in scheduler.PublishInput, stat string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### Task\n\n%s\n\n", quote(in.Task.Prompt))
	if in.Summary != "" {
		fmt.Fprintf(&b, "### Summary\n\n%s\n\n", in.Summary)
	}
	if in.Limit != "" {
		fmt.Fprintf(&b, "> [!WARNING]\n> The run stopped at a limit: %s. The changes may be incomplete.\n\n", in.Limit)
	}
	if stat = strings.TrimSpace(stat); stat != "" {
		fmt.Fprintf(&b, "### Files changed\n\n```\n%s\n```\n\n", stat)
	}
	fmt.Fprintf(&b, "Cost: $%.2f · Turns: %d", in.CostUSD, in.Turns)
	if p.TaskURL != nil {
		if u := p.TaskURL(in.Task.ID); u != "" {
			fmt.Fprintf(&b, " · [Task #%d](%s)", in.Task.ID, u)
		}
	}
	b.WriteString("\n\n<sub>Opened by Anora Runner.</sub>\n")
	return b.String()
}

func (p *Publisher) comment(in scheduler.PublishInput, stat string) string {
	var b strings.Builder
	b.WriteString("### New run pushed\n\n")
	if in.Summary != "" {
		fmt.Fprintf(&b, "%s\n\n", in.Summary)
	}
	if in.Limit != "" {
		fmt.Fprintf(&b, "Stopped at a limit: %s.\n\n", in.Limit)
	}
	if stat = strings.TrimSpace(stat); stat != "" {
		fmt.Fprintf(&b, "<details><summary>Files changed (whole PR)</summary>\n\n```\n%s\n```\n</details>\n\n", stat)
	}
	fmt.Fprintf(&b, "Task cost so far: $%.2f", in.CostUSD)
	return b.String()
}

func quote(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i, l := range lines {
		lines[i] = "> " + l
	}
	return strings.Join(lines, "\n")
}
