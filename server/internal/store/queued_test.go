package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/andrewsl105/anora/server/internal/store"
	"github.com/andrewsl105/anora/server/internal/testutil"
)

func newTask(t *testing.T, st *store.Store) *store.Task {
	t.Helper()
	ctx := context.Background()
	if _, err := st.CreateProject(ctx, "demo", store.ProjectFields{}); err != nil {
		t.Fatal(err)
	}
	repo, err := st.AddRepository(ctx, store.Repository{ProjectID: "demo", FullName: "o/r", BaseBranch: "dev", InstallationID: 1})
	if err != nil {
		t.Fatal(err)
	}
	task, err := st.CreateTask(ctx, store.Task{ProjectID: "demo", RepositoryID: repo.ID, Title: "t", Prompt: "p", Source: "api"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestQueuedMessageStartsNextRun(t *testing.T) {
	st := testutil.DB(t)
	ctx := context.Background()
	task := newTask(t, st)
	first, _ := st.PendingRun(ctx, task.ID)
	_ = st.MarkRunStarting(ctx, first.ID)
	if _, err := st.SetStatus(ctx, task.ID, nil, "running", "", false); err != nil {
		t.Fatal(err)
	}

	active := []string{"running", "waiting_input"}
	if _, ev, err := st.QueueMessage(ctx, task.ID, active, "one", true, nil); err != nil || ev == nil || !json.Valid(ev.Payload) {
		t.Fatalf("queue: %v %v", ev, err)
	}
	nt, _, err := st.QueueMessage(ctx, task.ID, active, "two", true, nil)
	if err != nil || str(nt.QueuedMessage) != "one\n\ntwo" {
		t.Fatalf("queued %q %v", str(nt.QueuedMessage), err)
	}

	// The run ends: pr_open is recorded, then the queued text becomes a follow-up run.
	nt, err = st.SetStatus(ctx, task.ID, nil, "pr_open", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if nt.Status != "queued" || nt.QueuedMessage != nil || len(nt.StatusEvents) != 2 {
		t.Fatalf("after drain: %s %q %d events", nt.Status, str(nt.QueuedMessage), len(nt.StatusEvents))
	}
	run, err := st.PendingRun(ctx, task.ID)
	if err != nil || run.Kind != "followup" || run.Input != "one\n\ntwo" {
		t.Fatalf("pending run %+v %v", run, err)
	}

	// Not in an active state any more: queueing is a conflict.
	if _, err := st.SetStatus(ctx, task.ID, nil, "completed", "", true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.QueueMessage(ctx, task.ID, active, "late", true, nil); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("queue on completed: %v", err)
	}
}

func TestQueuedMessageResumesPausedTask(t *testing.T) {
	st := testutil.DB(t)
	ctx := context.Background()
	task := newTask(t, st)
	if _, err := st.SetStatus(ctx, task.ID, nil, "waiting_input", "", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.QueueMessage(ctx, task.ID, []string{"waiting_input"}, "answer restated", false, nil); err != nil {
		t.Fatal(err)
	}
	nt, err := st.SetStatus(ctx, task.ID, nil, "paused", "timeout", false)
	if err != nil || nt.Status != "queued" {
		t.Fatalf("paused drain: %v %v", nt, err)
	}
	if run, _ := st.PendingRun(ctx, task.ID); run == nil || run.Kind != "resume" || run.Input != "answer restated" {
		t.Fatalf("resume run %+v", run)
	}
}

func TestQueuedMessageDroppedOnCancel(t *testing.T) {
	st := testutil.DB(t)
	ctx := context.Background()
	task := newTask(t, st)
	if _, _, err := st.QueueMessage(ctx, task.ID, []string{"queued"}, "never mind", true, nil); err != nil {
		t.Fatal(err)
	}
	nt, err := st.SetStatus(ctx, task.ID, nil, "cancelled", "by owner", true)
	if err != nil || nt.Status != "cancelled" || nt.QueuedMessage != nil {
		t.Fatalf("cancel: %+v %v", nt, err)
	}
	last := nt.StatusEvents[len(nt.StatusEvents)-1]
	var p struct{ Text, Reason string }
	_ = json.Unmarshal(last.Payload, &p)
	if last.Type != "message_dropped" || p.Text != "never mind" || p.Reason != "cancelled" {
		t.Fatalf("dropped event %s %s", last.Type, last.Payload)
	}
}
