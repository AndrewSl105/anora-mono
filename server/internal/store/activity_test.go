package store_test

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/andrewsl105/anora-runner/internal/store"
	"github.com/andrewsl105/anora-runner/internal/testutil"
)

func TestTurnsAndLastActivity(t *testing.T) {
	st := testutil.DB(t)
	ctx := context.Background()
	if _, err := st.CreateProject(ctx, "demo", store.ProjectFields{}); err != nil {
		t.Fatal(err)
	}
	repo, err := st.AddRepository(ctx, store.Repository{ProjectID: "demo", FullName: "o/r", BaseBranch: "dev", InstallationID: 1})
	if err != nil {
		t.Fatal(err)
	}
	task, err := st.CreateTask(ctx, store.Task{ProjectID: "demo", RepositoryID: repo.ID, Title: "t", Prompt: "p", Source: "api"})
	if err != nil {
		t.Fatal(err)
	}
	if str(task.LastActivity) != "queued" || task.LastActivityAt == nil || task.Turns != 0 {
		t.Fatalf("new task: %q %v %d", str(task.LastActivity), task.LastActivityAt, task.Turns)
	}

	nt, err := st.SetLastActivity(ctx, task.ID, "  Edit src/"+strings.Repeat("é", 200)+"\nsecond line")
	if err != nil {
		t.Fatal(err)
	}
	if a := str(nt.LastActivity); utf8.RuneCountInString(a) != 120 || !strings.HasSuffix(a, "…") || strings.Contains(a, "second") {
		t.Fatalf("activity %q", a)
	}

	nt, err = st.SetStatus(ctx, task.ID, nil, "failed", "boom", true)
	if err != nil || str(nt.LastActivity) != "failed: boom" {
		t.Fatalf("status activity %q %v", str(nt.LastActivity), err)
	}

	// Turns: per run, summed on the task.
	first, _ := st.PendingRun(ctx, task.ID)
	_ = st.MarkRunStarting(ctx, first.ID)
	if _, err := st.Requeue(ctx, task.ID, nil, "followup", "more"); err != nil {
		t.Fatal(err)
	}
	second, _ := st.PendingRun(ctx, task.ID)
	if _, err := st.SetRunTurns(ctx, first.ID, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetRunTurns(ctx, second.ID, 1); err != nil {
		t.Fatal(err)
	}
	nt, err = st.SetRunTurns(ctx, second.ID, 4)
	if err != nil || nt.Turns != 7 {
		t.Fatalf("task turns %d %v", nt.Turns, err)
	}
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
