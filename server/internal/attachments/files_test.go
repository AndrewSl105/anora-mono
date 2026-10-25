package attachments_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/andrewsl105/fleetpocket/server/internal/attachments"
	"github.com/andrewsl105/fleetpocket/server/internal/events"
	"github.com/andrewsl105/fleetpocket/server/internal/store"
	"github.com/andrewsl105/fleetpocket/server/internal/testutil"
)

func exists(t *testing.T, p string) bool {
	t.Helper()
	_, err := os.Stat(p)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

func TestSweepRemovesDeletedStrayAndOrphanedFiles(t *testing.T) {
	st := testutil.DB(t)
	ctx := context.Background()
	if _, err := st.CreateProject(ctx, "demo", store.ProjectFields{}); err != nil {
		t.Fatal(err)
	}
	repo, err := st.AddRepository(ctx, store.Repository{ProjectID: "demo", FullName: "o/r", BaseBranch: "dev", InstallationID: 1})
	if err != nil {
		t.Fatal(err)
	}
	var taskID int64
	if err := st.Pool.QueryRow(ctx, `INSERT INTO tasks (project_id, repository_id, title, prompt, status, source)
		VALUES ('demo', $1, 't', 'p', 'queued', 'api') RETURNING id`, repo.ID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	insert := func(deleted bool) *store.Attachment {
		var id int64
		if err := st.Pool.QueryRow(ctx, `INSERT INTO attachments (task_id, original_name, mime, size_bytes, width, height, deleted_at)
			VALUES ($1, 'a.png', 'image/png', 3, 1, 1, CASE WHEN $2 THEN now() END) RETURNING id`, taskID, deleted).Scan(&id); err != nil {
			t.Fatal(err)
		}
		a, err := st.GetAttachment(ctx, taskID, id)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	live, gone := insert(false), insert(true)

	f := &attachments.Files{Dir: t.TempDir()}
	rows := []*store.Attachment{live, gone}
	imgs := []*attachments.Image{{Data: []byte("one"), Thumb: []byte("t1")}, {Data: []byte("two"), Thumb: []byte("t2")}}
	if err := f.Place(imgs)(taskID, rows); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(f.TaskDir(taskID), "99.png.tmp")
	orphan := filepath.Join(f.Dir, "987654321")
	other := filepath.Join(f.Dir, "lost+found")
	for _, d := range []string{orphan, other} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{stray, filepath.Join(orphan, "1.png")} {
		if err := os.WriteFile(p, []byte("x"), 0o640); err != nil {
			t.Fatal(err)
		}
	}

	if err := f.SweepAll(ctx, st); err != nil {
		t.Fatal(err)
	}
	if !exists(t, f.Path(live)) || !exists(t, f.ThumbPath(live)) {
		t.Fatal("live attachment was removed")
	}
	if exists(t, f.Path(gone)) || exists(t, f.ThumbPath(gone)) || exists(t, stray) {
		t.Fatal("deleted attachment or stray file kept")
	}
	if exists(t, orphan) {
		t.Fatal("folder of an unknown task kept")
	}
	if !exists(t, other) {
		t.Fatal("non-task folder removed")
	}
	if got := attachments.AgentPath(live); got != "/attachments/"+filepath.Base(f.Path(live)) {
		t.Fatalf("agent path %q", got)
	}

	if n, err := st.DeleteAttachments(ctx, taskID); err != nil || n != 1 {
		t.Fatalf("DeleteAttachments = %d, %v", n, err)
	}
	if err := f.Sweep(ctx, st, taskID); err != nil {
		t.Fatal(err)
	}
	if exists(t, f.TaskDir(taskID)) {
		t.Fatal("task folder kept after its last attachment was deleted")
	}
}

func TestWatchSweepsWhenTaskCompletes(t *testing.T) {
	st := testutil.DB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := st.CreateProject(ctx, "demo", store.ProjectFields{}); err != nil {
		t.Fatal(err)
	}
	repo, _ := st.AddRepository(ctx, store.Repository{ProjectID: "demo", FullName: "o/r", BaseBranch: "dev", InstallationID: 1})
	f := &attachments.Files{Dir: t.TempDir()}
	img := &attachments.Image{Name: "a.png", Mime: "image/png", Width: 1, Height: 1, Data: []byte("x"), Thumb: []byte("t")}
	up := &store.Upload{Items: []store.NewAttachment{{Name: "a.png", Mime: "image/png", Size: 1, Width: 1, Height: 1}}, Place: f.Place([]*attachments.Image{img})}
	task, err := st.CreateTask(ctx, store.Task{ProjectID: "demo", RepositoryID: repo.ID, Title: "t", Prompt: "p", Source: "api"}, up)
	if err != nil {
		t.Fatal(err)
	}
	run, _ := st.PendingRun(ctx, task.ID)
	_ = st.MarkRunStarting(ctx, run.ID)

	hub := events.NewHub()
	go f.Watch(ctx, st, hub)
	time.Sleep(50 * time.Millisecond) // let Watch subscribe
	nt, err := st.SetStatus(ctx, task.ID, nil, "completed", "", true)
	if err != nil {
		t.Fatal(err)
	}
	hub.PublishTask(nt)
	for deadline := time.Now().Add(5 * time.Second); exists(t, f.TaskDir(task.ID)); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("files of a completed task were not removed")
		}
	}
}
