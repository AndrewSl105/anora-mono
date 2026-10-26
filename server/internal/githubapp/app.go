// Package githubapp talks to GitHub as a GitHub App: per-operation
// installation tokens scoped to one repository, pull requests, comments, and
// the webhook receiver.
package githubapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v92/github"

	"github.com/andrewsl105/fleetpocket/server/internal/api"
	"github.com/andrewsl105/fleetpocket/server/internal/tasks"
)

type App struct {
	appTr  *ghinstallation.AppsTransport
	app    *github.Client
	apiURL string // "" = api.github.com
	// OnError observes API failures (metrics).
	OnError func(op string)
}

// New loads the App's private key. apiURL overrides the API base (tests).
func New(appID int64, privateKeyPath, apiURL string) (*App, error) {
	tr, err := ghinstallation.NewAppsTransportKeyFromFile(http.DefaultTransport, appID, privateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("github app key: %w", err)
	}
	return newApp(tr, apiURL)
}

// NewWithKey is New with the PEM bytes in memory.
func NewWithKey(appID int64, pem []byte, apiURL string) (*App, error) {
	tr, err := ghinstallation.NewAppsTransport(http.DefaultTransport, appID, pem)
	if err != nil {
		return nil, fmt.Errorf("github app key: %w", err)
	}
	return newApp(tr, apiURL)
}

func newApp(tr *ghinstallation.AppsTransport, apiURL string) (*App, error) {
	a := &App{appTr: tr, apiURL: strings.TrimRight(apiURL, "/")}
	if a.apiURL != "" {
		tr.BaseURL = a.apiURL
	}
	c, err := a.newClient(github.WithTransport(tr))
	if err != nil {
		return nil, err
	}
	a.app = c
	return a, nil
}

func (a *App) newClient(opts ...github.ClientOptionsFunc) (*github.Client, error) {
	opts = append(opts, github.WithTimeout(30*time.Second))
	if a.apiURL != "" {
		base := a.apiURL + "/"
		opts = append(opts, github.WithURLs(&base, &base))
	}
	return github.NewClient(opts...)
}

func (a *App) fail(op string, err error) error {
	if err != nil && a.OnError != nil {
		a.OnError(op)
	}
	return err
}

// Token mints an installation token scoped to the single repository
// "owner/name". It is used for one operation and never stored.
func (a *App) Token(ctx context.Context, fullName string, installationID int64) (string, error) {
	_, name, ok := strings.Cut(fullName, "/")
	if !ok {
		return "", fmt.Errorf("bad repository name %q", fullName)
	}
	tok, _, err := a.app.Apps.CreateInstallationToken(ctx, installationID, &github.InstallationTokenOptions{Repositories: []string{name}})
	if err != nil {
		return "", a.fail("token", fmt.Errorf("installation token for %s: %w", fullName, err))
	}
	return tok.GetToken(), nil
}

func (a *App) repoClient(ctx context.Context, fullName string, installationID int64) (*github.Client, string, string, error) {
	tok, err := a.Token(ctx, fullName, installationID)
	if err != nil {
		return nil, "", "", err
	}
	owner, name, _ := strings.Cut(fullName, "/")
	c, err := a.newClient(github.WithAuthToken(tok))
	return c, owner, name, err
}

// ListRepositories returns every repository visible to any installation.
func (a *App) ListRepositories(ctx context.Context) ([]api.GitHubRepo, error) {
	var out []api.GitHubRepo
	opt := &github.ListOptions{PerPage: 100}
	for {
		insts, resp, err := a.app.Apps.ListInstallations(ctx, opt)
		if err != nil {
			return nil, a.fail("list_installations", err)
		}
		for _, inst := range insts {
			tok, _, err := a.app.Apps.CreateInstallationToken(ctx, inst.GetID(), nil)
			if err != nil {
				return nil, a.fail("token", err)
			}
			c, err := a.newClient(github.WithAuthToken(tok.GetToken()))
			if err != nil {
				return nil, err
			}
			ropt := &github.ListOptions{PerPage: 100}
			for {
				repos, rresp, err := c.Apps.ListRepos(ctx, ropt)
				if err != nil {
					return nil, a.fail("list_repos", err)
				}
				for _, r := range repos.Repositories {
					out = append(out, api.GitHubRepo{
						FullName: r.GetFullName(), DefaultBranch: r.GetDefaultBranch(),
						Private: r.GetPrivate(), InstallationID: inst.GetID(),
					})
				}
				if rresp.NextPage == 0 {
					break
				}
				ropt.Page = rresp.NextPage
			}
		}
		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}
	return out, nil
}

type PR struct {
	Number int
	URL    string
}

// FindOpenPR returns the open PR for branch, if any.
func (a *App) FindOpenPR(ctx context.Context, fullName string, installationID int64, branch string) (*PR, error) {
	c, owner, name, err := a.repoClient(ctx, fullName, installationID)
	if err != nil {
		return nil, err
	}
	prs, _, err := c.PullRequests.List(ctx, owner, name, &github.PullRequestListOptions{State: "open", Head: owner + ":" + branch})
	if err != nil {
		return nil, a.fail("list_prs", err)
	}
	if len(prs) == 0 {
		return nil, nil
	}
	return &PR{Number: prs[0].GetNumber(), URL: prs[0].GetHTMLURL()}, nil
}

func (a *App) CreatePR(ctx context.Context, fullName string, installationID int64, head, base, title, body string) (*PR, error) {
	c, owner, name, err := a.repoClient(ctx, fullName, installationID)
	if err != nil {
		return nil, err
	}
	pr, _, err := c.PullRequests.Create(ctx, owner, name, github.CreatePullRequest{
		Title: &title, Head: head, Base: base, Body: &body,
	})
	if err != nil {
		return nil, a.fail("create_pr", fmt.Errorf("create PR: %w", err))
	}
	return &PR{Number: pr.GetNumber(), URL: pr.GetHTMLURL()}, nil
}

func (a *App) Comment(ctx context.Context, fullName string, installationID int64, number int, body string) error {
	c, owner, name, err := a.repoClient(ctx, fullName, installationID)
	if err != nil {
		return err
	}
	_, _, err = c.Issues.CreateComment(ctx, owner, name, number, github.IssueCommentRequest{Body: body})
	return a.fail("comment", err)
}

// MergePR squash-merges PR number with the commit title "<title> (#number)",
// then deletes its head branch. GitHub's refusals (not mergeable, head moved,
// checks required) come back as tasks.ErrNotMergeable with GitHub's message.
// A failed branch deletion is only logged: the PR is merged either way.
func (a *App) MergePR(ctx context.Context, fullName string, installationID int64, number int, branch, title string) error {
	c, owner, name, err := a.repoClient(ctx, fullName, installationID)
	if err != nil {
		return err
	}
	_, _, err = c.PullRequests.Merge(ctx, owner, name, number, "", &github.PullRequestOptions{
		CommitTitle: fmt.Sprintf("%s (#%d)", title, number), MergeMethod: "squash",
	})
	var ge *github.ErrorResponse
	if errors.As(err, &ge) && ge.Response != nil {
		switch ge.Response.StatusCode {
		case http.StatusMethodNotAllowed, http.StatusConflict, http.StatusUnprocessableEntity:
			return fmt.Errorf("%w: %s", tasks.ErrNotMergeable, ge.Message)
		}
	}
	if err != nil {
		return a.fail("merge_pr", fmt.Errorf("merge PR #%d: %w", number, err))
	}
	if branch != "" {
		if _, err := c.Git.DeleteRef(ctx, owner, name, "heads/"+branch); err != nil {
			_ = a.fail("delete_branch", err)
			slog.Warn("delete merged branch", "repo", fullName, "branch", branch, "err", err)
		}
	}
	return nil
}

var ErrNotConfigured = errors.New("GitHub App is not configured")
