// Package workspace manages per-task git checkouts on the host path shared
// with runner containers: clone, branch, diff and push. Credentials are
// passed per command (http.extraHeader) and never written to disk.
package workspace

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Auth returns a short-lived token for one git operation on a repository, or
// "" for remotes that need none (local test repos).
type Auth func(ctx context.Context, repoFullName string, installationID int64) (string, error)

type Manager struct {
	Dir string // WORKSPACES_DIR
	// RemoteURL maps a repository to its clone URL. Defaults to GitHub https.
	RemoteURL func(fullName string) string
	Auth      Auth
}

func (m *Manager) Path(taskID int64) string {
	return filepath.Join(m.Dir, strconv.FormatInt(taskID, 10))
}

func (m *Manager) remote(fullName string) string {
	if m.RemoteURL != nil {
		return m.RemoteURL(fullName)
	}
	return "https://github.com/" + fullName + ".git"
}

// Repo identifies what to check out.
type Repo struct {
	FullName       string
	InstallationID int64
	BaseBranch     string
}

// Prepare makes sure the task workspace is a checkout of branch. A new branch
// is created from the base; an existing remote branch is checked out (e.g.
// after the workspace was cleaned up).
func (m *Manager) Prepare(ctx context.Context, taskID int64, repo Repo, branch string) error {
	dir := m.Path(taskID)
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		cur, err := git(ctx, dir, nil, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			return err
		}
		if strings.TrimSpace(cur) != branch {
			if _, err := git(ctx, dir, nil, "checkout", branch); err != nil {
				return err
			}
		}
		return nil
	}
	if err := os.MkdirAll(m.Dir, 0o755); err != nil {
		return err
	}
	_ = os.RemoveAll(dir)
	auth, err := m.authArgs(ctx, repo)
	if err != nil {
		return err
	}
	if _, err := git(ctx, m.Dir, auth, "clone", "--filter=blob:none", "--no-tags", "--branch", repo.BaseBranch,
		m.remote(repo.FullName), dir); err != nil {
		return fmt.Errorf("clone %s: %w", repo.FullName, err)
	}
	// Does the task branch already exist on the remote (a cleaned-up workspace)?
	out, err := git(ctx, dir, auth, "ls-remote", "--heads", "origin", branch)
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) != "" {
		if _, err := git(ctx, dir, auth, "fetch", "origin", branch+":refs/remotes/origin/"+branch); err != nil {
			return err
		}
		_, err = git(ctx, dir, nil, "checkout", "-b", branch, "origin/"+branch)
		return err
	}
	_, err = git(ctx, dir, nil, "checkout", "-b", branch)
	return err
}

// Exists reports whether the workspace checkout is present.
func (m *Manager) Exists(taskID int64) bool {
	_, err := os.Stat(filepath.Join(m.Path(taskID), ".git"))
	return err == nil
}

// AheadOfBase counts commits on HEAD that are not on origin/<base>.
func (m *Manager) AheadOfBase(ctx context.Context, taskID int64, base string) (int, error) {
	out, err := git(ctx, m.Path(taskID), nil, "rev-list", "--count", "origin/"+base+"..HEAD")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}

// UnpushedCommits counts commits not yet on origin/<branch> (all of them when
// the branch was never pushed).
func (m *Manager) UnpushedCommits(ctx context.Context, taskID int64, base, branch string) (int, error) {
	dir := m.Path(taskID)
	ref := "origin/" + branch
	if _, err := git(ctx, dir, nil, "rev-parse", "--verify", "--quiet", ref); err != nil {
		ref = "origin/" + base
	}
	out, err := git(ctx, dir, nil, "rev-list", "--count", ref+"..HEAD")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}

// Diff returns `git diff origin/base...HEAD`, cut at limit bytes.
func (m *Manager) Diff(ctx context.Context, taskID int64, base string, limit int) (string, bool, error) {
	if !m.Exists(taskID) {
		return "", false, errors.New("workspace not present")
	}
	out, err := git(ctx, m.Path(taskID), nil, "diff", "--no-color", "origin/"+base+"...HEAD")
	if err != nil {
		return "", false, err
	}
	if len(out) > limit {
		return out[:limit], true, nil
	}
	return out, false, nil
}

// Push pushes HEAD to origin/<branch> with a fresh token.
func (m *Manager) Push(ctx context.Context, taskID int64, repo Repo, branch string) error {
	auth, err := m.authArgs(ctx, repo)
	if err != nil {
		return err
	}
	_, err = git(ctx, m.Path(taskID), auth, "push", "origin", "HEAD:refs/heads/"+branch)
	if err != nil {
		return err
	}
	// Keep the remote-tracking ref current for UnpushedCommits.
	_, _ = git(ctx, m.Path(taskID), nil, "update-ref", "refs/remotes/origin/"+branch, "HEAD")
	return nil
}

func (m *Manager) Remove(taskID int64) error { return os.RemoveAll(m.Path(taskID)) }

// authArgs returns `-c http.extraHeader=...` for this one command.
func (m *Manager) authArgs(ctx context.Context, repo Repo) ([]string, error) {
	if m.Auth == nil {
		return nil, nil
	}
	token, err := m.Auth(ctx, repo.FullName, repo.InstallationID)
	if err != nil || token == "" {
		return nil, err
	}
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	return []string{"-c", "http.extraHeader=Authorization: Basic " + basic}, nil
}

// git runs one git command. Pre-args (e.g. -c settings) come before the
// subcommand. Credentials in pre-args are never logged.
func git(ctx context.Context, dir string, pre []string, args ...string) (string, error) {
	full := append(append([]string{}, pre...), args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_ASKPASS=/bin/false",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(redact(stderr.String())))
	}
	return stdout.String(), nil
}

var basicRe = regexp.MustCompile(`(?i)(authorization: basic )[A-Za-z0-9+/=]+`)

func redact(s string) string { return basicRe.ReplaceAllString(s, "${1}***") }

// Branch is "agent/<id>-<slug>".
func Branch(taskID int64, title string) string {
	slug := Slug(title)
	if slug == "" {
		return fmt.Sprintf("agent/%d", taskID)
	}
	return fmt.Sprintf("agent/%d-%s", taskID, slug)
}

// Slug turns a title into lowercase ASCII words joined by '-', max 40 chars.
func Slug(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range norm.NFKD.String(title) {
		switch {
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(unicode.ToLower(r))
			dash = false
		case unicode.Is(unicode.Mn, r):
			// drop combining accents
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 40 {
		s = s[:40]
		if i := strings.LastIndexByte(s, '-'); i > 0 {
			s = s[:i]
		}
	}
	return s
}
