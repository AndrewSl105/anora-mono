package maintenance

import (
	"context"
	"testing"
	"time"

	"github.com/andrewsl105/fleetpocket/server/internal/projects"
	"github.com/andrewsl105/fleetpocket/server/internal/store"
	"github.com/andrewsl105/fleetpocket/server/internal/tasks"
	"github.com/andrewsl105/fleetpocket/server/internal/testutil"
)

type fakeDocker struct{ all, sidecars, volumes []int64 }

func (f *fakeDocker) RemoveAll(_ context.Context, id int64) error {
	f.all = append(f.all, id)
	return nil
}
func (f *fakeDocker) RemoveSidecars(_ context.Context, id int64) error {
	f.sidecars = append(f.sidecars, id)
	return nil
}
func (f *fakeDocker) RemoveVolume(_ context.Context, id int64) error {
	f.volumes = append(f.volumes, id)
	return nil
}

type fakeWS struct{ removed []int64 }

func (f *fakeWS) Remove(id int64) error { f.removed = append(f.removed, id); return nil }

// fakeHost combines the two fakes into a maintenance.Host.
type fakeHost struct {
	*fakeDocker
	ws *fakeWS
}

func (h fakeHost) RemoveWorkspace(_ context.Context, id int64) error { return h.ws.Remove(id) }

func hostOf(d *fakeDocker, ws *fakeWS) func(*store.Task) (Host, bool) {
	return func(*store.Task) (Host, bool) { return fakeHost{d, ws}, true }
}

func TestOnce(t *testing.T) {
	st := testutil.DB(t)
	ctx := context.Background()
	svc := &tasks.Service{Store: st}
	name := "P"
	_, _ = (&projects.Service{Store: st}).Create(ctx, projects.CreateInput{ID: "p", ProjectFields: store.ProjectFields{Name: &name}})
	_, _ = (&projects.Service{Store: st}).AddRepository(ctx, "p", projects.RepoInput{FullName: "o/r", InstallationID: 1})

	done, _ := svc.Create(ctx, tasks.CreateInput{ProjectID: "p", Prompt: "old"})
	paused, _ := svc.Create(ctx, tasks.CreateInput{ProjectID: "p", Prompt: "paused"})
	fresh, _ := svc.Create(ctx, tasks.CreateInput{ProjectID: "p", Prompt: "fresh"})
	_, _ = st.SetStatus(ctx, done.ID, nil, tasks.Completed, "", true)
	_, _ = st.SetStatus(ctx, paused.ID, nil, tasks.Paused, "", false)
	_, _ = st.SetStatus(ctx, fresh.ID, nil, tasks.Completed, "", true)
	run, _ := st.PendingRun(ctx, done.ID)
	_ = st.MarkRunStarting(ctx, run.ID)
	_, _ = st.AddRunnerEvent(ctx, done.ID, run.ID, 1, "tool_use", []byte(`{"type":"tool_use"}`))
	// Age the first task by 8 days.
	_, _ = st.Pool.Exec(ctx, `UPDATE tasks SET finished_at = now() - interval '8 days', updated_at = now() - interval '8 days' WHERE id = $1`, done.ID)
	_, _ = st.Pool.Exec(ctx, `UPDATE events SET created_at = now() - interval '31 days' WHERE task_id = $1`, done.ID)
	_, _ = st.Pool.Exec(ctx, `UPDATE tasks SET updated_at = now() - interval '1 hour' WHERE id = $1`, paused.ID)
	_ = st.CreateSession(ctx, "expired", "web", time.Now().Add(-time.Hour))

	d, ws := &fakeDocker{}, &fakeWS{}
	m := &Maintainer{Store: st, Host: hostOf(d, ws), EventRetention: 720 * time.Hour,
		WorkspaceRetention: 7 * 24 * time.Hour, SidecarIdleTTL: 30 * time.Minute}
	m.Once(ctx)

	if len(ws.removed) != 1 || ws.removed[0] != done.ID || len(d.volumes) != 1 {
		t.Fatalf("workspace cleanup: ws=%v vol=%v", ws.removed, d.volumes)
	}
	if len(d.sidecars) != 1 || d.sidecars[0] != paused.ID {
		t.Fatalf("idle sidecars: %v", d.sidecars)
	}
	evs, _ := st.ListEvents(ctx, done.ID, 0, 100)
	for _, e := range evs {
		if e.Seq != nil {
			t.Fatal("old runner events should be deleted")
		}
	}
	if len(evs) == 0 {
		t.Fatal("status events must be kept")
	}
	if _, err := st.TouchSession(ctx, "expired", time.Hour); err == nil {
		t.Fatal("expired session not deleted")
	}
	// Second pass does nothing new.
	m.Once(ctx)
	if len(ws.removed) != 1 {
		t.Fatal("workspace cleaned twice")
	}
}

type fakeAttachments struct {
	swept    []int64
	sweptAll int
}

func (f *fakeAttachments) Sweep(_ context.Context, _ *store.Store, id int64) error {
	f.swept = append(f.swept, id)
	return nil
}
func (f *fakeAttachments) SweepAll(_ context.Context, _ *store.Store) error { f.sweptAll++; return nil }

func TestRetentionRemovesAttachments(t *testing.T) {
	st := testutil.DB(t)
	ctx := context.Background()
	name := "P"
	_, _ = (&projects.Service{Store: st}).Create(ctx, projects.CreateInput{ID: "p", ProjectFields: store.ProjectFields{Name: &name}})
	repo, _ := (&projects.Service{Store: st}).AddRepository(ctx, "p", projects.RepoInput{FullName: "o/r", InstallationID: 1})
	up := &store.Upload{Items: []store.NewAttachment{{Name: "a.png", Mime: "image/png", Size: 1, Width: 1, Height: 1}}}
	old, err := st.CreateTask(ctx, store.Task{ProjectID: "p", RepositoryID: repo.ID, Title: "t", Prompt: "p", Source: "api"}, up)
	if err != nil {
		t.Fatal(err)
	}
	recent, _ := st.CreateTask(ctx, store.Task{ProjectID: "p", RepositoryID: repo.ID, Title: "t", Prompt: "p", Source: "api"}, up)
	for _, id := range []int64{old.ID, recent.ID} {
		_, _ = st.SetStatus(ctx, id, nil, tasks.Failed, "boom", true)
	}
	_, _ = st.Pool.Exec(ctx, `UPDATE tasks SET finished_at = now() - interval '8 days' WHERE id = $1`, old.ID)

	a := &fakeAttachments{}
	m := &Maintainer{Store: st, Host: hostOf(&fakeDocker{}, &fakeWS{}), Attachments: a,
		EventRetention: 720 * time.Hour, WorkspaceRetention: 7 * 24 * time.Hour, SidecarIdleTTL: time.Hour}
	m.Once(ctx)

	if l, _ := st.LiveAttachments(ctx, old.ID); len(l) != 0 {
		t.Fatalf("failed task past retention keeps attachments: %v", l)
	}
	if l, _ := st.LiveAttachments(ctx, recent.ID); len(l) != 1 {
		t.Fatalf("recently failed task lost its attachments (retry needs them): %v", l)
	}
	if len(a.swept) != 1 || a.swept[0] != old.ID || a.sweptAll != 1 {
		t.Fatalf("sweeps: %v all=%d", a.swept, a.sweptAll)
	}
}
