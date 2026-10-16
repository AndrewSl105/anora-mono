package store_test

import (
	"context"
	"testing"

	"github.com/andrewsl105/anora/server/internal/store"
	"github.com/andrewsl105/anora/server/internal/testutil"
)

func upload(names ...string) *store.Upload {
	u := &store.Upload{}
	for _, n := range names {
		u.Items = append(u.Items, store.NewAttachment{Name: n, Mime: "image/png", Size: 1, Width: 1, Height: 1})
	}
	return u
}

func names(as []*store.Attachment) []string {
	out := []string{}
	for _, a := range as {
		out = append(out, a.Name)
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// startRun marks the pending run started and returns what it receives.
func startRun(t *testing.T, st *store.Store, taskID int64, retry bool) (cur, earlier []string) {
	t.Helper()
	ctx := context.Background()
	run, err := st.PendingRun(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkRunStarting(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	c, e, err := st.AttachmentsForRun(ctx, taskID, run.ID, retry)
	if err != nil {
		t.Fatal(err)
	}
	return names(c), names(e)
}

func TestAttachmentsReachTheRunThatCarriesTheirText(t *testing.T) {
	st := testutil.DB(t)
	ctx := context.Background()
	if _, err := st.CreateProject(ctx, "demo", store.ProjectFields{}); err != nil {
		t.Fatal(err)
	}
	repo, err := st.AddRepository(ctx, store.Repository{ProjectID: "demo", FullName: "o/r", BaseBranch: "dev", InstallationID: 1})
	if err != nil {
		t.Fatal(err)
	}
	task, err := st.CreateTask(ctx, store.Task{ProjectID: "demo", RepositoryID: repo.ID, Title: "t", Prompt: "p", Source: "api"}, upload("create.png"))
	if err != nil {
		t.Fatal(err)
	}
	// A message queued before the initial run starts belongs to the next run.
	if _, _, err := st.QueueMessage(ctx, task.ID, []string{"queued"}, "later", true, upload("queued.png")); err != nil {
		t.Fatal(err)
	}
	if cur, earlier := startRun(t, st, task.ID, false); !eq(cur, []string{"create.png"}) || len(earlier) != 0 {
		t.Fatalf("initial run: %v / %v", cur, earlier)
	}
	// The run ends; the queue drains into a follow-up run with the queued image.
	if _, err := st.SetStatus(ctx, task.ID, nil, "pr_open", "", false); err != nil {
		t.Fatal(err)
	}
	if cur, earlier := startRun(t, st, task.ID, false); !eq(cur, []string{"queued.png"}) || !eq(earlier, []string{"create.png"}) {
		t.Fatalf("drained follow-up: %v / %v", cur, earlier)
	}
	// A message on pr_open starts a run right away with its images.
	if _, err := st.SetStatus(ctx, task.ID, nil, "pr_open", "", false); err != nil {
		t.Fatal(err)
	}
	if _, ev, err := st.RequeueMessage(ctx, task.ID, []string{"pr_open"}, "followup", "now", upload("now.png")); err != nil || ev == nil {
		t.Fatal(err)
	}
	if cur, earlier := startRun(t, st, task.ID, false); !eq(cur, []string{"now.png"}) || !eq(earlier, []string{"create.png", "queued.png"}) {
		t.Fatalf("new-run message: %v / %v", cur, earlier)
	}
	// A retry gets the task's own images again.
	if _, err := st.SetStatus(ctx, task.ID, nil, "failed", "boom", true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Requeue(ctx, task.ID, []string{"failed"}, "retry", "p"); err != nil {
		t.Fatal(err)
	}
	if cur, earlier := startRun(t, st, task.ID, true); !eq(cur, []string{"create.png"}) || !eq(earlier, []string{"queued.png", "now.png"}) {
		t.Fatalf("retry: %v / %v", cur, earlier)
	}
}
