package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Limits for a diff before it may be pushed (SPEC section 5).
const (
	MaxFileBytes    = 5 << 20
	MaxChangedLines = 20000
)

// ErrInvalidDiff wraps every validation rejection.
var ErrInvalidDiff = errors.New("diff rejected")

// SecretsScanner scans commits base..HEAD of a checkout and returns findings.
type SecretsScanner func(ctx context.Context, dir, base string) ([]string, error)

// Validate checks the task branch against origin/<base>: no changes under
// .github/, no file over 5 MB, at most 20 000 changed lines, no secrets.
// A nil scanner skips the secrets scan.
func (m *Manager) Validate(ctx context.Context, taskID int64, base string, scan SecretsScanner) error {
	dir := m.Path(taskID)
	rng := "origin/" + base + "...HEAD"

	names, err := git(ctx, dir, nil, "diff", "--name-only", "--no-renames", rng)
	if err != nil {
		return err
	}
	for _, f := range strings.Split(strings.TrimSpace(names), "\n") {
		if f == ".github" || strings.HasPrefix(f, ".github/") {
			return fmt.Errorf("%w: changes under .github/ are not allowed (%s)", ErrInvalidDiff, f)
		}
	}

	present, err := git(ctx, dir, nil, "diff", "--name-only", "--no-renames", "--diff-filter=AM", rng)
	if err != nil {
		return err
	}
	for _, f := range strings.Split(strings.TrimSpace(present), "\n") {
		if f == "" {
			continue
		}
		out, err := git(ctx, dir, nil, "cat-file", "-s", "HEAD:"+f)
		if err != nil {
			return err
		}
		if n, _ := strconv.ParseInt(strings.TrimSpace(out), 10, 64); n > MaxFileBytes {
			return fmt.Errorf("%w: %s is %d bytes (max %d)", ErrInvalidDiff, f, n, MaxFileBytes)
		}
	}

	numstat, err := git(ctx, dir, nil, "diff", "--numstat", "--no-renames", rng)
	if err != nil {
		return err
	}
	if n := ChangedLines(numstat); n > MaxChangedLines {
		return fmt.Errorf("%w: %d changed lines (max %d)", ErrInvalidDiff, n, MaxChangedLines)
	}

	if scan != nil {
		findings, err := scan(ctx, dir, "origin/"+base)
		if err != nil {
			return fmt.Errorf("%w: secrets scan failed: %v", ErrInvalidDiff, err)
		}
		if len(findings) > 0 {
			return fmt.Errorf("%w: secrets scan found %d issue(s): %s", ErrInvalidDiff, len(findings), strings.Join(findings, "; "))
		}
	}
	return nil
}

// ChangedLines sums insertions and deletions from `git diff --numstat`
// (binary files count as 0).
func ChangedLines(numstat string) int {
	total := 0
	for _, line := range strings.Split(numstat, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		a, _ := strconv.Atoi(f[0])
		d, _ := strconv.Atoi(f[1])
		total += a + d
	}
	return total
}

// DiffStat returns `git diff --stat` against the base (for PR bodies).
func (m *Manager) DiffStat(ctx context.Context, taskID int64, base string) (string, error) {
	return git(ctx, m.Path(taskID), nil, "diff", "--stat=100", "origin/"+base+"...HEAD")
}

// Gitleaks scans base..HEAD with the gitleaks binary. Findings are reported
// as "file:line rule" only; secrets are redacted and never logged.
func Gitleaks(binary string) SecretsScanner {
	return func(ctx context.Context, dir, base string) ([]string, error) {
		path, err := exec.LookPath(binary)
		if err != nil {
			return nil, fmt.Errorf("%s not installed", binary)
		}
		cmd := exec.CommandContext(ctx, path, "git", "--no-banner", "--redact", "--exit-code", "3",
			"--log-opts", base+"..HEAD", "--report-format", "json", "--report-path", "-", dir)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err = cmd.Run()
		var exitErr *exec.ExitError
		if err != nil && !(errors.As(err, &exitErr) && exitErr.ExitCode() == 3) {
			return nil, fmt.Errorf("gitleaks: %v: %s", err, strings.TrimSpace(stderr.String()))
		}
		var report []struct {
			File      string `json:"File"`
			StartLine int    `json:"StartLine"`
			RuleID    string `json:"RuleID"`
		}
		if s := bytes.TrimSpace(stdout.Bytes()); len(s) > 0 {
			if err := json.Unmarshal(s, &report); err != nil {
				return nil, fmt.Errorf("gitleaks report: %w", err)
			}
		}
		out := make([]string, 0, len(report))
		for _, f := range report {
			out = append(out, fmt.Sprintf("%s:%d %s", f.File, f.StartLine, f.RuleID))
		}
		return out, nil
	}
}
