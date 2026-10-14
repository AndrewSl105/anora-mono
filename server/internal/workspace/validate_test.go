package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func commitFile(t *testing.T, dir, name string, content []byte) {
	t.Helper()
	p := filepath.Join(dir, name)
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, content, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.name=a", "-c", "user.email=a@a", "commit", "-qm", "c"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
}

func prepared(t *testing.T) (*Manager, string) {
	bare := newBareRepo(t, "dev")
	m := &Manager{Dir: t.TempDir(), RemoteURL: func(string) string { return bare }}
	if err := m.Prepare(context.Background(), 1, Repo{FullName: "o/r", BaseBranch: "dev"}, "agent/1"); err != nil {
		t.Fatal(err)
	}
	return m, m.Path(1)
}

func TestValidate(t *testing.T) {
	ctx := context.Background()
	noScan := SecretsScanner(nil)

	m, dir := prepared(t)
	commitFile(t, dir, "src/ok.go", []byte("package x\n"))
	if err := m.Validate(ctx, 1, "dev", noScan); err != nil {
		t.Fatalf("clean diff rejected: %v", err)
	}

	m, dir = prepared(t)
	commitFile(t, dir, ".github/workflows/x.yml", []byte("on: push\n"))
	if err := m.Validate(ctx, 1, "dev", noScan); !errors.Is(err, ErrInvalidDiff) || !strings.Contains(err.Error(), ".github") {
		t.Fatalf(".github change: %v", err)
	}

	m, dir = prepared(t)
	commitFile(t, dir, "big.bin", make([]byte, MaxFileBytes+1))
	if err := m.Validate(ctx, 1, "dev", noScan); !errors.Is(err, ErrInvalidDiff) || !strings.Contains(err.Error(), "big.bin") {
		t.Fatalf("big file: %v", err)
	}

	m, dir = prepared(t)
	commitFile(t, dir, "lines.txt", []byte(strings.Repeat("x\n", MaxChangedLines+1)))
	if err := m.Validate(ctx, 1, "dev", noScan); !errors.Is(err, ErrInvalidDiff) || !strings.Contains(err.Error(), "changed lines") {
		t.Fatalf("too many lines: %v", err)
	}

	m, dir = prepared(t)
	commitFile(t, dir, "a.txt", []byte("a\n"))
	fake := func(context.Context, string, string) ([]string, error) {
		return []string{"a.txt:1 generic-api-key"}, nil
	}
	if err := m.Validate(ctx, 1, "dev", fake); !errors.Is(err, ErrInvalidDiff) || !strings.Contains(err.Error(), "generic-api-key") {
		t.Fatalf("secrets: %v", err)
	}
	failing := func(context.Context, string, string) ([]string, error) { return nil, errors.New("not installed") }
	if err := m.Validate(ctx, 1, "dev", failing); !errors.Is(err, ErrInvalidDiff) {
		t.Fatalf("scanner failure must reject (fail closed): %v", err)
	}
}

func TestGitleaks(t *testing.T) {
	if _, err := exec.LookPath("gitleaks"); err != nil {
		t.Skip("gitleaks not installed")
	}
	ctx := context.Background()
	_, dir := prepared(t)
	commitFile(t, dir, "clean.txt", []byte("hello\n"))
	if f, err := Gitleaks("gitleaks")(ctx, dir, "origin/dev"); err != nil || len(f) != 0 {
		t.Fatalf("clean: %v %v", f, err)
	}
	_, dir = prepared(t)
	commitFile(t, dir, "config.py", []byte("AWS_SECRET_ACCESS_KEY = \"wJalrXUtnFEMI/K7MDENG/bPxRfiCYzEXAMPLEKEY\"\naws_access_key_id = \"AKIAZ3MSJV4WWNKWW5FG\"\n"))
	f, err := Gitleaks("gitleaks")(ctx, dir, "origin/dev")
	if err != nil || len(f) == 0 {
		t.Fatalf("leak not found: %v %v", f, err)
	}
	if strings.Contains(strings.Join(f, " "), "AKIA") {
		t.Fatal("finding must not contain the secret")
	}
	if _, err := Gitleaks("no-such-gitleaks")(ctx, dir, "origin/dev"); err == nil {
		t.Fatal("missing binary must error")
	}
}

func TestChangedLines(t *testing.T) {
	if n := ChangedLines("3\t1\ta.go\n-\t-\tbin.png\n10\t0\tb.go\n"); n != 14 {
		t.Fatal(n)
	}
}
