package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSlugAndBranch(t *testing.T) {
	cases := map[string]string{
		"Add CSV export to Tables":                             "add-csv-export-to-tables",
		"  Fix: crash!! on save ":                              "fix-crash-on-save",
		"Café déjà vu":                                         "cafe-deja-vu",
		"Додати експорт":                                       "",
		"a very long title that goes on and on and on forever": "a-very-long-title-that-goes-on-and-on",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
	if Branch(7, "Додати") != "agent/7" || Branch(7, "Do it") != "agent/7-do-it" {
		t.Fatal("branch")
	}
}

func TestRedact(t *testing.T) {
	if got := redact("fatal: Authorization: Basic eC1hY2Nlc3M6dG9r failed"); got != "fatal: Authorization: Basic *** failed" {
		t.Fatal(got)
	}
}

// NewBareRepo creates a bare "remote" with one commit on branch base.
func newBareRepo(t *testing.T, base string) string {
	t.Helper()
	dir := t.TempDir()
	bare := filepath.Join(dir, "remote.git")
	work := filepath.Join(dir, "seed")
	run := func(d string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = d
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run(dir, "init", "-q", "--bare", "-b", base, bare)
	run(dir, "init", "-q", "-b", base, work)
	_ = os.WriteFile(filepath.Join(work, "README.md"), []byte("hi\n"), 0o644)
	run(work, "add", "-A")
	run(work, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "init")
	run(work, "push", "-q", bare, base)
	return bare
}

func TestPrepareDiffPush(t *testing.T) {
	ctx := context.Background()
	bare := newBareRepo(t, "dev")
	m := &Manager{Dir: t.TempDir(), RemoteURL: func(string) string { return bare }}
	repo := Repo{FullName: "o/r", BaseBranch: "dev"}
	branch := Branch(1, "Do it")
	if err := m.Prepare(ctx, 1, repo, branch); err != nil {
		t.Fatal(err)
	}
	if n, _ := m.AheadOfBase(ctx, 1, "dev"); n != 0 {
		t.Fatalf("ahead %d", n)
	}
	ws := m.Path(1)
	_ = os.WriteFile(filepath.Join(ws, "new.txt"), []byte("x\n"), 0o644)
	cmd := exec.Command("git", "-c", "user.name=a", "-c", "user.email=a@a", "commit", "-qam", "x")
	cmd.Dir = ws
	exec.Command("git", "-C", ws, "add", "-A").Run()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if n, _ := m.AheadOfBase(ctx, 1, "dev"); n != 1 {
		t.Fatalf("ahead %d", n)
	}
	d, trunc, err := m.Diff(ctx, 1, "dev", 1<<20)
	if err != nil || trunc || !contains(d, "+x") {
		t.Fatalf("diff %q %v %v", d, trunc, err)
	}
	if n, _ := m.UnpushedCommits(ctx, 1, "dev", branch); n != 1 {
		t.Fatalf("unpushed %d", n)
	}
	if err := m.Push(ctx, 1, repo, branch); err != nil {
		t.Fatal(err)
	}
	if n, _ := m.UnpushedCommits(ctx, 1, "dev", branch); n != 0 {
		t.Fatalf("unpushed after push %d", n)
	}
	// Re-prepare after cleanup checks out the pushed branch.
	_ = m.Remove(1)
	if err := m.Prepare(ctx, 1, repo, branch); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ws, "new.txt")); err != nil {
		t.Fatal("pushed branch not restored")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
